package catalog

import (
	"context"
	"time"
)

type SchoolTermPeriod struct {
	TermOrder
	WeekOneStart string `json:"week_one_start"`
	Timezone     string `json:"timezone"`
}

// SchoolTermAt resolves only catalog calendar facts. A term starts at local
// midnight on the first Monday. Undated terms after the last known start do
// not advance the term until another start is known.
func (s *Service) SchoolTermAt(ctx context.Context, school string, at time.Time) (TermOrder, error) {
	if !ValidID(school) || at.IsZero() {
		return TermOrder{}, ErrInput
	}
	const maxTerms = 1000
	var periods []SchoolTermPeriod
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		var err error
		periods, err = t.SchoolTermPeriods(school, maxTerms+1)
		return err
	})
	if err != nil {
		return TermOrder{}, err
	}
	if len(periods) > maxTerms {
		return TermOrder{}, ErrUnavailable
	}
	return schoolTermAt(periods, at)
}

func schoolTermAt(periods []SchoolTermPeriod, at time.Time) (TermOrder, error) {
	chosen := -1
	next := len(periods)
	var previous time.Time
	lastKey := 0
	timezone := ""
	for i, period := range periods {
		key := TermOrderKey(period.Year, period.Season)
		if key <= lastKey || !ValidID(period.ID) {
			return TermOrder{}, ErrUnavailable
		}
		lastKey = key
		if period.WeekOneStart == "" && period.Timezone == "" {
			continue
		}
		loc, err := time.LoadLocation(period.Timezone)
		if err != nil || period.Timezone == "" || period.Timezone == "Local" || timezone != "" && timezone != period.Timezone {
			return TermOrder{}, ErrUnavailable
		}
		timezone = period.Timezone
		start, err := time.ParseInLocation("2006-01-02", period.WeekOneStart, loc)
		if err != nil || start.Weekday() != time.Monday || !start.After(previous) {
			return TermOrder{}, ErrUnavailable
		}
		previous = start
		if !start.After(at) {
			chosen = i
		} else if next == len(periods) {
			next = i
		}
	}
	// A term between two known starts needs its own start to resolve this
	// interval. With no later anchor, the latest confirmed term is the limit.
	if chosen < 0 || (next < len(periods) && next != chosen+1) {
		return TermOrder{}, ErrUnavailable
	}
	return periods[chosen].TermOrder, nil
}
