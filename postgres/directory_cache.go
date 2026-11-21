package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	c "github.com/StuHelper/stuhelper-catalog"
	"github.com/jackc/pgx/v5"
	"sync"
	"time"
)

// Only catalog-owned membership is cached. Every request checks its transactional
// generation. Ratings, heat, authority decisions and personal data are not cached.
const directoryCacheBytes = 64 << 20
const directoryCacheEntries = 32
const directoryPinnedEntries = 16

type directoryCandidate struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Departments []string `json:"departments"`
	Categories  []string `json:"categories"`
	Natures     []string `json:"natures"`
}
type directorySelection struct {
	NatureFacets     []c.DirectoryFacet   `json:"nature_facets"`
	VariantCount     int64                `json:"variant_count"`
	Candidates       []directoryCandidate `json:"candidates"`
	FacetTotal       int64                `json:"facet_total"`
	CategoryFacets   []c.DirectoryFacet   `json:"category_facets"`
	CategoryMembers  map[string][]string  `json:"category_members"`
	DepartmentFacets []c.DirectoryFacet   `json:"department_facets"`
}
type directoryCacheEntry struct {
	selection *directorySelection
	revision  int64
	bytes     int
	used      uint64
	expires   time.Time
}
type directoryCache struct {
	mu                      sync.Mutex
	pinned                  map[string]directoryCacheEntry
	entries                 map[string]directoryCacheEntry
	building                map[string]chan struct{}
	used                    uint64
	bytes                   int
	hits, misses, evictions uint64
}

func (r *Repository) directoryCache() *directoryCache {
	r.directoryOnce.Do(func() {
		r.directoryData = &directoryCache{
			pinned:   map[string]directoryCacheEntry{},
			entries:  map[string]directoryCacheEntry{},
			building: map[string]chan struct{}{},
		}
	})
	return r.directoryData
}

func (r *Repository) DirectoryCacheMetrics() c.DirectoryCacheMetrics {
	cache := r.directoryCache()
	cache.mu.Lock()
	entries := len(cache.pinned) + len(cache.entries)
	bytes := cache.bytes
	defer cache.mu.Unlock()
	return c.DirectoryCacheMetrics{
		Hits:      cache.hits,
		Misses:    cache.misses,
		Evictions: cache.evictions,
		Entries:   entries,
		Bytes:     bytes,
	}
}

func (r *Repository) OperationalMetrics(ctx context.Context, now time.Time) (c.OperationalMetrics, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	out := c.OperationalMetrics{
		Cache:       r.DirectoryCacheMetrics(),
		Projections: []c.SchoolProjectionMetrics{},
	}
	if err := r.DB.QueryRowContext(ctx, `SELECT count(*) FROM catalog.outbox WHERE sent_at IS NULL`).Scan(&out.PendingOutbox); err != nil {
		return c.OperationalMetrics{}, safe(err)
	}
	rows, err := r.DB.QueryContext(ctx, `SELECT COALESCE(h.school_id,f.school_id) AS school_id,
 COALESCE(h.version,0), g.built_at, COALESCE(f.reason,''), f.failed_at
 FROM catalog.projection_heads h
 LEFT JOIN catalog.projection_generations g ON g.version=h.version
 FULL OUTER JOIN catalog.projection_failures f ON f.school_id=h.school_id
 ORDER BY school_id LIMIT 1000`)
	if err != nil {
		return c.OperationalMetrics{}, safe(err)
	}
	defer rows.Close()
	for rows.Next() {
		var item c.SchoolProjectionMetrics
		var builtAt, failedAt sql.NullTime
		if err := rows.Scan(&item.SchoolID, &item.Version, &builtAt, &item.LastFailureReason, &failedAt); err != nil {
			return c.OperationalMetrics{}, safe(err)
		}
		if builtAt.Valid {
			t := builtAt.Time.UTC()
			item.BuiltAt = &t
			if age := int64(now.Sub(t).Seconds()); age > 0 {
				item.AgeSeconds = age
			}
		}
		if failedAt.Valid {
			t := failedAt.Time.UTC()
			item.LastFailedAt = &t
		}
		out.Projections = append(out.Projections, item)
	}
	if err := rows.Err(); err != nil {
		return c.OperationalMetrics{}, safe(err)
	}
	return out, nil
}

func (cache *directoryCache) evictOldest(pool map[string]directoryCacheEntry) bool {
	oldestKey := ""
	var oldest uint64
	for k, entry := range pool {
		if oldestKey == "" || entry.used < oldest {
			oldestKey, oldest = k, entry.used
		}
	}
	if oldestKey == "" {
		return false
	}
	cache.bytes -= pool[oldestKey].bytes
	delete(pool, oldestKey)
	cache.evictions++
	return true
}

func (cache *directoryCache) get(ctx context.Context, key string, revision int64, pin bool, build func() (*directorySelection, int, error)) (*directorySelection, error) {
	if cache == nil {
		value, _, err := build()
		return value, err
	}
	for {
		cache.mu.Lock()
		cache.used++
		pool := cache.entries
		if pin {
			pool = cache.pinned
		}
		if entry, ok := pool[key]; ok && entry.revision == revision && time.Now().Before(entry.expires) {
			entry.used = cache.used
			pool[key] = entry
			cache.hits++
			cache.mu.Unlock()
			return entry.selection, nil
		}
		if ready, ok := cache.building[key]; ok {
			cache.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-ready:
				continue
			}
		}
		ready := make(chan struct{})
		cache.building[key] = ready
		cache.misses++
		cache.mu.Unlock()
		value, size, err := build()
		cache.mu.Lock()
		if err == nil && size <= directoryCacheBytes {
			pool := cache.entries
			if pin {
				pool = cache.pinned
			}
			old, exists := pool[key]
			// A slow transaction cannot replace a newer committed generation.
			if !exists || old.revision <= revision {
				if exists {
					cache.bytes -= old.bytes
					delete(pool, key)
				}
				if pin {
					for cache.bytes+size > directoryCacheBytes && cache.evictOldest(cache.entries) {
					}
					for len(cache.pinned) >= directoryPinnedEntries || cache.bytes+size > directoryCacheBytes {
						if !cache.evictOldest(cache.pinned) {
							break
						}
					}
					cache.used++
					cache.pinned[key] = directoryCacheEntry{value, revision, size, cache.used, time.Now().Add(5 * time.Minute)}
					cache.bytes += size
				} else {
					for len(cache.entries) >= directoryCacheEntries || cache.bytes+size > directoryCacheBytes {
						if !cache.evictOldest(cache.entries) {
							break
						}
					}
					if len(cache.entries) < directoryCacheEntries && cache.bytes+size <= directoryCacheBytes {
						cache.used++
						cache.entries[key] = directoryCacheEntry{value, revision, size, cache.used, time.Now().Add(5 * time.Minute)}
						cache.bytes += size
					}
				}
			}
		}
		delete(cache.building, key)
		close(ready)
		cache.mu.Unlock()
		return value, err
	}
}
func (t *transaction) directorySelection(q c.DirectoryQuery) (*directorySelection, error) {
	var revision, pendingHomepageRevision int64
	if t.projectionRead {
		var err error
		revision, err = t.projectionVersion(q.SchoolID)
		if err != nil {
			return nil, err
		}
		if q.Kind == "teacher" {
			if err := t.tx.QueryRowContext(t.ctx, `SELECT COALESCE((
 SELECT s.revision FROM catalog.projection_pending p JOIN catalog.teacher_homepage_review_state s USING(school_id)
 WHERE p.school_id=$1),0)`, q.SchoolID).Scan(&pendingHomepageRevision); err != nil {
				return nil, safe(err)
			}
		}
	} else if err := t.tx.QueryRowContext(t.ctx, `SELECT revision FROM catalog.directory_revision WHERE singleton`).Scan(&revision); err != nil {
		return nil, safe(err)
	}
	input, err := json.Marshal([]any{q.Kind, q.SchoolID, q.Query, q.DepartmentID, q.DepartmentIDs, q.CategoryID, q.CategoryIDs, q.NatureID, q.TeacherID, q.CreditsMin, q.CreditsMax, t.requireTeacherDirectory, t.projectionRead, pendingHomepageRevision})
	if err != nil {
		return nil, c.ErrInput
	}
	hash := sha256.Sum256(input)
	key := hex.EncodeToString(hash[:])
	unfiltered := q.Query == "" && q.DepartmentID == "" && len(q.DepartmentIDs) == 0 && q.CategoryID == "" && len(q.CategoryIDs) == 0 && q.NatureID == "" && q.TeacherID == "" && q.CreditsMin == nil && q.CreditsMax == nil
	return t.directoryData.get(t.ctx, key, revision, unfiltered, func() (*directorySelection, int, error) {
		var raw []byte
		query, args, err := directorySelectionQuery(q)
		if t.projectionRead {
			query, args, err = projectedSelectionQuery(q, revision)
			if unfiltered && pendingHomepageRevision == 0 {
				// Older publications stored candidates without their nature memberships.
				// Recompute them from the same immutable version on the first cache miss.
				err = t.tx.QueryRowContext(t.ctx, `SELECT CASE WHEN jsonb_array_length(data->'candidates')=0
 OR (data->'candidates'->0) ? 'natures' THEN data END FROM catalog.projection_selections WHERE version=$1 AND kind=$2`, revision, q.Kind).Scan(&raw)
				if err != nil {
					return nil, 0, safe(err)
				}
			}
		}
		if err != nil {
			return nil, 0, err
		}
		if raw == nil {
			if err = t.tx.QueryRowContext(t.ctx, query, append([]any{pgx.QueryExecModeExec}, args...)...).Scan(&raw); err != nil {
				return nil, 0, safe(err)
			}
		}
		if len(raw) > 32<<20 {
			return nil, 0, c.ErrUnavailable
		}
		var value directorySelection
		if json.Unmarshal(raw, &value) != nil || len(value.Candidates) > 100000 || value.VariantCount > 500000 {
			return nil, 0, c.ErrUnavailable
		}
		return &value, len(raw) * 3, nil
	})
}
