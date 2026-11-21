package postgres

import (
	c "github.com/StuHelper/stuhelper-catalog"
	"slices"
	"strings"
	"time"
)

type directoryRanked struct {
	candidate *directoryCandidate
	metric    *c.DirectoryMetric
	heat      float64
	rank      int
}

func directoryRankCompare(a, b directoryRanked, order string) int {
	compare := func(a, b float64) int {
		if a > b {
			return -1
		}
		if a < b {
			return 1
		}
		return 0
	}
	var cmp int
	switch order {
	case "name":
		cmp = strings.Compare(a.candidate.Name, b.candidate.Name)
	case "reviews":
		cmp = compare(float64(a.metric.ReviewCount), float64(b.metric.ReviewCount))
		if cmp == 0 {
			cmp = compare(a.heat, b.heat)
		}
	case "recommend":
		cmp = compare(float64(a.rank), float64(b.rank))
		if cmp == 0 {
			cmp = compare(a.heat, b.heat)
		}
	default:
		cmp = compare(a.heat, b.heat)
		if cmp == 0 {
			cmp = compare(float64(a.metric.ReviewCount), float64(b.metric.ReviewCount))
		}
	}
	if cmp != 0 {
		return cmp
	}
	return strings.Compare(a.candidate.ID, b.candidate.ID)
}
func (t *transaction) Directory(q c.DirectoryQuery, cursor c.DirectoryPosition, metrics []c.DirectoryMetric, config c.DirectoryConfig, from, until time.Time) (c.DirectoryResult, error) {
	if (q.Kind == "teacher" || q.TeacherID != "") && !t.requireTeacherDirectory {
		return c.DirectoryResult{}, c.ErrUnavailable
	}
	// Generation, membership and object expansion share one current MVCC snapshot.
	if _, err := t.tx.ExecContext(t.ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY`); err != nil {
		return c.DirectoryResult{}, safe(err)
	}
	if _, err := t.tx.ExecContext(t.ctx, `SET LOCAL jit=off`); err != nil {
		return c.DirectoryResult{}, safe(err)
	}
	selection, err := t.directorySelection(q)
	if err != nil {
		return c.DirectoryResult{}, err
	}
	natureFacets := slices.Clone(selection.NatureFacets)
	if natureFacets == nil {
		natureFacets = []c.DirectoryFacet{}
	}
	out := c.DirectoryResult{Rows: []c.DirectoryRow{}, Total: int64(len(selection.Candidates)), FacetTotal: selection.FacetTotal, CategoryFacets: slices.Clone(selection.CategoryFacets), DepartmentFacets: slices.Clone(selection.DepartmentFacets), NatureFacets: natureFacets}
	if t.projectionRead {
		out.Version, err = t.projectionVersion(q.SchoolID)
		if err != nil {
			return c.DirectoryResult{}, err
		}
		if cursor.ID != "" && cursor.Version != out.Version {
			return c.DirectoryResult{}, c.ErrConflict
		}
	}
	// The provider slice is read-only for this request. Sorting references avoids
	// copying five rating arrays into every map value and every ranked row.
	byID := make(map[string]*c.DirectoryMetric, len(metrics))
	for i := range metrics {
		byID[metrics[i].ID] = &metrics[i]
	}
	views, err := t.directoryViews(q.Kind, q.SchoolID, from, until)
	if err != nil {
		return c.DirectoryResult{}, err
	}
	departments := map[string]int{}
	for i, facet := range out.DepartmentFacets {
		departments[facet.ID] = i
	}
	natures := make(map[string]int, len(out.NatureFacets))
	for i, facet := range out.NatureFacets {
		natures[facet.ID] = i
	}
	hasCategoryFilter := q.CategoryID != "" || len(q.CategoryIDs) > 0
	// Category tabs exclude their own selection. Their cached membership is
	// therefore separate from the filtered page candidates, while counts always
	// use this request's live review metrics.
	categories := make(map[string]int, len(out.CategoryFacets))
	for i, facet := range out.CategoryFacets {
		categories[facet.ID] = i
		if hasCategoryFilter {
			for _, id := range selection.CategoryMembers[facet.ID] {
				if metric := byID[id]; metric != nil {
					out.CategoryFacets[i].ReviewCount += metric.ReviewCount
				}
			}
		}
	}
	ranks := map[string]int{"unrated": 0, "not_recommended": 1, "average": 2, "recommended": 3, "strongly_recommended": 4}
	rows := make([]directoryRanked, 0, len(selection.Candidates))
	unrated := &c.DirectoryMetric{Recommendation: "unrated"}
	for i := range selection.Candidates {
		candidate := &selection.Candidates[i]
		metric := byID[candidate.ID]
		if metric == nil {
			metric = unrated
		}
		for _, department := range candidate.Departments {
			if index, ok := departments[department]; ok {
				out.DepartmentFacets[index].ReviewCount += metric.ReviewCount
			}
		}
		for _, nature := range candidate.Natures {
			if index, ok := natures[nature]; ok {
				out.NatureFacets[index].ReviewCount += metric.ReviewCount
			}
		}
		if !hasCategoryFilter {
			// Without a selected category, these candidates already are the full
			// facet membership. Reuse the metric read needed for ranking.
			for _, category := range candidate.Categories {
				if index, ok := categories[category]; ok {
					out.CategoryFacets[index].ReviewCount += metric.ReviewCount
				}
			}
		}
		heat := float64(views[candidate.ID])*config.ViewWeight + float64(metric.Favorites)*config.FavoriteWeight + float64(metric.RecentReviews)*config.ReviewWeight
		rows = append(rows, directoryRanked{candidate, metric, heat, ranks[metric.Recommendation]})
	}
	slices.SortFunc(rows, func(a, b directoryRanked) int { return directoryRankCompare(a, b, q.Sort) })
	if cursor.ID != "" {
		position := directoryRanked{candidate: &directoryCandidate{ID: cursor.ID, Name: cursor.Name}, metric: &c.DirectoryMetric{ReviewCount: cursor.Reviews}, heat: cursor.Heat, rank: cursor.Recommendation}
		n := 0
		for n < len(rows) && directoryRankCompare(rows[n], position, q.Sort) <= 0 {
			n++
		}
		rows = rows[n:]
	}
	if len(rows) > q.Limit+1 {
		rows = rows[:q.Limit+1]
	}
	ids := []string{}
	for _, row := range rows {
		ids = append(ids, row.candidate.ID)
		ids = append(ids, row.candidate.Departments...)
		ids = append(ids, row.candidate.Categories...)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) > 1000 {
		return c.DirectoryResult{}, c.ErrUnavailable
	}
	objects := map[string]c.Object{}
	if len(ids) > 0 {
		var values []c.Object
		if t.projectionRead {
			// Selection already applied pending teacher revocations in this
			// snapshot. The added department and category IDs are labels.
			values, err = t.projectedLookup(ids, false)
		} else {
			values, err = t.Lookup(ids)
		}
		if err != nil {
			return c.DirectoryResult{}, err
		}
		for _, object := range values {
			if object.SchoolID == q.SchoolID && !object.Inactive {
				objects[object.ID] = object
			}
		}
	}
	for _, row := range rows {
		object, ok := objects[row.candidate.ID]
		if !ok || object.Kind != q.Kind {
			return c.DirectoryResult{}, c.ErrUnavailable
		}
		item := c.DirectoryItem{Object: object, Departments: []c.Object{}, Categories: []c.Object{}, ReviewCount: row.metric.ReviewCount, Recommendation: row.metric.Recommendation, Dimensions: row.metric.Dimensions, Heat: row.heat}
		for _, id := range row.candidate.Departments {
			if ref, ok := objects[id]; ok && ref.Kind == "department" {
				item.Departments = append(item.Departments, ref)
			}
		}
		for _, id := range row.candidate.Categories {
			if ref, ok := objects[id]; ok && ref.Kind == "category" {
				item.Categories = append(item.Categories, ref)
			}
		}
		out.Rows = append(out.Rows, c.DirectoryRow{Item: item, Rank: row.rank})
	}
	return out, nil
}
func (t *transaction) directoryViews(kind, school string, from, until time.Time) (map[string]int64, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT target_id,sum(first_count) FROM catalog.activity_buckets WHERE school_id=$1 AND kind=$2 AND day>=$3 AND day<$4 AND visible GROUP BY target_id LIMIT 100001`, school, kind, from, until)
	if err != nil {
		return nil, safe(err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var count int64
		if err := rows.Scan(&id, &count); err != nil {
			return nil, safe(err)
		}
		if len(out) >= 100000 || count < 0 {
			return nil, c.ErrUnavailable
		}
		out[id] = count
	}
	return out, safe(rows.Err())
}
