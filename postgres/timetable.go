package postgres

import (
	"context"
	"database/sql"

	c "github.com/StuHelper/stuhelper-catalog"
)

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
 WHERE tr.school_id=$1 AND tr.sort_key>=$3 AND tr.id=catalog.canonical(tr.id)
 AND NOT COALESCE((o.data->'object'->>'inactive')::boolean,false)
 ORDER BY tr.sort_key,tr.id LIMIT $2`, school, limit, c.FirstTermOrderKey())
}

// PruneTimetablesBeforeStart is an offline operation for the catalog migration
// identity. A nil expected count performs a read-only inventory.
func (r *Repository) PruneTimetablesBeforeStart(ctx context.Context, school string, expected *int) (out []string, err error) {
	if !c.ValidID(school) || expected != nil && *expected < 0 {
		return nil, c.ErrInput
	}
	tx, err := r.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: expected == nil})
	if err != nil {
		return nil, safe(err)
	}
	defer tx.Rollback()
	t := &transaction{ctx: ctx, tx: tx}
	if expected != nil {
		if err := t.Lock(school); err != nil {
			return nil, err
		}
	}
	const where = `tt.school_id=$1 AND tr.school_id=tt.school_id
 AND tr.id=catalog.canonical(tt.term_id) AND tr.sort_key>0 AND tr.sort_key<$2`
	out, err = list[string](t, `SELECT to_jsonb(tt.term_id) FROM catalog.timetables tt,
 catalog.term_orders tr WHERE `+where+` ORDER BY tt.term_id`, school, c.FirstTermOrderKey())
	if err != nil {
		return nil, err
	}
	if expected != nil {
		if len(out) != *expected {
			return nil, c.ErrConflict
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM catalog.timetables tt
 USING catalog.term_orders tr WHERE `+where, school, c.FirstTermOrderKey())
		if err != nil {
			return nil, safe(err)
		}
		deleted, err := result.RowsAffected()
		if err != nil || deleted != int64(len(out)) {
			return nil, c.ErrConflict
		}
	}
	return out, safe(tx.Commit())
}
