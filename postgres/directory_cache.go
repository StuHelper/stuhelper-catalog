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
const directoryCacheEntries = 32
const directoryPinnedEntries = 16

type directoryCandidate struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Departments []string `json:"departments"`
	Categories  []string `json:"categories"`
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
	pinned   map[string]directoryCacheEntry
	entries  map[string]directoryCacheEntry
	building map[string]chan struct{}
	used     uint64
	bytes    int
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
	input, err := json.Marshal([]any{q.Kind, q.SchoolID, q.Query, q.DepartmentID, q.CategoryID, q.NatureID, q.TeacherID, t.requireTeacherDirectory, t.projectionRead})
	if err != nil {
		return nil, c.ErrInput
	}
	hash := sha256.Sum256(input)
	key := hex.EncodeToString(hash[:])
	unfiltered := q.Query == "" && q.DepartmentID == "" && q.CategoryID == "" && q.NatureID == "" && q.TeacherID == ""
	return t.directoryData.get(t.ctx, key, revision, unfiltered, func() (*directorySelection, int, error) {
		query, args, err := directorySelectionQuery(q)
		if t.projectionRead {
			query, args, err = projectedSelectionQuery(q, revision)
			if unfiltered {
				query = `SELECT data FROM catalog.projection_selections WHERE version=$1 AND kind=$2`
				args = []any{revision, q.Kind}
			}
		}
		if err != nil {
			return nil, 0, err
		}
		var raw []byte
		if err = t.tx.QueryRowContext(t.ctx, query, append([]any{pgx.QueryExecModeExec}, args...)...).Scan(&raw); err != nil {
			return nil, 0, safe(err)
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
