package catalog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"time"
)

type DirectoryConfig struct {
	WindowDays     int     `json:"window_days"`
	ViewWeight     float64 `json:"view_weight"`
	FavoriteWeight float64 `json:"favorite_weight"`
	ReviewWeight   float64 `json:"review_weight"`
}

func DefaultDirectoryConfig() DirectoryConfig { return DirectoryConfig{30, 1, 3, 5} }
func (c DirectoryConfig) Validate() error {
	if c.WindowDays < 1 || c.WindowDays > 366 {
		return ErrInput
	}
	positive := false
	for _, weight := range []float64{c.ViewWeight, c.FavoriteWeight, c.ReviewWeight} {
		if math.IsNaN(weight) || math.IsInf(weight, 0) || weight < 0 || weight > 1000000 {
			return ErrInput
		}
		positive = positive || weight > 0
	}
	if !positive {
		return ErrInput
	}
	return nil
}

type DirectoryBand struct {
	Level   int `json:"level"`
	Samples int `json:"samples"`
	Courses int `json:"courses"`
}
type DirectoryMetric struct {
	ID             string           `json:"id"`
	ReviewCount    int64            `json:"review_count"`
	RecentReviews  int64            `json:"recent_reviews"`
	Favorites      int64            `json:"favorites"`
	Recommendation string           `json:"recommendation"`
	Dimensions     [5]DirectoryBand `json:"dimensions"`
}
type DirectoryMetricProvider interface {
	// Metrics receives inclusive and exclusive boundaries at Beijing midnight.
	Metrics(context.Context, Principal, string, string, time.Time, time.Time) ([]DirectoryMetric, error)
}
type DirectoryItem struct {
	Object         Object           `json:"object"`
	Departments    []Object         `json:"departments"`
	Categories     []Object         `json:"categories"`
	ReviewCount    int64            `json:"review_count"`
	Recommendation string           `json:"recommendation"`
	Dimensions     [5]DirectoryBand `json:"dimensions"`
	Heat           float64          `json:"heat"`
}
type DirectoryFacet struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Count       int64  `json:"count"`
	ReviewCount int64  `json:"review_count"`
}
type DirectoryPage struct {
	Items            []DirectoryItem  `json:"items"`
	Next             string           `json:"next"`
	Total            int64            `json:"total"`
	FacetTotal       int64            `json:"facet_total"`
	CategoryFacets   []DirectoryFacet `json:"category_facets"`
	DepartmentFacets []DirectoryFacet `json:"department_facets"`
	NatureFacets     []DirectoryFacet `json:"nature_facets"`
	GeneratedAt      time.Time        `json:"generated_at"`
	WindowDays       int              `json:"window_days"`
}
type DirectoryPosition struct {
	Version        int64   `json:"version,omitempty"`
	WindowStart    string  `json:"window_start,omitempty"`
	Scope          string  `json:"scope"`
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Heat           float64 `json:"heat"`
	Reviews        int64   `json:"reviews"`
	Recommendation int     `json:"recommendation"`
}
type DirectoryQuery struct {
	Filter
	DepartmentIDs []string
	CategoryIDs   []string
	CreditsMin    *float64
	CreditsMax    *float64
	Sort          string
}
type DirectoryRow struct {
	Item DirectoryItem `json:"item"`
	Rank int           `json:"rank"`
}
type DirectoryResult struct {
	Version          int64            `json:"version"`
	Rows             []DirectoryRow   `json:"rows"`
	Total            int64            `json:"total"`
	FacetTotal       int64            `json:"facet_total"`
	CategoryFacets   []DirectoryFacet `json:"category_facets"`
	DepartmentFacets []DirectoryFacet `json:"department_facets"`
	NatureFacets     []DirectoryFacet `json:"nature_facets"`
}
type DirectoryTransaction interface {
	Directory(DirectoryQuery, DirectoryPosition, []DirectoryMetric, DirectoryConfig, time.Time, time.Time) (DirectoryResult, error)
}

func directoryScope(p Principal, q DirectoryQuery, config DirectoryConfig) string {
	return digest([]any{p.Subject, q.Kind, q.SchoolID, q.DepartmentID, q.DepartmentIDs, q.CategoryID, q.CategoryIDs, q.NatureID, q.TeacherID, q.CreditsMin, q.CreditsMax, q.Query, q.Sort, config})
}

func normalizeDirectoryIDs(single *string, multi *[]string) error {
	if *single != "" && len(*multi) > 0 {
		return ErrInput
	}
	if len(*multi) > 20 {
		return ErrInput
	}
	if len(*multi) == 0 {
		*multi = nil
		return nil
	}
	cleaned := slices.Clone(*multi)
	for _, id := range cleaned {
		if !ValidID(id) {
			return ErrInput
		}
	}
	slices.Sort(cleaned)
	cleaned = slices.Compact(cleaned)
	if len(cleaned) == 1 {
		*single = cleaned[0]
		*multi = nil
		return nil
	}
	*multi = cleaned
	return nil
}

func (s *Service) ReadDirectory(ctx context.Context, p Principal, q DirectoryQuery) (DirectoryPage, error) {
	out := DirectoryPage{Items: []DirectoryItem{}, CategoryFacets: []DirectoryFacet{}, DepartmentFacets: []DirectoryFacet{}, NatureFacets: []DirectoryFacet{}, GeneratedAt: s.now()}
	school, err := s.readScope(ctx, p, q.SchoolID)
	if err != nil {
		return out, err
	}
	if err := publicFilter(q.Filter); err != nil {
		return out, err
	}
	q.SchoolID = school
	q.Query = strings.TrimSpace(q.Query)
	if q.Sort == "" {
		q.Sort = "hot"
	}
	if err := normalizeDirectoryIDs(&q.DepartmentID, &q.DepartmentIDs); err != nil {
		return out, err
	}
	if err := normalizeDirectoryIDs(&q.CategoryID, &q.CategoryIDs); err != nil {
		return out, err
	}
	for _, credit := range []*float64{q.CreditsMin, q.CreditsMax} {
		if credit != nil && (math.IsNaN(*credit) || math.IsInf(*credit, 0) || *credit < 0 || *credit > 100) {
			return out, ErrInput
		}
	}
	if q.CreditsMin != nil && q.CreditsMax != nil && *q.CreditsMin > *q.CreditsMax {
		return out, ErrInput
	}
	if q.Kind != "course" && q.Kind != "teacher" || !slices.Contains([]string{"hot", "recommend", "reviews", "name"}, q.Sort) ||
		q.Limit < 1 || q.Limit > 100 || len(q.After) > 4096 || !validText(q.Query, 200) || q.IncludeInactive || q.IDs != nil ||
		q.CourseID != "" || q.TermID != "" || q.TermCourseID != "" || q.ClassID != "" {
		return out, ErrInput
	}
	for _, id := range []string{q.DepartmentID, q.CategoryID, q.NatureID, q.TeacherID} {
		if id != "" && !ValidID(id) {
			return out, ErrInput
		}
	}
	if q.Kind == "teacher" && (q.CategoryID != "" || len(q.CategoryIDs) > 0 || q.NatureID != "" || q.TeacherID != "" || q.CreditsMin != nil || q.CreditsMax != nil) {
		return out, ErrInput
	}
	if q.TeacherID != "" {
		teacher, err := s.Get(ctx, q.TeacherID)
		if err != nil {
			return out, err
		}
		if teachingDetail(teacher.Kind) || teacher.Inactive {
			return out, ErrNotFound
		}
		if teacher.SchoolID != school {
			return out, ErrForbidden
		}
		if teacher.Kind != "teacher" {
			return out, ErrNotFound
		}
		if teacher.CanonicalID != "" {
			q.TeacherID = teacher.CanonicalID
		}
	}
	config := s.DirectoryConfig
	if config == (DirectoryConfig{}) {
		config = DefaultDirectoryConfig()
	}
	if config.Validate() != nil || s.DirectoryMetrics == nil {
		return out, ErrUnavailable
	}
	from, until := ActivityWindow(out.GeneratedAt, config.WindowDays)
	windowStart := ""
	if q.Sort != "name" {
		windowStart = from.Format(time.DateOnly)
	}
	scope := directoryScope(p, q, config)
	position := DirectoryPosition{}
	if q.After != "" {
		data, err := base64.RawURLEncoding.Strict().DecodeString(q.After)
		if err != nil || json.Unmarshal(data, &position) != nil || position.Scope != scope || !ValidID(position.ID) ||
			!validText(position.Name, 256) || position.Heat < 0 || math.IsNaN(position.Heat) || math.IsInf(position.Heat, 0) || position.Reviews < 0 || position.Recommendation < 0 || position.Recommendation > 4 {
			return out, ErrInput
		}
		if position.WindowStart != windowStart {
			return out, ErrConflict
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	metrics, err := s.DirectoryMetrics.Metrics(callCtx, p, school, q.Kind, from, until)
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return out, ErrUnavailable
		}
		return out, err
	}
	if len(metrics) > 100000 {
		return out, ErrUnavailable
	}
	seen := make(map[string]bool, len(metrics))
	for _, metric := range metrics {
		if !ValidID(metric.ID) || seen[metric.ID] || metric.ReviewCount < 0 || metric.RecentReviews < 0 || metric.Favorites < 0 ||
			!slices.Contains([]string{"unrated", "not_recommended", "average", "recommended", "strongly_recommended"}, metric.Recommendation) {
			return out, ErrUnavailable
		}
		for _, dimension := range metric.Dimensions {
			if dimension.Level < 0 || dimension.Level > 5 || dimension.Samples < 0 || dimension.Courses < 0 {
				return out, ErrUnavailable
			}
		}
		seen[metric.ID] = true
	}
	var result DirectoryResult
	err = s.Repo.Transaction(ctx, func(tx Transaction) error {
		port, ok := tx.(DirectoryTransaction)
		if !ok {
			return ErrUnavailable
		}
		var err error
		result, err = port.Directory(q, position, metrics, config, from, until)
		return err
	})
	if err != nil {
		return out, err
	}
	if len(result.Rows) > q.Limit {
		result.Rows = result.Rows[:q.Limit]
		last := result.Rows[len(result.Rows)-1]
		position = DirectoryPosition{Version: result.Version, WindowStart: windowStart, Scope: scope, ID: last.Item.Object.ID, Name: last.Item.Object.Name, Heat: last.Item.Heat, Reviews: last.Item.ReviewCount, Recommendation: last.Rank}
		encoded, err := json.Marshal(position)
		if err != nil {
			return DirectoryPage{}, ErrUnavailable
		}
		out.Next = base64.RawURLEncoding.EncodeToString(encoded)
	}
	for _, row := range result.Rows {
		out.Items = append(out.Items, row.Item)
	}
	out.Total, out.FacetTotal, out.CategoryFacets, out.DepartmentFacets, out.NatureFacets = result.Total, result.FacetTotal, result.CategoryFacets, result.DepartmentFacets, result.NatureFacets
	out.WindowDays = config.WindowDays
	return out, nil
}
