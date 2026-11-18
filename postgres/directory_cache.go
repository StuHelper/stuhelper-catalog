package postgres

import (
	"context"
	"crypto/sha256"
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
const directoryCacheEntries = 4

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
	mu       sync.Mutex
	entries  map[string]directoryCacheEntry
	building map[string]chan struct{}
	used     uint64
	bytes    int
}

func (r *Repository) directoryCache() *directoryCache {
	r.directoryOnce.Do(func() {
		r.directoryData = &directoryCache{entries: map[string]directoryCacheEntry{}, building: map[string]chan struct{}{}}
	})
	return r.directoryData
}
func (cache *directoryCache) get(ctx context.Context, key string, revision int64, build func() (*directorySelection, int, error)) (*directorySelection, error) {
	if cache == nil {
		value, _, err := build()
		return value, err
	}
	for {
		cache.mu.Lock()
		cache.used++
		if entry, ok := cache.entries[key]; ok && entry.revision == revision && time.Now().Before(entry.expires) {
			entry.used = cache.used
			cache.entries[key] = entry
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
		cache.mu.Unlock()
		value, size, err := build()
		cache.mu.Lock()
		if err == nil && size <= directoryCacheBytes {
			old, exists := cache.entries[key]
			// A slow transaction cannot replace a newer committed generation.
			if !exists || old.revision <= revision {
				if exists {
					cache.bytes -= old.bytes
					delete(cache.entries, key)
				}
				for len(cache.entries) >= directoryCacheEntries || cache.bytes+size > directoryCacheBytes {
					oldestKey := ""
					var oldest uint64
					for k, entry := range cache.entries {
						if oldestKey == "" || entry.used < oldest {
							oldestKey, oldest = k, entry.used
						}
					}
					if oldestKey == "" {
						break
					}
					cache.bytes -= cache.entries[oldestKey].bytes
					delete(cache.entries, oldestKey)
				}
				cache.used++
				cache.entries[key] = directoryCacheEntry{value, revision, size, cache.used, time.Now().Add(5 * time.Minute)}
				cache.bytes += size
			}
		}
		delete(cache.building, key)
		close(ready)
		cache.mu.Unlock()
		return value, err
	}
}
func (t *transaction) directorySelection(q c.DirectoryQuery) (*directorySelection, error) {
	var revision int64
	if t.projectionRead {
		var err error
		revision, err = t.projectionVersion(q.SchoolID)
		if err != nil {
			return nil, err
		}
	} else if err := t.tx.QueryRowContext(t.ctx, `SELECT revision FROM catalog.directory_revision WHERE singleton`).Scan(&revision); err != nil {
		return nil, safe(err)
	}
	input, err := json.Marshal([]any{q.Kind, q.SchoolID, q.Query, q.DepartmentID, q.DepartmentIDs, q.CategoryID, q.CategoryIDs, q.NatureID, q.TeacherID, q.CreditsMin, q.CreditsMax, t.requireTeacherDirectory, t.projectionRead})
	if err != nil {
		return nil, c.ErrInput
	}
	hash := sha256.Sum256(input)
	key := hex.EncodeToString(hash[:])
	return t.directoryData.get(t.ctx, key, revision, func() (*directorySelection, int, error) {
		var raw []byte
		query, args, err := directorySelectionQuery(q)
		if t.projectionRead {
			query, args, err = projectedSelectionQuery(q, revision)
			if q.Query == "" && q.DepartmentID == "" && len(q.DepartmentIDs) == 0 && q.CategoryID == "" && len(q.CategoryIDs) == 0 && q.NatureID == "" && q.TeacherID == "" && q.CreditsMin == nil && q.CreditsMax == nil {
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
