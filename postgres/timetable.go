package postgres

import c "github.com/StuHelper/stuhelper-catalog"

func (t *transaction) Timetable(school, term string) (c.Timetable, error) {
	return read[c.Timetable](t, `SELECT data FROM catalog.timetables WHERE school_id=$1 AND term_id=$2`, school, term)
}
func (t *transaction) SaveTimetable(v c.Timetable) error {
	return t.save(`INSERT INTO catalog.timetables(school_id,term_id,data) VALUES($1,$2,$3) ON CONFLICT(school_id,term_id) DO UPDATE SET data=excluded.data`, v, v.SchoolID, v.TermID)
}
