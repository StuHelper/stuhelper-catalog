package postgres

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	m "github.com/StuHelper/stuhelper-catalog"
	"time"
)

// Bucket row locks serialize independent actors. Callers update keys in ID order
// when an operation affects multiple buckets, avoiding inverse lock acquisition.
func (t *transaction) changeActivity(kind, target, school string, day time.Time, firstDelta, secondDelta int64, forcedVisibility *bool, at time.Time) error {
	day = day.UTC().Truncate(24 * time.Hour)
	id := m.ActivityID(kind, target, day)
	if _, _, e := m.ParseActivityID(id); e != nil {
		return e
	}
	var first, second, revision int64
	e := t.tx.QueryRowContext(t.ctx, `INSERT INTO catalog.activity_buckets(id,kind,target_id,school_id,day,first_count,second_count,revision,visible) VALUES($1,$2,$3,$4,$5,$6,$7,1,false) ON CONFLICT(id) DO UPDATE SET first_count=catalog.activity_buckets.first_count+EXCLUDED.first_count,second_count=catalog.activity_buckets.second_count+EXCLUDED.second_count,revision=catalog.activity_buckets.revision+1 RETURNING first_count,second_count,revision`, id, kind, target, school, day, firstDelta, secondDelta).Scan(&first, &second, &revision)
	if e != nil {
		return safe(e)
	}
	if first < 0 {
		return m.ErrUnavailable
	}
	if first > 1000000000 || second < 0 || second > 1000000000 {
		return m.ErrUnavailable
	}
	d := m.Activity{Source: "catalog", ID: id, Target: m.ActivityTarget{Source: "catalog", Kind: kind, ID: target}, Day: day.Format(time.DateOnly), Revision: revision, Visible: first > 0 || second > 0, Metrics: map[string]int64{"views": first}}
	if forcedVisibility != nil {
		d.Visible = *forcedVisibility && d.Visible
	}
	data, e := json.Marshal(d)
	if e != nil {
		return m.ErrUnavailable
	}
	if _, e = t.tx.ExecContext(t.ctx, `UPDATE catalog.activity_buckets SET visible=$2,data=$3 WHERE id=$1`, id, d.Visible, data); e != nil {
		return safe(e)
	}
	if _, e = t.tx.ExecContext(t.ctx, `INSERT INTO catalog.activity_history(id,revision,day,data) VALUES($1,$2,$3,$4)`, id, revision, day, data); e != nil {
		return safe(e)
	}
	eventID := fmt.Sprintf("activity:%x:%d", sha256.Sum256([]byte(id)), revision)
	visible := d.Visible
	targetDTO := m.DiscoveryTarget{Source: "catalog", Kind: kind, ID: target}
	return t.saveEnvelope(m.Envelope{SpecVersion: "1.0", ID: eventID, Source: "catalog", Type: "discovery.activity.v1", Subject: id, Time: at, Data: m.ResultData{Source: "catalog", ID: id, Day: d.Day, Revision: revision, Target: &targetDTO, Visible: &visible, Metrics: d.Metrics}})
}
