// Command teacher-portrait maintains school-scoped source portrait exclusions.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/StuHelper/stuhelper-catalog/postgres"
)

func run() error {
	mode := flag.String("mode", "", "install-policy or exclude")
	school := flag.String("school", "", "Catalog school ID")
	numbers := flag.String("sites", "", "comma-separated numeric source site IDs")
	digest := flag.String("digest", "", "expected SHA-256")
	actor := flag.String("actor", "", "maintenance audit subject")
	flag.Parse()
	dsn := os.Getenv("CATALOG_DSN")
	if dsn == "" || flag.NArg() != 0 {
		return c.ErrInput
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return c.ErrUnavailable
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch *mode {
	case "install-policy":
		return postgres.InstallSitePortraitPolicy(ctx, db)
	case "exclude":
		if !c.ValidID(*actor) {
			return c.ErrInput
		}
		var sites []string
		for _, n := range strings.Split(*numbers, ",") {
			if len(n) == 0 || len(n) > 128 || strings.Trim(n, "0123456789") != "" {
				return c.ErrInput
			}
			sites = append(sites, c.TeacherSiteID(*school, n))
		}
		svc := c.Service{Repo: &postgres.Repository{DB: db}}
		return svc.ExcludeSitePortrait(ctx, c.Principal{Subject: *actor, CanMaintain: true, Schools: []string{*school}}, *school, *digest, sites)
	default:
		return c.ErrInput
	}
}
func main() {
	if err := run(); err != nil {
		code := "database operation failed"
		for _, known := range []error{c.ErrInput, c.ErrForbidden, c.ErrConflict} {
			if errors.Is(err, known) {
				code = known.Error()
			}
		}
		fmt.Fprintln(os.Stderr, code)
		os.Exit(1)
	}
	fmt.Println("ok")
}
