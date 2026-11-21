package catalog

import (
	"time"
	_ "time/tzdata"
)

var activityLocation = func() *time.Location {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		panic(err)
	}
	return location
}()

// ActivityDate returns the Beijing midnight that begins the date containing at.
func ActivityDate(at time.Time) time.Time {
	year, month, day := at.In(activityLocation).Date()
	return time.Date(year, month, day, 0, 0, 0, 0, activityLocation)
}

// ActivityWindow returns the half-open boundaries for days Beijing dates,
// ending immediately before tomorrow. The caller validates that days is positive.
func ActivityWindow(at time.Time, days int) (from, until time.Time) {
	until = ActivityDate(at).AddDate(0, 0, 1)
	return until.AddDate(0, 0, -days), until
}
