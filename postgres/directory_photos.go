package postgres

import (
	"encoding/json"
	c "github.com/StuHelper/stuhelper-catalog"
)

// Read only this page's live source and control metadata. An independent
// profile can publish a manually approved portrait without a matched site.
func (t *transaction) directoryPhotos(school string, ids []string) (map[string]*c.TeacherHomepagePhoto, error) {
	list, err := t.TeacherHomepageAllowlist(school)
	if err != nil {
		return nil, err
	}
	list = list.Effective()
	rows, err := t.tx.QueryContext(t.ctx, `SELECT o.id,COALESCE(saved.page_visibility,'visible'),
 COALESCE(td.verified_homepage,false),COALESCE(saved.rules,'[]'::jsonb),s.site_id IS NOT NULL,COALESCE(p.photo_id,''),COALESCE(approved.photo_id,'')
 FROM catalog.objects o
 LEFT JOIN catalog.teacher_homepage_controls saved ON saved.school_id=o.school_id AND saved.teacher_id=o.id
 LEFT JOIN catalog.teacher_homepage_override_photos approved ON approved.school_id=o.school_id AND approved.teacher_id=o.id
 LEFT JOIN catalog.teacher_directory td ON td.school_id=o.school_id AND td.teacher_id=o.id
 LEFT JOIN catalog.teacher_homepage_sites s ON s.school_id=td.school_id AND s.site_id=td.homepage_site_id
 AND s.reason<>'site_unavailable' AND `+officialTeacherCondition("td", "o")+`
 LEFT JOIN `+homepagePhotoJoin+`
 WHERE o.school_id=$1 AND o.id=ANY($2) AND o.kind='teacher' AND o.id=catalog.canonical(o.id)
 AND NOT COALESCE((o.data->'object'->>'inactive')::boolean,false)`, school, ids)
	if err != nil {
		return nil, safe(err)
	}
	defer rows.Close()
	photos := make(map[string]*c.TeacherHomepagePhoto, len(ids))
	for rows.Next() {
		var teacher, source, approved string
		var matched bool
		var raw []byte
		var controls c.TeacherHomepageControls
		if err := rows.Scan(&teacher, &controls.PageVisibility, &controls.Independent, &raw, &matched, &source, &approved); err != nil {
			return nil, safe(err)
		}
		if json.Unmarshal(raw, &controls.Rules) != nil {
			return nil, c.ErrUnavailable
		}
		photo := c.DirectoryTeacherAvatar(teacher, controls, list, matched, source)
		if photo != nil && (photo.ID == source || photo.ID == approved) {
			photos[teacher] = photo
		}
	}
	return photos, safe(rows.Err())
}
