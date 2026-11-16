package catalog_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	c "github.com/StuHelper/stuhelper-catalog"
	cp "github.com/StuHelper/stuhelper-catalog/postgres"
	"slices"
	"testing"
	"time"
)

func TestEnrollmentAndTermOptionsUseStructuredOrder(t *testing.T) {
	s, p, _, _ := projectionFixture(t)
	ctx := context.Background()
	terms := []string{"unknown", "z-spring", "a-autumn", "m-summer", "a-autumn"}
	in := c.Import{SchoolID: "school", SourceID: "enrollment-source", Revision: 1, FetchedAt: time.Now()}
	for i, term := range terms {
		in.Enrollments = append(in.Enrollments, c.Enrollment{ID: fmt.Sprintf("en-%d", i), Subject: p.Subject, SchoolID: "school", CourseID: "course", TermID: term, TeacherIDs: []string{}, Active: true})
	}
	in.Enrollments = append(in.Enrollments, c.Enrollment{ID: "other-user", Subject: "issuer:other", SchoolID: "school", CourseID: "course", TermID: "a-autumn", TeacherIDs: []string{}, Active: true})
	if _, err := s.Import(ctx, p, "enrollments", in); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	keys := []int{}
	after := ""
	for range 4 {
		page, err := s.MyEnrollments(ctx, p, "", "", "", after, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, en := range page {
			ids = append(ids, en.ID)
			if en.TermOrderKey == nil {
				t.Fatal("missing read order key")
			}
			keys = append(keys, *en.TermOrderKey)
		}
		after = page[len(page)-1].ID
	}
	if !slices.Equal(ids, []string{"en-2", "en-4", "en-3", "en-1", "en-0"}) || !slices.Equal(keys, []int{20243, 20243, 20242, 20241, 0}) {
		t.Fatal(ids, keys)
	}
	page, err := s.MyEnrollments(ctx, p, "", "", "", "other-user", 100)
	if err != nil || len(page) != 0 {
		t.Fatal("foreign cursor leaked data", page, err)
	}
	page, err = s.MyEnrollments(ctx, p, "", "", "z-spring", "en-2", 100)
	if err != nil || len(page) != 0 {
		t.Fatal("cursor escaped term filter", page, err)
	}
	if _, err = s.MyEnrollments(ctx, c.Principal{}, "", "", "", "", 2); !errors.Is(err, c.ErrForbidden) {
		t.Fatal("anonymous enrollments", err)
	}
	for _, limit := range []int{0, 101} {
		if _, err = s.MyEnrollments(ctx, p, "", "", "", "", limit); !errors.Is(err, c.ErrInput) {
			t.Fatal("limit", err)
		}
	}
	options := []int{}
	after = ""
	for range 4 {
		page, err := s.ReadList(ctx, p, c.Filter{Kind: "term", SchoolID: "school", After: after, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, term := range page.Items {
			options = append(options, term.TermOrderKey)
			got, err := s.ReadLookup(ctx, p, []string{term.ID})
			if err != nil || len(got.Items) != 1 || got.Items[0].TermOrderKey != term.TermOrderKey {
				t.Fatal("lookup order differs", got, err)
			}
		}
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	if !slices.Equal(options, []int{20243, 20242, 20241, 20233}) {
		t.Fatal("term options", options)
	}
	s.Repo.(*cp.Repository).RequireDirectoryProjection = false
	raw, err := s.ReadList(ctx, p, c.Filter{Kind: "term", SchoolID: "school", Limit: 100})
	if err != nil || len(raw.Items) != 7 {
		t.Fatal("raw terms", raw, err)
	}
	rawKeys := []int{}
	for _, v := range raw.Items {
		rawKeys = append(rawKeys, v.TermOrderKey)
		single, e := s.Read(ctx, p, v.ID)
		if e != nil || single.TermOrderKey != v.TermOrderKey {
			t.Fatal("raw detail key", single, e)
		}
	}
	if !slices.Equal(rawKeys, []int{20253, 20243, 20242, 20241, 20233, 20232, 0}) {
		t.Fatal("raw term order", rawKeys)
	}
	if _, err = s.ReadList(ctx, c.Principal{}, c.Filter{Kind: "term", Limit: 20}); !errors.Is(err, c.ErrUnauthenticated) {
		t.Fatal("anonymous terms", err)
	}
}

func TestTermOrderReadFieldPreservesLegacyImportReplay(t *testing.T) {
	s, p, in := fixture(t)
	ctx := context.Background()
	result, err := s.Import(ctx, p, "legacy-replay", in)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce the exact pre-B2 input shape, including its original JSON order.
	legacy := bytes.ReplaceAll(raw, []byte(`"term_order_key":0,`), nil)
	sum := sha256.Sum256(legacy)
	err = s.Repo.Transaction(ctx, func(tx c.Transaction) error {
		receipt, e := tx.Replay(result.ID)
		if e != nil {
			return e
		}
		receipt.Digest = hex.EncodeToString(sum[:])
		return tx.SaveReplay(result.ID, receipt)
	})
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Import(ctx, p, "legacy-replay", in)
	if err != nil || again.ID != result.ID {
		t.Fatal("legacy replay changed", again, err)
	}
	if bytes.Contains(raw, []byte(`"term_order_key"`)) {
		t.Fatal("derived read field changed persisted source input")
	}
	zero := 0
	in.Enrollments[0].TermOrderKey = &zero
	if _, err = s.Import(ctx, p, "forged-read-field", in); !errors.Is(err, c.ErrInput) {
		t.Fatal("import accepted derived order key", err)
	}
}
