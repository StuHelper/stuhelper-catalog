package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// EnrollmentSource is a trusted in-process adapter. Subject is supplied by
// verified authentication. A complete snapshot replaces only that owner's
// records from one source/school; a failed read never becomes an empty snapshot.
type EnrollmentSource interface {
	OwnSnapshot(context.Context, string) (EnrollmentSnapshot, error)
}
type EnrollmentSnapshot struct {
	Subject     string
	SchoolID    string
	SourceID    string
	Version     string
	Revision    int64
	FetchedAt   time.Time
	Enrollments []Enrollment
}

type sourceMarker struct {
	Version  string
	Revision int64
	Result   ImportResult
}

func (s *Service) refreshOwn(ctx context.Context, p Principal) error {
	if s.EnrollmentSource == nil {
		return nil
	}
	snapshot, err := s.EnrollmentSource.OwnSnapshot(ctx, p.Subject)
	if err != nil {
		return ErrUnavailable
	}
	if snapshot.Subject != p.Subject {
		return ErrUnavailable
	}
	_, err = s.ReplaceOwnEnrollments(ctx, Principal{Subject: "urn:stuhelper:catalog-source:" + snapshot.SourceID, Schools: []string{snapshot.SchoolID}, CanImport: true}, snapshot)
	return err
}

// ReplaceOwnEnrollments applies a bounded, complete source snapshot atomically.
// It is a Go maintenance port, not an HTTP endpoint. The durable source marker
// prevents repeated reads (including after 24 hours) from duplicating writes.
func (s *Service) ReplaceOwnEnrollments(ctx context.Context, p Principal, in EnrollmentSnapshot) (ImportResult, error) {
	var out ImportResult
	if !p.CanImport || !allowed(p, in.SchoolID) {
		return out, ErrForbidden
	}
	if in.Subject == "" || len(in.Subject) > 512 || !ValidID(in.SchoolID) || !ValidID(in.SourceID) || in.Version == "" || len(in.Version) > 256 || in.Revision < 1 || in.FetchedAt.IsZero() || in.FetchedAt.After(s.now().Add(time.Minute)) || len(in.Enrollments) > 2000 {
		return out, ErrInput
	}
	in.Enrollments = slices.Clone(in.Enrollments)
	slices.SortFunc(in.Enrollments, func(a, b Enrollment) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	for i := range in.Enrollments {
		en := &in.Enrollments[i]
		if en.Subject != in.Subject || en.SchoolID != in.SchoolID || !en.Active || i > 0 && en.ID == in.Enrollments[i-1].ID {
			return out, ErrInput
		}
		en.TeacherIDs = slices.Clone(en.TeacherIDs)
		slices.Sort(en.TeacherIDs)
		en.TeacherIDs = slices.Compact(en.TeacherIDs)
		en.SourceID = in.SourceID
		en.SourceRevision = in.Revision
		en.FetchedAt = in.FetchedAt
	}
	key := digest([]string{"own-source", in.Subject, in.SchoolID, in.SourceID})
	h := digest(in)
	now := s.now()
	err := s.Repo.Transaction(ctx, func(tx Transaction) error {
		if err := tx.Lock("own-source:" + key); err != nil {
			return err
		}
		previous, err := tx.Replay(key)
		if err == nil {
			var marker sourceMarker
			if json.Unmarshal(previous.Result, &marker) != nil {
				return ErrUnavailable
			}
			if previous.Digest == h {
				out = marker.Result
				return nil
			}
			if marker.Version == in.Version || marker.Revision > in.Revision {
				return ErrConflict
			}
		}
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		old, err := tx.SourceEnrollments(in.Subject, in.SchoolID, in.SourceID, 2001)
		if err != nil {
			return err
		}
		if len(old) > 2000 {
			return ErrUnavailable
		}
		seen := make(map[string]bool, len(in.Enrollments))
		for _, en := range in.Enrollments {
			seen[en.ID] = true
			if err := importEnrollmentState(tx, en, Import{SchoolID: in.SchoolID, SourceID: in.SourceID, Revision: in.Revision, FetchedAt: in.FetchedAt}, true); err != nil {
				return err
			}
		}
		for _, en := range old {
			if en.SourceRevision > in.Revision {
				return ErrConflict
			}
			if !seen[en.ID] && en.Active {
				en.Active = false
				en.SourceRevision = in.Revision
				en.FetchedAt = in.FetchedAt
				if err := tx.SaveEnrollment(en); err != nil {
					return err
				}
			}
		}
		var event [16]byte
		if _, err := rand.Read(event[:]); err != nil {
			return ErrUnavailable
		}
		out = ImportResult{ID: hex.EncodeToString(event[:]), Enrollments: len(in.Enrollments)}
		if err := tx.Event(Event{ID: out.ID, Subject: "enrollments/" + key, Type: "catalog.enrollments_refreshed.v1", SchoolID: in.SchoolID, Actor: p.Subject, Result: "success", Revision: in.Revision, Time: now}); err != nil {
			return err
		}
		encoded, err := json.Marshal(sourceMarker{Version: in.Version, Revision: in.Revision, Result: out})
		if err != nil {
			return err
		}
		return tx.SaveReplay(key, Replay{Digest: h, Result: encoded})
	})
	return out, err
}
