package catalog_test

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"testing"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/postgres"
)

func TestPublicAddressesKeepAliasesAndRequireAdmission(t *testing.T) {
	ctx := context.Background()
	s, admin, in := fixture(t)
	user := c.Principal{Subject: "issuer:student"}
	for i := range in.Objects {
		if in.Objects[i].ID == "course" {
			in.Objects[i].Code = "B240012012"
		}
	}
	if _, err := s.Import(ctx, admin, "seed", in); err != nil {
		t.Fatal(err)
	}
	check := func(address, id, want string) {
		t.Helper()
		r, err := s.ReadAddress(ctx, user, "course", address)
		if err != nil || r.Object.ID != id || r.Object.AddressID != want {
			t.Fatalf("%s: %+v %v", address, r.Object, err)
		}
	}
	check("B240012012", "course", "B240012012")
	check("course", "course", "B240012012")
	in.Enrollments = nil
	in.Objects = []c.Object{{ID: "second", SchoolID: "school", Kind: "course", Name: "second", Code: "B240012012"}, {ID: "unknown", SchoolID: "school", Kind: "course", Name: "unknown", Code: "path/invalid"}}
	in.Revision++
	if _, err := s.Import(ctx, admin, "duplicates", in); err != nil {
		t.Fatal(err)
	}
	check("B240012012-2", "second", "B240012012-2")
	obj, err := s.ReadResolution(ctx, user, "unknown")
	if err != nil || !regexp.MustCompile("^course-[1-9][0-9]{5}$").MatchString(obj.Object.AddressID) {
		t.Fatal(obj, err)
	}
	in.Objects = in.Objects[1:]
	in.Objects[0].Code = "NEW001"
	in.Revision++
	if _, err = s.Import(ctx, admin, "valid", in); err != nil {
		t.Fatal(err)
	}
	check(obj.Object.AddressID, "unknown", "NEW001")
	in.Objects[0].Code = "NEW002"
	in.Revision++
	if _, err = s.Import(ctx, admin, "renumber", in); err != nil {
		t.Fatal(err)
	}
	check("NEW001", "unknown", "NEW002")
	if _, err = s.ReadAddress(ctx, user, "teacher", "NEW002"); !errors.Is(err, c.ErrNotFound) {
		t.Fatal(err)
	}
	s.ReadAccess = &readAccess{revoked: true}
	if _, err = s.ReadAddress(ctx, user, "course", "NEW001"); !errors.Is(err, c.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err = s.ReadAddress(ctx, c.Principal{}, "course", "NEW001"); !errors.Is(err, c.ErrUnauthenticated) {
		t.Fatal(err)
	}
}

func TestAddressMigrationConcurrentTeacherReservations(t *testing.T) {
	repo, db := database(t)
	ctx := context.Background()
	// Simulate a pre-migration schema with existing teachers.
	if _, err := db.Exec("DROP TRIGGER reserve_public_address ON catalog.objects; DELETE FROM catalog.address_migration_receipt"); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO catalog.objects(id,school_id,kind,name,department_id,category_id,nature_id,course_id,term_id,term_course_id,class_id,teacher_id,data)
 VALUES($1,'school','teacher','name','','','','','','','','',jsonb_build_object('object',jsonb_build_object('id',$1::text,'school_id','school','kind','teacher','code','staff-code')))`
	if _, err := db.Exec(insert, "legacy"); err != nil {
		t.Fatal(err)
	}
	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var migrated int
	if err := db.QueryRow("SELECT teachers FROM catalog.address_migration_receipt WHERE version=1").Scan(&migrated); err != nil || migrated != 1 {
		t.Fatal(migrated, err)
	}
	var original string
	if err := db.QueryRow("SELECT address FROM catalog.addresses WHERE object_id='legacy'").Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM catalog.objects WHERE id='legacy'"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for n := range 24 {
		wg.Go(func() {
			if _, err := repo.DB.Exec(insert, fmt.Sprintf("teacher-%02d", n)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var total, unique, correct, descents int
	err := db.QueryRow(`SELECT count(*),count(DISTINCT address),count(*) FILTER(WHERE address ~ '^[1-9][0-9]{4}$'),count(*) FILTER(WHERE address<prev)
 FROM (SELECT address,lag(address) OVER(ORDER BY object_id) prev FROM catalog.addresses) a`).Scan(&total, &unique, &correct, &descents)
	if err != nil || total != 25 || unique != 25 || correct != 25 || descents == 0 {
		t.Fatal(total, unique, correct, descents, err)
	}
	if err = postgres.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var kept string
	if err = db.QueryRow("SELECT address FROM catalog.addresses WHERE object_id='legacy'").Scan(&kept); err != nil || kept != original {
		t.Fatal(kept, err)
	}
	var hidden string
	if err = db.QueryRow("SELECT address FROM catalog.addresses WHERE object_id='teacher-00'").Scan(&hidden); err != nil {
		t.Fatal(err)
	}
	s := &c.Service{Repo: repo, ReadAccess: &readAccess{}}
	repo.RequireTeacherDirectory = true
	if _, err = s.ReadAddress(ctx, c.Principal{Subject: "issuer:student"}, "teacher", hidden); !errors.Is(err, c.ErrNotFound) {
		t.Fatal("unmarked teacher", err)
	}
	if _, err = s.ReadAddress(ctx, c.Principal{Subject: "issuer:student"}, "teacher", original); !errors.Is(err, c.ErrNotFound) {
		t.Fatal(err)
	}
}
