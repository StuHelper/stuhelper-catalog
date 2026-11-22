package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"net/netip"

	c "github.com/StuHelper/stuhelper-catalog"
)

// A fixed database minute is shared by every serving process. Take the global
// counter first so admission and rollback use one lock order across all scopes.
func (r *Repository) TakeTeacherImageSignature(ctx context.Context, user string, ip netip.Addr) error {
	if user == "" || !ip.IsValid() {
		return c.ErrInput
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return safe(err)
	}
	defer tx.Rollback()
	const take = `INSERT INTO catalog.teacher_image_signature_limits AS l (scope, identity_hash, window_start, issued)
 VALUES ($1,$2,date_trunc('minute',transaction_timestamp()),1)
 ON CONFLICT (scope,identity_hash) DO UPDATE SET window_start=EXCLUDED.window_start,
 issued=CASE WHEN l.window_start<EXCLUDED.window_start THEN 1 ELSE l.issued+1 END
 WHERE l.window_start<EXCLUDED.window_start OR (l.window_start=EXCLUDED.window_start AND l.issued<$3) RETURNING issued`
	counters := []struct {
		scope, identity string
		limit           int
	}{{"global", "", 1200}, {"user", user, 60}, {"ip", ip.Unmap().String(), 240}}
	for _, counter := range counters {
		hash := sha256.Sum256([]byte(counter.identity))
		var n int
		err = tx.QueryRowContext(ctx, take, counter.scope, hash[:], counter.limit).Scan(&n)
		if err == sql.ErrNoRows {
			return c.ErrRateLimited
		}
		if err != nil {
			return safe(err)
		}
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM catalog.teacher_image_signature_limits WHERE ctid IN
 (SELECT ctid FROM catalog.teacher_image_signature_limits WHERE window_start < now()-interval '1 minute' ORDER BY window_start LIMIT 8)`)
	if err != nil {
		return safe(err)
	}
	return safe(tx.Commit())
}
