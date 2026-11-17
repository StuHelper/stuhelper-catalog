package postgres

import (
	"encoding/json"
	c "github.com/StuHelper/stuhelper-catalog"
	"time"
)

func (t *transaction) CourseIdentityRecords(ids []string) ([]c.Record, map[string]string, error) {
	rows, err := list[c.Record](t, `SELECT data FROM catalog.objects WHERE id=ANY($1::text[]) ORDER BY id`, ids)
	if err != nil {
		return nil, nil, err
	}
	redirects, err := read[map[string]string](t, `SELECT COALESCE(jsonb_object_agg(alias_id,canonical_id),'{}'::jsonb) FROM catalog.redirects WHERE alias_id=ANY($1::text[]) OR canonical_id=ANY($1::text[])`, ids)
	return rows, redirects, err
}

func (t *transaction) RedirectCourses(pairs []c.CourseIdentityMapping, now time.Time, reason string) error {
	data, err := json.Marshal(pairs)
	if err != nil {
		return c.ErrInput
	}
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO catalog.redirects(alias_id,canonical_id,reason,created_at)
 SELECT source_id,target_id,$2,$3 FROM jsonb_to_recordset($1::jsonb) AS m(source_id text,target_id text)`, data, reason, now)
	return safe(err)
}
