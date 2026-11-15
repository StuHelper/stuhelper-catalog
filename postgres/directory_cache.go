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
}
type directorySelection struct {
	VariantCount     int64                `json:"variant_count"`
	Candidates       []directoryCandidate `json:"candidates"`
	FacetTotal       int64                `json:"facet_total"`
	CategoryFacets   []c.DirectoryFacet   `json:"category_facets"`
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
	if err := t.tx.QueryRowContext(t.ctx, `SELECT revision FROM catalog.directory_revision WHERE singleton`).Scan(&revision); err != nil {
		return nil, safe(err)
	}
	input, err := json.Marshal([]any{q.Kind, q.SchoolID, q.Query, q.DepartmentID, q.CategoryID, q.NatureID, q.TeacherID, t.requireTeacherDirectory})
	if err != nil {
		return nil, c.ErrInput
	}
	hash := sha256.Sum256(input)
	key := hex.EncodeToString(hash[:])
	return t.directoryData.get(t.ctx, key, revision, func() (*directorySelection, int, error) {
		query, args, err := directorySelectionQuery(q)
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
