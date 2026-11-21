package catalog

import "context"

type TeacherHomepageCounts struct {
	TeacherDirectoryResult
	VerifiedHomepage int `json:"verified_homepage"`
}

type TeacherHomepageCountsTransaction interface {
	TeacherHomepageCounts(school string) (TeacherHomepageCounts, error)
}

func (s *Service) ReadTeacherHomepageCounts(ctx context.Context, p Principal, school string) (TeacherHomepageCounts, error) {
	var out TeacherHomepageCounts
	const action = "homepage-counts-viewed"
	if err := s.teacherRefreshAccess(ctx, p, school, action, p.CanMaintain); err != nil {
		return out, err
	}
	err := s.Repo.Transaction(ctx, func(t Transaction) error {
		port, ok := t.(TeacherHomepageCountsTransaction)
		if !ok {
			return ErrUnavailable
		}
		var err error
		out, err = port.TeacherHomepageCounts(school)
		if err != nil {
			return err
		}
		return s.teacherRefreshAudit(t, p, school, action, "school/"+school, "success")
	})
	return out, err
}
