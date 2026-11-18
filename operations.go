package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
)

type MergeInput struct {
	TargetID       string `json:"target_id"`
	SourceRevision int64  `json:"source_revision"`
	TargetRevision int64  `json:"target_revision"`
	Reason         string `json:"reason"`
	PreviewToken   string `json:"preview_token,omitempty"`
}
type MergePreview struct {
	Source       Object `json:"source"`
	Target       Object `json:"target"`
	Aliases      int    `json:"aliases"`
	PreviewToken string `json:"preview_token"`
}
type ObjectState struct {
	Revision int64  `json:"revision"`
	Inactive bool   `json:"inactive"`
	Reason   string `json:"reason"`
}
type Resolution struct {
	Object Object   `json:"object"`
	IDs    []string `json:"ids"`
}

func (s *Service) Resolve(ctx context.Context, id string) (Resolution, error) {
	out := Resolution{}
	if !ValidID(id) {
		return out, ErrInput
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		var e error
		out.Object, e = t.PublicObject(id)
		if e != nil {
			return e
		}
		out.IDs, e = t.ResolveIDs(id)
		return e
	})
	return out, err
}
func mergePlan(t Transaction, p Principal, id string, in MergeInput) (MergePreview, error) {
	out := MergePreview{}
	if err := maintainRead(p); err != nil {
		return out, err
	}
	if !ValidID(id) || !ValidID(in.TargetID) || id == in.TargetID || strings.TrimSpace(in.Reason) == "" || !validText(in.Reason, 2000) {
		return out, ErrInput
	}
	source, e := t.Object(id)
	if e != nil {
		return out, e
	}
	target, e := t.Object(in.TargetID)
	if e != nil {
		return out, e
	}
	if !allowed(p, source.Object.SchoolID) || !allowed(p, target.Object.SchoolID) {
		return out, ErrForbidden
	}
	if source.Object.SchoolID != target.Object.SchoolID || source.Object.Kind != target.Object.Kind || !slices.Contains([]string{"course", "teacher"}, source.Object.Kind) || source.Object.Revision != in.SourceRevision || target.Object.Revision != in.TargetRevision || target.Object.Inactive {
		return out, ErrConflict
	}
	a, e := t.PublicObject(id)
	if e != nil {
		return out, e
	}
	b, e := t.PublicObject(in.TargetID)
	if e != nil {
		return out, e
	}
	if a.CanonicalID != "" || b.CanonicalID != "" {
		return out, ErrConflict
	}
	if source.Object.Kind == "course" {
		copy := target.Object
		if err := addFormer(&copy, source.Object); err != nil {
			return out, err
		}
	} else if !slices.Contains(target.Object.Aliases, source.Object.Name) && len(target.Object.Aliases) >= 50 {
		return out, ErrInput
	}
	ids, e := t.ResolveIDs(id)
	if e != nil {
		return out, e
	}
	targetIDs, e := t.ResolveIDs(in.TargetID)
	if e != nil {
		return out, e
	}
	if len(ids)+len(targetIDs) > 1000 {
		return out, ErrInput
	}
	out = MergePreview{source.Object, target.Object, len(ids), digest([]any{source, target, ids, targetIDs, in.Reason})}
	return out, nil
}
func (s *Service) PreviewMerge(ctx context.Context, p Principal, id string, in MergeInput) (MergePreview, error) {
	if err := maintainRead(p); err != nil {
		return MergePreview{}, err
	}
	ctx = teacherMaintenanceContext(ctx)
	var out MergePreview
	err := s.Repo.Transaction(ctx, func(t Transaction) error { var e error; out, e = mergePlan(t, p, id, in); return e })
	return out, err
}

func (s *Service) maintainWrite(ctx context.Context, p Principal, id, action, key string, input any, fn func(t Transaction, rec Record, rk string, now time.Time) (Object, Event, error)) (Object, error) {
	if err := maintainRead(p); err != nil {
		return Object{}, err
	}
	if !ValidID(id) || !ValidKey(key) {
		return Object{}, ErrInput
	}
	ctx = teacherMaintenanceContext(ctx)
	if s.AsyncMaintenanceRebuild {
		ctx = deferMaintenanceRebuildContext(ctx)
	}
	var out Object
	rk := digest([]string{p.Subject, action, id, key})
	h := digest(input)
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		initial, e := t.Object(id)
		if e != nil {
			return e
		}
		if !allowed(p, initial.Object.SchoolID) {
			return ErrForbidden
		}
		if e = t.Lock(initial.Object.SchoolID); e != nil {
			return e
		}
		rec, e := t.Object(id)
		if e != nil {
			return e
		}
		now := s.now()
		cached, e := t.Replay(rk)
		if e == nil && now.Before(cached.Until) {
			if cached.Digest != h {
				return ErrConflict
			}
			return json.Unmarshal(cached.Result, &out)
		}
		if e != nil && !errors.Is(e, ErrNotFound) {
			return e
		}
		public, e := t.PublicObject(id)
		if e != nil {
			return e
		}
		if public.CanonicalID != "" {
			return ErrConflict
		}
		var ev Event
		out, ev, e = fn(t, rec, rk, now)
		if e != nil {
			return e
		}
		if e = t.Event(ev); e != nil {
			return e
		}
		data, e := json.Marshal(out)
		if e != nil {
			return e
		}
		return t.SaveReplay(rk, Replay{Digest: h, Result: data, Until: now.Add(24 * time.Hour), Expires: true})
	})
	return out, err
}

func (s *Service) Merge(ctx context.Context, p Principal, id, key string, in MergeInput) (Object, error) {
	if err := maintainRead(p); err != nil {
		return Object{}, err
	}
	if !ValidID(id) || !ValidKey(key) || in.PreviewToken == "" {
		return Object{}, ErrInput
	}
	return s.maintainWrite(ctx, p, id, "merge", key, in, func(t Transaction, _ Record, rk string, now time.Time) (Object, Event, error) {
		plan, e := mergePlan(t, p, id, in)
		if e != nil {
			return Object{}, Event{}, e
		}
		if plan.PreviewToken != in.PreviewToken {
			return Object{}, Event{}, ErrConflict
		}
		if _, e = t.MergeAliases(id, in.TargetID, plan.Source.SchoolID, now, in.Reason); e != nil {
			return Object{}, Event{}, e
		}
		source, e := t.Object(id)
		if e != nil {
			return Object{}, Event{}, e
		}
		source.Object.Revision++
		source.Object.UpdatedAt = now
		if e = t.SaveObject(source); e != nil {
			return Object{}, Event{}, e
		}
		target, e := t.Object(in.TargetID)
		if e != nil {
			return Object{}, Event{}, e
		}
		target.Object.Revision++
		target.Object.UpdatedAt = now
		if plan.Source.Kind == "course" {
			if err := addFormer(&target.Object, plan.Source); err != nil {
				return Object{}, Event{}, err
			}
		} else if !slices.Contains(target.Object.Aliases, plan.Source.Name) {
			target.Object.Aliases = append(target.Object.Aliases, plan.Source.Name)
		}
		if e = t.SaveObject(target); e != nil {
			return Object{}, Event{}, e
		}
		return target.Object, Event{Result: "success", ID: rk, Subject: id, Type: "catalog.merged.v1", Reason: "catalog.merge", SchoolID: target.Object.SchoolID, Actor: p.Subject, Revision: source.Object.Revision, Time: now}, nil
	})
}

func (s *Service) SetState(ctx context.Context, p Principal, id, key string, in ObjectState) (Object, error) {
	if err := maintainRead(p); err != nil {
		return Object{}, err
	}
	if !ValidID(id) || !ValidKey(key) || strings.TrimSpace(in.Reason) == "" || !validText(in.Reason, 2000) {
		return Object{}, ErrInput
	}
	return s.maintainWrite(ctx, p, id, "state", key, in, func(t Transaction, old Record, rk string, now time.Time) (Object, Event, error) {
		if old.Object.Revision != in.Revision || !slices.Contains([]string{"course", "teacher"}, old.Object.Kind) {
			return Object{}, Event{}, ErrConflict
		}
		if old.Manual == nil {
			old.Manual = map[string]string{}
		}
		old.Manual["inactive_reason"] = in.Reason
		old.Manual["inactive"] = strconv.FormatBool(in.Inactive)
		old.Object.Inactive = in.Inactive
		old.Object.Revision++
		old.Object.UpdatedAt = now
		if e := t.SaveObject(old); e != nil {
			return Object{}, Event{}, e
		}
		return old.Object, Event{Result: "success", ID: rk, Subject: id, Type: "catalog.state_changed.v1", Reason: "catalog.state_change", SchoolID: old.Object.SchoolID, Actor: p.Subject, Revision: old.Object.Revision, Time: now}, nil
	})
}

// Delete logically removes a directory object while preserving academic history.
// The resulting discovery tombstone has a higher revision; SetState may restore it.
func (s *Service) Delete(ctx context.Context, p Principal, id, key string, in ObjectState) (Object, error) {
	in.Inactive = true
	return s.SetState(ctx, p, id, key, in)
}
