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
	if !p.CanMaintain || !ValidID(id) || !ValidID(in.TargetID) || id == in.TargetID || strings.TrimSpace(in.Reason) == "" || !validText(in.Reason, 2000) {
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
	var out MergePreview
	err := s.Repo.Transaction(ctx, func(t Transaction) error { var e error; out, e = mergePlan(t, p, id, in); return e })
	return out, err
}
func (s *Service) Merge(ctx context.Context, p Principal, id, key string, in MergeInput) (Object, error) {
	var out Object
	if !ValidKey(key) || in.PreviewToken == "" {
		return out, ErrInput
	}
	rk := digest([]string{p.Subject, "merge", id, key})
	h := digest(in)
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		original, e := t.Object(id)
		if e != nil {
			return e
		}
		if !p.CanMaintain || !allowed(p, original.Object.SchoolID) {
			return ErrForbidden
		}
		if e = t.Lock(original.Object.SchoolID); e != nil {
			return e
		}
		cached, e := t.Replay(rk)
		if e == nil && s.now().Before(cached.Until) {
			if cached.Digest != h {
				return ErrConflict
			}
			return json.Unmarshal(cached.Result, &out)
		}
		if e != nil && !errors.Is(e, ErrNotFound) {
			return e
		}
		plan, e := mergePlan(t, p, id, in)
		if e != nil {
			return e
		}
		if plan.PreviewToken != in.PreviewToken {
			return ErrConflict
		}
		if _, e = t.MergeAliases(id, in.TargetID, plan.Source.SchoolID, s.now(), in.Reason); e != nil {
			return e
		}
		target, e := t.Object(in.TargetID)
		if e != nil {
			return e
		}
		target.Object.Revision++
		target.Object.UpdatedAt = s.now()
		if !slices.Contains(target.Object.Aliases, plan.Source.Name) && len(target.Object.Aliases) < 50 {
			target.Object.Aliases = append(target.Object.Aliases, plan.Source.Name)
		}
		if e = t.SaveObject(target); e != nil {
			return e
		}
		out = target.Object
		if e = t.Event(Event{ID: rk, Subject: id, Type: "catalog.merged.v1", SchoolID: out.SchoolID, Actor: p.Subject, Revision: out.Revision, Time: s.now()}); e != nil {
			return e
		}
		data, _ := json.Marshal(out)
		return t.SaveReplay(rk, Replay{h, data, s.now().Add(24 * time.Hour)})
	})
	return out, err
}
func (s *Service) SetState(ctx context.Context, p Principal, id, key string, in ObjectState) (Object, error) {
	var out Object
	if !ValidID(id) || !ValidKey(key) || strings.TrimSpace(in.Reason) == "" || !validText(in.Reason, 2000) {
		return out, ErrInput
	}
	rk := digest([]string{p.Subject, "state", id, key})
	h := digest(in)
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		old, e := t.Object(id)
		if e != nil {
			return e
		}
		if !p.CanMaintain || !allowed(p, old.Object.SchoolID) {
			return ErrForbidden
		}
		if e = t.Lock(old.Object.SchoolID); e != nil {
			return e
		}
		old, e = t.Object(id)
		if e != nil {
			return e
		}
		cached, e := t.Replay(rk)
		if e == nil && s.now().Before(cached.Until) {
			if cached.Digest != h {
				return ErrConflict
			}
			return json.Unmarshal(cached.Result, &out)
		}
		if e != nil && !errors.Is(e, ErrNotFound) {
			return e
		}
		if old.Object.Revision != in.Revision || !slices.Contains([]string{"course", "teacher"}, old.Object.Kind) {
			return ErrConflict
		}
		public, e := t.PublicObject(id)
		if e != nil {
			return e
		}
		if public.CanonicalID != "" {
			return ErrConflict
		}
		if old.Manual == nil {
			old.Manual = map[string]string{}
		}
		old.Manual["inactive_reason"] = in.Reason
		old.Manual["inactive"] = strconv.FormatBool(in.Inactive)
		old.Object.Inactive = in.Inactive
		old.Object.Revision++
		old.Object.UpdatedAt = s.now()
		if e = t.SaveObject(old); e != nil {
			return e
		}
		out = old.Object
		if e = t.Event(Event{ID: rk, Subject: id, Type: "catalog.state_changed.v1", SchoolID: out.SchoolID, Actor: p.Subject, Revision: out.Revision, Time: s.now()}); e != nil {
			return e
		}
		data, _ := json.Marshal(out)
		return t.SaveReplay(rk, Replay{h, data, s.now().Add(24 * time.Hour)})
	})
	return out, err
}
