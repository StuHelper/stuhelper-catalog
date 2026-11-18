package catalog

import (
	"context"
	"crypto/rand"
	"errors"
	"time"
)

const ProjectionChannel = "catalog_projection"
const ProjectionRebuildTimeout = 15 * time.Minute

type maintenanceRebuildKey struct{}

func deferMaintenanceRebuildContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, maintenanceRebuildKey{}, true)
}

func MaintenanceRebuildDeferred(ctx context.Context) bool {
	v, _ := ctx.Value(maintenanceRebuildKey{}).(bool)
	return v
}

type ProjectionFailure struct {
	Reason   string    `json:"reason"`
	FailedAt time.Time `json:"failed_at"`
}

type ProjectionRebuildRun struct {
	ID            string           `json:"id"`
	SchoolID      string           `json:"school_id"`
	Status        string           `json:"status"`
	Policy        *DirectoryPolicy `json:"policy,omitempty"`
	RequestedAt   time.Time        `json:"requested_at"`
	StartedAt     *time.Time       `json:"started_at"`
	FinishedAt    *time.Time       `json:"finished_at"`
	DeadlineAt    time.Time        `json:"deadline_at"`
	DurationMS    int64            `json:"duration_ms"`
	Version       *int64           `json:"version"`
	FailureReason string           `json:"failure_reason"`
}

type DirectoryProjectionStatus struct {
	Published         *ProjectionReport     `json:"published"`
	LatestFailure     *ProjectionFailure    `json:"latest_failure"`
	LatestRun         *ProjectionRebuildRun `json:"latest_run"`
	SourceRevision    int64                 `json:"source_revision"`
	PublishedRevision int64                 `json:"published_revision"`
	SourceRevisionLag int64                 `json:"source_revision_lag"`
	PendingRebuild    bool                  `json:"pending_rebuild"`
}

type ProjectionAdminTransaction interface {
	DirectoryProjectionStatus(school string, now time.Time) (DirectoryProjectionStatus, error)
	QueueDirectoryRebuild(run ProjectionRebuildRun, policyDigest, actor string, now time.Time) (ProjectionRebuildRun, error)
	ClaimDirectoryRebuild(school, actor string, now time.Time) (*ProjectionRebuildRun, error)
	FinishDirectoryRebuild(id string, version *int64, failureReason, actor string, now time.Time) (ProjectionRebuildRun, error)
	PendingProjectionUpdatedAt(school string) (*time.Time, error)
	ClearPendingProjection(school string, upTo time.Time) error
	ExpireDirectoryRebuildIfNeeded(school string, now time.Time) error
	RecordProjectionAudit(Event) error
}

func projectionAdminPort(t Transaction) (ProjectionAdminTransaction, error) {
	port, ok := t.(ProjectionAdminTransaction)
	if !ok {
		return nil, ErrUnavailable
	}
	return port, nil
}

func (s *Service) projectionAdminAccess(ctx context.Context, p Principal, school, action string) error {
	if p.Subject == "" {
		return ErrUnauthenticated
	}
	if !ValidID(school) {
		return ErrInput
	}
	if p.CanMaintain && allowed(p, school) {
		return nil
	}
	if err := s.Repo.Transaction(ctx, func(t Transaction) error {
		port, err := projectionAdminPort(t)
		if err != nil {
			return err
		}
		return port.RecordProjectionAudit(Event{
			ID:       "projection-admin:" + rand.Text(),
			Subject:  "school/" + school,
			Type:     "catalog.directory-projection." + action + ".v1",
			SchoolID: school,
			Actor:    p.Subject,
			Result:   "denied",
			Time:     s.now(),
		})
	}); err != nil {
		return err
	}
	return ErrForbidden
}

func (s *Service) AdminDirectoryProjection(ctx context.Context, p Principal, school string) (DirectoryProjectionStatus, error) {
	var out DirectoryProjectionStatus
	if err := s.projectionAdminAccess(ctx, p, school, "status-viewed"); err != nil {
		return out, err
	}
	now := s.now()
	if err := s.Repo.Transaction(ctx, func(t Transaction) error {
		port, err := projectionAdminPort(t)
		if err != nil {
			return err
		}
		return port.ExpireDirectoryRebuildIfNeeded(school, now)
	}); err != nil {
		return out, err
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		port, err := projectionAdminPort(t)
		if err != nil {
			return err
		}
		out, err = port.DirectoryProjectionStatus(school, now)
		if err != nil {
			return err
		}
		return port.RecordProjectionAudit(Event{
			ID:       "projection-admin:" + rand.Text(),
			Subject:  "school/" + school,
			Type:     "catalog.directory-projection.status-viewed.v1",
			SchoolID: school,
			Actor:    p.Subject,
			Result:   "success",
			Time:     now,
		})
	})
	return out, err
}

func (s *Service) QueueDirectoryRebuild(ctx context.Context, p Principal, school, key string, policy *DirectoryPolicy) (ProjectionRebuildRun, error) {
	var out ProjectionRebuildRun
	if err := s.projectionAdminAccess(ctx, p, school, "triggered"); err != nil {
		return out, err
	}
	if !ValidKey(key) || (policy != nil && !policy.Valid()) {
		return out, ErrInput
	}
	now := s.now()
	id := "projection-rebuild-" + digest([]string{p.Subject, school, key})
	policyHash := digest(policy)
	run := ProjectionRebuildRun{
		ID:          id,
		SchoolID:    school,
		Status:      "queued",
		Policy:      policy,
		RequestedAt: now,
		DeadlineAt:  now.Add(ProjectionRebuildTimeout),
	}
	var rejected error
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		port, err := projectionAdminPort(t)
		if err != nil {
			return err
		}
		out, err = port.QueueDirectoryRebuild(run, policyHash, p.Subject, now)
		if errors.Is(err, ErrConflict) {
			rejected = ErrConflict
			return port.RecordProjectionAudit(Event{
				ID:       "projection-admin:" + rand.Text(),
				Subject:  id,
				Type:     "catalog.directory-projection.triggered.v1",
				SchoolID: school,
				Actor:    p.Subject,
				Result:   "denied",
				Time:     now,
			})
		}
		if err != nil {
			return err
		}
		return port.RecordProjectionAudit(Event{
			ID:       "projection-admin:" + rand.Text(),
			Subject:  id,
			Type:     "catalog.directory-projection.triggered.v1",
			SchoolID: school,
			Actor:    p.Subject,
			Result:   "success",
			Time:     now,
		})
	})
	if err != nil {
		return ProjectionRebuildRun{}, err
	}
	return out, rejected
}

func (s *Service) ExecuteDirectoryRebuild(ctx context.Context, p Principal, school string) (*ProjectionRebuildRun, error) {
	if p.Subject == "" {
		return nil, ErrUnauthenticated
	}
	if (!p.CanImport && !p.CanMaintain) || !allowed(p, school) {
		return nil, ErrForbidden
	}
	if !ValidID(school) {
		return nil, ErrInput
	}
	var claimed *ProjectionRebuildRun
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		port, err := projectionAdminPort(t)
		if err != nil {
			return err
		}
		var e error
		claimed, e = port.ClaimDirectoryRebuild(school, p.Subject, s.now())
		return e
	})
	if err != nil || claimed == nil {
		return claimed, err
	}
	buildCtx, cancel := context.WithTimeout(ctx, min(10*time.Minute, claimed.DeadlineAt.Sub(s.now())))
	defer cancel()
	var finished ProjectionRebuildRun
	buildErr := s.Repo.Transaction(buildCtx, func(t Transaction) error {
		port, ok := t.(DirectoryProjectionTransaction)
		if !ok {
			return ErrUnavailable
		}
		adminPort, err := projectionAdminPort(t)
		if err != nil {
			return err
		}
		pendingAt, err := adminPort.PendingProjectionUpdatedAt(school)
		if err != nil {
			return err
		}
		report, err := port.RebuildDirectory(school, claimed.Policy, s.now())
		if err != nil {
			return err
		}
		if pendingAt != nil {
			if err := adminPort.ClearPendingProjection(school, *pendingAt); err != nil {
				return err
			}
		}
		if err := t.Event(Event{
			ID:       "directory-projection:" + digest([]any{school, report.Version}),
			Subject:  "school/" + school,
			Type:     "catalog.directory.rebuilt.v1",
			SchoolID: school,
			Actor:    p.Subject,
			Result:   "success",
			Revision: report.Version,
			Time:     s.now(),
		}); err != nil {
			return err
		}
		finished, err = adminPort.FinishDirectoryRebuild(claimed.ID, &report.Version, "", p.Subject, s.now())
		return err
	})
	if buildErr == nil {
		return &finished, nil
	}
	failure := ""
	if errors.Is(buildErr, context.DeadlineExceeded) {
		failure = "build_timeout"
	} else if errors.Is(buildErr, context.Canceled) {
		failure = "build_canceled"
	} else if errors.Is(buildErr, ErrConflict) {
		failure = "import_conflict"
	} else {
		failure = "build_failed"
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	if err := s.Repo.Transaction(finishCtx, func(t Transaction) error {
		port, err := projectionAdminPort(t)
		if err != nil {
			return err
		}
		var e error
		finished, e = port.FinishDirectoryRebuild(claimed.ID, nil, failure, p.Subject, s.now())
		return e
	}); err != nil {
		return nil, err
	}
	return &finished, buildErr
}

func (s *Service) FlushPendingProjection(ctx context.Context, p Principal, school string) (*ProjectionReport, error) {
	if p.Subject == "" {
		return nil, ErrUnauthenticated
	}
	if (!p.CanImport && !p.CanMaintain) || !allowed(p, school) {
		return nil, ErrForbidden
	}
	if !ValidID(school) {
		return nil, ErrInput
	}
	var out *ProjectionReport
	buildCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	err := s.Repo.Transaction(buildCtx, func(t Transaction) error {
		adminPort, err := projectionAdminPort(t)
		if err != nil {
			return err
		}
		pendingAt, err := adminPort.PendingProjectionUpdatedAt(school)
		if err != nil || pendingAt == nil {
			return err
		}
		projPort, ok := t.(DirectoryProjectionTransaction)
		if !ok {
			return ErrUnavailable
		}
		out, err = projPort.RebuildPublishedDirectory(school, s.now())
		if err != nil {
			return err
		}
		return adminPort.ClearPendingProjection(school, *pendingAt)
	})
	return out, err
}

// RunProjectionWorker coalesces pending single-object maintenance mutations
// within the debounce window and executes queued admin policy rebuilds.
func (s *Service) RunProjectionWorker(ctx context.Context, p Principal, school string, debounce time.Duration) error {
	if p.Subject == "" {
		return ErrUnauthenticated
	}
	if (!p.CanImport && !p.CanMaintain) || !allowed(p, school) {
		return ErrForbidden
	}
	if !ValidID(school) || debounce <= 0 || debounce > time.Minute {
		return ErrInput
	}
	ticker := time.NewTicker(debounce)
	defer ticker.Stop()
	var observedPending *time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			_, _ = s.ExecuteDirectoryRebuild(ctx, p, school)
			var current *time.Time
			_ = s.Repo.Transaction(ctx, func(t Transaction) error {
				port, err := projectionAdminPort(t)
				if err != nil {
					return err
				}
				current, err = port.PendingProjectionUpdatedAt(school)
				return err
			})
			if current == nil {
				observedPending = nil
				continue
			}
			// Wait at least one quiet debounce interval after the latest mutation
			// so rapid consecutive edits coalesce into a single projection build.
			if observedPending != nil && !current.After(*observedPending) {
				_, _ = s.FlushPendingProjection(ctx, p, school)
				observedPending = nil
			} else {
				ts := *current
				observedPending = &ts
			}
		}
	}
}
