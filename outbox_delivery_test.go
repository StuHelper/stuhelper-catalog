package catalog_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/postgres"
	"github.com/jackc/pgx/v5/stdlib"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOutboxNotificationBacklogOrderAndConfirmation(t *testing.T) {
	repo, db := database(t)
	ctx := context.Background()
	listener, e := repo.DB.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	if _, e = listener.ExecContext(ctx, "LISTEN "+c.OutboxChannel); e != nil {
		t.Fatal(e)
	}
	publish := func(id, subject string, revision int64, rollback bool) error {
		return repo.Transaction(ctx, func(tx c.Transaction) error {
			if e := tx.Event(c.Event{ID: id, Type: "catalog.test.v1", Subject: subject, Revision: revision, Time: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)}); e != nil {
				return e
			}
			if rollback {
				return errors.New("rollback")
			}
			return nil
		})
	}
	if e = publish("rollback", "object", 1, true); e == nil {
		t.Fatal("expected rollback")
	}
	noNotice, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	e = listener.Raw(func(raw any) error { _, e := raw.(*stdlib.Conn).Conn().WaitForNotification(noNotice); return e })
	cancel()
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("rollback notified", e)
	}
	pending, e := repo.PendingOutbox(ctx, 100, nil)
	if e != nil || len(pending) != 0 {
		t.Fatal(pending, e)
	}
	var count int
	if e = db.QueryRow(`SELECT count(*) FROM catalog.audit`).Scan(&count); e != nil || count != 0 {
		t.Fatal("rollback audit", count, e)
	}
	// Publish revisions out of order to distinguish numeric order from timestamps/IDs.
	for _, v := range []struct {
		id, subject string
		revision    int64
	}{{"ten", "a", 10}, {"two", "a", 2}, {"one", "a", 1}, {"z-one", "z", 1}} {
		if e = publish(v.id, v.subject, v.revision, false); e != nil {
			t.Fatal(e)
		}
	}
	notice, stop := context.WithTimeout(ctx, time.Second)
	e = listener.Raw(func(raw any) error {
		n, e := raw.(*stdlib.Conn).Conn().WaitForNotification(notice)
		if e == nil && (n.Channel != c.OutboxChannel || n.Payload != "") {
			t.Error(n)
		}
		return e
	})
	stop()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = listener.ExecContext(ctx, "UNLISTEN *"); e != nil {
		t.Fatal(e)
	}
	// Accumulate without a listener, then read through a fresh repository instance.
	for i := 0; i < 101; i++ {
		if e = publish(fmt.Sprintf("backlog-%03d", i), "backlog", int64(i+1), false); e != nil {
			t.Fatal(e)
		}
	}
	restarted := &postgres.Repository{DB: repo.DB}
	pending, e = restarted.PendingOutbox(ctx, 100, nil)
	if e != nil || len(pending) != 100 {
		t.Fatal(len(pending), e)
	}
	if pending[0].ID != "one" || pending[1].ID != "two" || pending[2].ID != "ten" {
		t.Fatal(pending[:3])
	}
	again, e := restarted.PendingOutbox(ctx, 100, nil)
	if e != nil || !reflect.DeepEqual(again, pending) {
		t.Fatal("read changed delivery", e)
	}
	for i, event := range pending {
		if event.SpecVersion != "1.0" || event.Source != "catalog" || event.Subject == "" || event.Time.IsZero() || event.Data.Revision < 1 {
			t.Fatal(event)
		}
		if i > 0 {
			prev := pending[i-1]
			if prev.Subject > event.Subject || prev.Subject == event.Subject && prev.Data.Revision > event.Data.Revision {
				t.Fatal(prev, event)
			}
		}
		if e = restarted.ConfirmOutbox(ctx, event.ID); e != nil {
			t.Fatal(e)
		}
	}
	var first time.Time
	if e = db.QueryRow(`SELECT sent_at FROM catalog.outbox WHERE id=$1`, pending[0].ID).Scan(&first); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { errs <- repo.ConfirmOutbox(ctx, pending[0].ID) })
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var second time.Time
	if e = db.QueryRow(`SELECT sent_at FROM catalog.outbox WHERE id=$1`, pending[0].ID).Scan(&second); e != nil || !first.Equal(second) {
		t.Fatal(first, second, e)
	}
	rest, e := repo.PendingOutbox(ctx, 100, nil)
	if e != nil || len(rest) != 5 || rest[4].ID != "z-one" {
		t.Fatal(rest, e)
	}
	for _, v := range rest {
		if e = repo.ConfirmOutbox(ctx, v.ID); e != nil {
			t.Fatal(e)
		}
	}
	empty, e := repo.PendingOutbox(ctx, 1, nil)
	if e != nil || len(empty) != 0 {
		t.Fatal(empty, e)
	}
	for _, limit := range []int{-1, 0, 101} {
		if _, e = repo.PendingOutbox(ctx, limit, nil); !errors.Is(e, c.ErrInput) {
			t.Fatal(limit, e)
		}
	}
	if e = repo.ConfirmOutbox(ctx, "missing"); !errors.Is(e, c.ErrNotFound) {
		t.Fatal(e)
	}
	longID := "discovery:" + strings.Repeat("x", 128) + ":1"
	if e = publish(longID, "long", 1, false); e != nil {
		t.Fatal(e)
	}
	if e = repo.ConfirmOutbox(ctx, longID); e != nil {
		t.Fatal(e)
	}
	cancelled, done := context.WithCancel(ctx)
	done()
	if _, e = repo.PendingOutbox(cancelled, 1, nil); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if e = repo.ConfirmOutbox(cancelled, longID); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	var index string
	if e = db.QueryRow(`SELECT indexdef FROM pg_indexes WHERE schemaname='catalog' AND indexname='catalog_outbox_pending'`).Scan(&index); e != nil || !strings.Contains(index, "WHERE (sent_at IS NULL)") || !strings.Contains(index, "bigint") {
		t.Fatal(index, e)
	}
}

func TestOutboxDiscoveryAndTimetablePayloads(t *testing.T) {
	s, admin, in := fixture(t)
	ctx := context.Background()
	repo := s.Repo.(*postgres.Repository)
	for range 2 {
		if _, e := s.Import(ctx, admin, "seed", in); e != nil {
			t.Fatal(e)
		}
		if _, e := s.PutTimetable(ctx, admin, timetable()); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.Delete(ctx, admin, "teacher", "delete", c.ObjectState{Revision: 1, Reason: "private reason"}); e != nil {
		t.Fatal(e)
	}
	cfg := timetable()
	cfg.Revision = 1
	cfg.Periods[0].Start = "08:10"
	if _, e := s.PutTimetable(ctx, admin, cfg); e != nil {
		t.Fatal(e)
	}
	events, e := repo.PendingOutbox(ctx, 100, nil)
	if e != nil || len(events) != 9 {
		t.Fatal(len(events), e)
	}
	discovery, timetables, tombstones := 0, 0, 0
	for _, event := range events {
		// Marshaling a typed relay envelope must preserve the complete stored payload.
		var raw []byte
		if e = repo.DB.QueryRow(`SELECT data FROM catalog.outbox WHERE id=$1`, event.ID).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		encoded, e := json.Marshal(event)
		if e != nil {
			t.Fatal(e)
		}
		var stored, returned any
		if e = json.Unmarshal(raw, &stored); e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(encoded, &returned); e != nil || !reflect.DeepEqual(stored, returned) {
			t.Fatal(string(raw), string(encoded), e)
		}
		if strings.Contains(string(encoded), "private reason") || strings.Contains(string(encoded), "issuer:") {
			t.Fatal("private fields leaked", string(encoded))
		}
		switch event.Type {
		case "catalog.discovery.v1":
			discovery++
			if event.Data.Target == nil || event.Subject != event.Data.Target.ID || event.Data.Title == nil || event.Data.SchoolID == nil || event.Data.Visible == nil || event.Data.Ratings == nil || event.Data.At == nil {
				t.Fatal(event)
			}
			if !*event.Data.Visible {
				tombstones++
				if *event.Data.Title != "" || event.Data.Revision != 2 {
					t.Fatal(event)
				}
			}
		case "catalog.timetable.updated.v1":
			timetables++
			if event.Subject != "timetable/school/term1" || event.Data.Revision != int64(timetables) {
				t.Fatal(event)
			}
		}
		if e = repo.ConfirmOutbox(ctx, event.ID); e != nil {
			t.Fatal(e)
		}
	}
	if discovery != 5 || timetables != 2 || tombstones != 1 {
		t.Fatal(discovery, timetables, tombstones)
	}
	empty, e := repo.PendingOutbox(ctx, 100, nil)
	if e != nil || len(empty) != 0 {
		t.Fatal(empty, e)
	}
}

func TestOutboxMergeRevisionBelongsToSourceSubject(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	in.Objects = append(in.Objects, c.Object{ID: "duplicate", Kind: "course", SchoolID: "school", Name: "Duplicate"})
	if _, e := s.Import(ctx, p, "seed", in); e != nil {
		t.Fatal(e)
	}
	name := "Target updated"
	if _, e := s.Correct(ctx, p, "course", "correct", c.Correction{Revision: 1, Name: &name}); e != nil {
		t.Fatal(e)
	}
	input := c.MergeInput{TargetID: "course", SourceRevision: 1, TargetRevision: 2, Reason: "Confirmed duplicate"}
	preview, e := s.PreviewMerge(ctx, p, "duplicate", input)
	if e != nil {
		t.Fatal(e)
	}
	input.PreviewToken = preview.PreviewToken
	if _, e = s.Merge(ctx, p, "duplicate", "merge", input); e != nil {
		t.Fatal(e)
	}
	pending, e := s.Repo.(c.OutboxReader).PendingOutbox(ctx, 100, nil)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, v := range pending {
		if v.Type == "catalog.merged.v1" {
			found = true
			if v.Subject != "duplicate" || v.Data.Revision != 2 {
				t.Fatal(v)
			}
		}
	}
	if !found {
		t.Fatal("merge event missing")
	}
}

func TestOutboxExcludedSubjectsDoNotBlockOtherSubjects(t *testing.T) {
	repo, _ := database(t)
	ctx := context.Background()
	err := repo.Transaction(ctx, func(tx c.Transaction) error {
		for i := 1; i <= 100; i++ {
			if e := tx.Event(c.Event{ID: fmt.Sprintf("blocked-%03d", i), Type: "catalog.test.v1", Subject: "a-blocked", Revision: int64(i), Time: time.Now().UTC()}); e != nil {
				return e
			}
		}
		for _, v := range []struct {
			id, subject string
			rev         int64
		}{{"z-ten", "z-ready", 10}, {"z-two-b", "z-ready", 2}, {"z-two-a", "z-ready", 2}, {"b-one", "b-ready", 1}} {
			if e := tx.Event(c.Event{ID: v.id, Type: "catalog.test.v1", Subject: v.subject, Revision: v.rev, Time: time.Now().UTC()}); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	baseline, e := repo.PendingOutbox(ctx, 100, nil)
	if e != nil || len(baseline) != 100 || baseline[99].Subject != "a-blocked" {
		t.Fatal(baseline, e)
	}
	empty, e := repo.PendingOutbox(ctx, 100, []string{})
	if e != nil || !reflect.DeepEqual(empty, baseline) {
		t.Fatal("empty exclusion changed results", e)
	}
	result, e := repo.PendingOutbox(ctx, 100, []string{"a-blocked"})
	if e != nil || len(result) != 4 {
		t.Fatal(result, e)
	}
	want := []string{"b-one", "z-two-a", "z-two-b", "z-ten"}
	for i, v := range result {
		if v.ID != want[i] {
			t.Fatal(result)
		}
	}
	repeat, e := repo.PendingOutbox(ctx, 100, []string{"a-blocked", "a-blocked", "missing"})
	if e != nil || !reflect.DeepEqual(repeat, result) {
		t.Fatal(repeat, e)
	}
	one, e := repo.PendingOutbox(ctx, 1, []string{"a-blocked"})
	if e != nil || len(one) != 1 || one[0].ID != "b-one" {
		t.Fatal(one, e)
	}
	all, e := repo.PendingOutbox(ctx, 100, []string{"a-blocked", "b-ready", "z-ready"})
	if e != nil || len(all) != 0 {
		t.Fatal(all, e)
	}
	maximum := make([]string, 1000)
	for i := range maximum {
		maximum[i] = fmt.Sprintf("absent-%d", i)
	}
	maximum[999] = "a-blocked"
	boundary, e := repo.PendingOutbox(ctx, 100, maximum)
	if e != nil || !reflect.DeepEqual(boundary, result) {
		t.Fatal(boundary, e)
	}
	if _, e = repo.PendingOutbox(ctx, 100, append(maximum, "overflow")); !errors.Is(e, c.ErrInput) {
		t.Fatal("unbounded exclusion accepted", e)
	}
	// Exclusion has no delivery side effect: removing it makes the backlog visible.
	restored, e := repo.PendingOutbox(ctx, 100, nil)
	if e != nil || !reflect.DeepEqual(restored, baseline) {
		t.Fatal("excluded events changed", e)
	}
}
