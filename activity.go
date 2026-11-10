package catalog

import (
	"context"
	"encoding/base64"
	"strings"
	"time"
	"unicode/utf8"
)

// Activity is one complete provider-owned UTC daily snapshot, including zeros.
type ActivityTarget struct {
	Source string `json:"source"`
	Kind   string `json:"kind"`
	ID     string `json:"id"`
}
type Activity struct {
	Source   string           `json:"source"`
	ID       string           `json:"id"`
	Target   ActivityTarget   `json:"target"`
	Day      string           `json:"day"`
	Revision int64            `json:"revision"`
	Visible  bool             `json:"visible"`
	Metrics  map[string]int64 `json:"metrics"`
}
type ActivityReader interface {
	ActivitySnapshot(context.Context, time.Time, time.Time, string, int) ([]Activity, string, error)
	ReadActivity(context.Context, string) (Activity, error)
}

func ActivityID(kind, id string, day time.Time) string {
	return kind + "/" + base64.RawURLEncoding.EncodeToString([]byte(id)) + "/" + day.UTC().Format(time.DateOnly)
}
func ParseActivityID(id string) (ActivityTarget, time.Time, error) {
	parts := strings.Split(id, "/")
	if len(parts) != 3 || (parts[0] != "course" && parts[0] != "teacher") {
		return ActivityTarget{}, time.Time{}, ErrInput
	}
	raw, e := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if e != nil || len(raw) == 0 || len(raw) > 200 || !utf8.Valid(raw) {
		return ActivityTarget{}, time.Time{}, ErrInput
	}
	day, e := time.Parse(time.DateOnly, parts[2])
	if e != nil || id != ActivityID(parts[0], string(raw), day) {
		return ActivityTarget{}, time.Time{}, ErrInput
	}
	return ActivityTarget{Source: "catalog", Kind: parts[0], ID: string(raw)}, day, nil
}
func ActivityDay(t time.Time) bool {
	_, offset := t.Zone()
	return !t.IsZero() && t.Year() >= 1 && t.Year() <= 9999 && offset == 0 && t.Equal(t.UTC().Truncate(24*time.Hour))
}
