package postgres

import c "github.com/StuHelper/stuhelper-catalog"

func (t *transaction) Timetable(school, term string) (c.Timetable, error) {
	return read[c.Timetable](t, `SELECT data FROM catalog.timetables WHERE school_id=$1 AND term_id=$2`, school, term)
}
func (t *transaction) SaveTimetable(v c.Timetable) error {
	return t.save(`INSERT INTO catalog.timetables(school_id,term_id,data) VALUES($1,$2,$3) ON CONFLICT(school_id,term_id) DO UPDATE SET data=excluded.data`, v, v.SchoolID, v.TermID)
}

func (t *transaction) SchoolTermOrder(school, term string) (int, error) {
	var order int
	err := t.tx.QueryRowContext(t.ctx, `SELECT sort_key FROM catalog.term_orders WHERE school_id=$1 AND id=catalog.canonical($2)`, school, term).Scan(&order)
	return order, safe(err)
}

func (t *transaction) SchoolTermPeriods(school string, limit int) ([]c.SchoolTermPeriod, error) {
	return list[c.SchoolTermPeriod](t, `SELECT tr.data||jsonb_build_object(
 'week_one_start',COALESCE(tt.data->>'week_one_start',''),'timezone',COALESCE(tt.data->>'timezone',''))
 FROM catalog.term_orders tr JOIN catalog.objects o ON o.id=tr.id AND o.kind='term' AND o.school_id=tr.school_id
 LEFT JOIN catalog.timetables tt ON tt.school_id=tr.school_id AND tt.term_id=tr.id
 WHERE tr.school_id=$1 AND tr.sort_key>0 AND tr.id=catalog.canonical(tr.id)
 AND NOT COALESCE((o.data->'object'->>'inactive')::boolean,false)
 ORDER BY tr.sort_key,tr.id LIMIT $2`, school, limit)
}
