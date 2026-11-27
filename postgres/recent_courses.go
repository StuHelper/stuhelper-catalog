package postgres

import (
	"context"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

func (t *transaction) SaveRecentCourse(owner string, object c.Object, now time.Time) error {
	if err := t.Lock("recent-courses:" + owner); err != nil {
		return err
	}
	id := object.ID
	if object.CanonicalID != "" {
		id = object.CanonicalID
	}
	current, err := t.PublicObject(id)
	if err != nil {
		return err
	}
	if current.Inactive || current.Kind != "course" || current.SchoolID != object.SchoolID {
		return c.ErrForbidden
	}
	if current.CanonicalID != "" {
		id = current.CanonicalID
	}
	// Store only the account partition, course identifier and visit time. There
	// is no body, source address, user agent, session or duplicate event history.
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO catalog.recent_courses(owner,course_id,visited_at) VALUES($1,$2,$3)
 ON CONFLICT(owner,course_id) DO UPDATE SET visited_at=GREATEST(catalog.recent_courses.visited_at,excluded.visited_at)`, owner, id, now)
	if err != nil {
		return safe(err)
	}
	_, err = t.tx.ExecContext(t.ctx, `DELETE FROM catalog.recent_courses WHERE owner=$1 AND
 (visited_at<=$2 OR course_id IN (SELECT course_id FROM catalog.recent_courses WHERE owner=$1 ORDER BY visited_at DESC,course_id LIMIT ALL OFFSET 20)
 OR (course_id<>$3 AND catalog.canonical(course_id)=$3))`, owner, now.Add(-c.RecentCourseRetention), id)
	return safe(err)
}

func (t *transaction) RecentCourses(owner, school string, now time.Time) ([]c.RecentCourse, error) {
	if t.projectionRead {
		return list[c.RecentCourse](t, `SELECT jsonb_build_object('course',o.data || catalog.address_field(o.id),'visited_at',max(r.visited_at),'resource_count',NULL)
 FROM catalog.projection_heads h JOIN catalog.recent_courses r ON r.owner=$1 AND r.visited_at>$3
 LEFT JOIN catalog.projection_aliases a ON a.version=h.version AND a.alias_id=r.course_id
 JOIN catalog.projection_objects o ON o.version=h.version AND o.id=COALESCE(a.canonical_id,r.course_id) AND o.kind='course' AND o.visible
 WHERE h.school_id=$2 GROUP BY o.id,o.data ORDER BY max(r.visited_at) DESC,o.id LIMIT 20`, owner, school, now.Add(-c.RecentCourseRetention))
	}
	return list[c.RecentCourse](t, `WITH recent AS (
 SELECT catalog.canonical(course_id) AS id,max(visited_at) AS visited_at FROM catalog.recent_courses
 WHERE owner=$1 AND visited_at>$3 GROUP BY catalog.canonical(course_id)
 ) SELECT jsonb_build_object('course',(catalog.public_object(o.data->'object') || catalog.address_field(o.id)),'visited_at',r.visited_at,'resource_count',NULL)
 FROM recent r JOIN catalog.objects o ON o.id=r.id AND o.kind='course' AND o.school_id=$2
 WHERE NOT COALESCE((o.data->'object'->>'inactive')::boolean,false)
 ORDER BY r.visited_at DESC,o.id LIMIT 20`, owner, school, now.Add(-c.RecentCourseRetention))
}

func (t *transaction) ClearRecentCourses(owner string) error {
	if err := t.Lock("recent-courses:" + owner); err != nil {
		return err
	}
	_, err := t.tx.ExecContext(t.ctx, `DELETE FROM catalog.recent_courses WHERE owner=$1`, owner)
	return safe(err)
}

func (r *Repository) PruneRecentCourses(ctx context.Context, now time.Time, limit int) (int64, error) {
	if now.IsZero() || limit < 1 || limit > 1000 {
		return 0, c.ErrInput
	}
	result, err := r.DB.ExecContext(ctx, `DELETE FROM catalog.recent_courses WHERE (owner,course_id) IN (
 SELECT owner,course_id FROM catalog.recent_courses WHERE visited_at<=$1 ORDER BY visited_at,owner,course_id LIMIT $2)`, now.Add(-c.RecentCourseRetention), limit)
	if err != nil {
		return 0, safe(err)
	}
	n, err := result.RowsAffected()
	return n, safe(err)
}
