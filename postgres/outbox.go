package postgres

import (
	"context"
	c "github.com/StuHelper/stuhelper-catalog"
)

var _ c.OutboxReader = (*Repository)(nil)

const pendingOutboxSQL = `SELECT data FROM catalog.outbox WHERE sent_at IS NULL AND NOT ((data->>'subject') = ANY(COALESCE($2::text[], ARRAY[]::text[]))) ORDER BY (data->>'source') COLLATE "C", (data->>'subject') COLLATE "C", (data->'data'->>'revision')::bigint, id COLLATE "C" LIMIT $1`

func (r *Repository) PendingOutbox(ctx context.Context, limit int, excludeSubjects []string) ([]c.Envelope, error) {
	if limit < 1 || limit > 100 || len(excludeSubjects) > 1000 {
		return nil, c.ErrInput
	}
	var out []c.Envelope
	err := r.Transaction(ctx, func(tx c.Transaction) error {
		var e error
		out, e = list[c.Envelope](tx.(*transaction), pendingOutboxSQL, limit, excludeSubjects)
		return e
	})
	return out, err
}
func (r *Repository) ConfirmOutbox(ctx context.Context, id string) error {
	if id == "" || len(id) > 200 {
		return c.ErrInput
	}
	result, e := r.DB.ExecContext(ctx, `UPDATE catalog.outbox SET sent_at=COALESCE(sent_at,now()) WHERE id=$1`, id)
	if e != nil {
		return safe(e)
	}
	n, e := result.RowsAffected()
	if e != nil {
		return safe(e)
	}
	if n == 0 {
		return c.ErrNotFound
	}
	return nil
}
func (t *transaction) saveEnvelope(v c.Envelope) error {
	if v.SpecVersion != "1.0" || v.Source != "catalog" || v.ID == "" || len(v.ID) > 200 || v.Subject == "" || v.Type == "" || v.Time.IsZero() || v.Data.Revision < 1 {
		return c.ErrInput
	}
	if e := t.save(`INSERT INTO catalog.outbox(id,data) VALUES($1,$2)`, v, v.ID); e != nil {
		return e
	}
	_, e := t.tx.ExecContext(t.ctx, `SELECT pg_notify($1,'')`, c.OutboxChannel)
	return safe(e)
}
