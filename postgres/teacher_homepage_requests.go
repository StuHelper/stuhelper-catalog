package postgres

import (
	"context"
	"encoding/json"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
)

var _ c.TeacherHomepageRequestTransaction = (*transaction)(nil)

// School and account ownership are stored separately so the public DTO cannot
// accidentally serialize the authenticated submitter.
type storedTeacherHomepageRequest struct {
	Request   c.TeacherHomepageRequest `json:"request"`
	SchoolID  string                   `json:"school_id"`
	Submitter string                   `json:"submitter"`
}

func (r storedTeacherHomepageRequest) request() c.TeacherHomepageRequest {
	r.Request.SchoolID = r.SchoolID
	r.Request.Submitter = r.Submitter
	r.Request.CreatedAt = r.Request.CreatedAt.UTC()
	if r.Request.ResolvedAt != nil {
		at := r.Request.ResolvedAt.UTC()
		r.Request.ResolvedAt = &at
	}
	return r.Request
}

const teacherHomepageRequestJSON = `jsonb_build_object(
 'request',r.data||jsonb_build_object('id',r.id,'teacher_id',r.teacher_id,'status',r.status,
 'revision',r.revision,'created_at',r.created_at,'resolved_at',r.resolved_at),
 'school_id',r.school_id,'submitter',r.submitter)`

// Legal retention preserves evidence for authorized governance access without
// extending the ordinary receipt, review queue, or attachment read window.
const readableTeacherHomepageRequest = `(retention_due_at IS NULL OR retention_due_at>(CURRENT_TIMESTAMP AT TIME ZONE 'UTC'))`

func (t *transaction) InsertTeacherHomepageRequest(in c.TeacherHomepageRequest) error {
	data, err := json.Marshal(in)
	if err != nil {
		return c.ErrInput
	}
	// Private requests remain available after a homepage is suppressed or a
	// source disappears. Their target is the directory teacher itself.
	result, err := t.tx.ExecContext(t.ctx, `INSERT INTO catalog.teacher_homepage_requests
 (id,school_id,teacher_id,submitter,status,revision,created_at,data)
 SELECT $1,o.school_id,o.id,$4,'pending',1,$5,$6 FROM catalog.objects o
 WHERE o.school_id=$2 AND o.id=$3 AND o.id=catalog.canonical(o.id) AND o.kind='teacher'
 AND NOT COALESCE((o.data->'object'->>'inactive')::boolean,false)`,
		in.ID, in.SchoolID, in.TeacherID, in.Submitter, in.CreatedAt, string(data))
	if err != nil {
		return safe(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return safe(err)
	}
	if count == 0 {
		return c.ErrNotFound
	}
	return nil
}

func (t *transaction) GetTeacherHomepageRequest(school, id string) (c.TeacherHomepageRequest, error) {
	stored, err := read[storedTeacherHomepageRequest](t, `SELECT `+teacherHomepageRequestJSON+`
 FROM catalog.teacher_homepage_requests r WHERE r.school_id=$1 AND r.id=$2 AND `+readableTeacherHomepageRequest, school, id)
	return stored.request(), err
}

func (t *transaction) ReadTeacherHomepageRequestForOwner(owner, id string) (c.TeacherHomepageRequest, error) {
	stored, err := read[storedTeacherHomepageRequest](t, `SELECT `+teacherHomepageRequestJSON+`
 FROM catalog.teacher_homepage_requests r WHERE r.submitter=$1 AND r.id=$2 AND `+readableTeacherHomepageRequest, owner, id)
	return stored.request(), err
}

// Notification lookups need the storage identity and review outcome only.
const teacherHomepageRequestReferenceSQL = `SELECT id,school_id,teacher_id,submitter,status,revision FROM catalog.teacher_homepage_requests`

func scanTeacherHomepageRequestRef(row interface{ Scan(...any) error }) (c.TeacherHomepageRequestRef, error) {
	var out c.TeacherHomepageRequestRef
	err := row.Scan(&out.ID, &out.SchoolID, &out.TeacherID, &out.Submitter, &out.Status, &out.Revision)
	return out, safe(err)
}

func (t *transaction) FindTeacherHomepageRequest(id string) (c.TeacherHomepageRequestRef, error) {
	return scanTeacherHomepageRequestRef(t.tx.QueryRowContext(t.ctx, teacherHomepageRequestReferenceSQL+
		` WHERE id=$1 AND `+readableTeacherHomepageRequest, id))
}

func (t *transaction) ReadTeacherHomepageRequestsForOwner(owner string, ids []string) ([]c.TeacherHomepageRequestRef, error) {
	if len(ids) > 100 {
		return nil, c.ErrInput
	}
	rows, err := t.tx.QueryContext(t.ctx, teacherHomepageRequestReferenceSQL+`
 WHERE submitter=$1 AND id=ANY($2::text[]) AND `+readableTeacherHomepageRequest+` ORDER BY id`, owner, ids)
	if err != nil {
		return nil, safe(err)
	}
	defer rows.Close()
	out := make([]c.TeacherHomepageRequestRef, 0, len(ids))
	for rows.Next() {
		ref, err := scanTeacherHomepageRequestRef(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, safe(rows.Err())
}

func (t *transaction) TeacherHomepageRequestsSince(submitter string, since time.Time, limit int) (int, error) {
	if submitter == "" || since.IsZero() || limit < 1 || limit > 100 {
		return 0, c.ErrInput
	}
	var count int
	err := t.tx.QueryRowContext(t.ctx, `SELECT count(*) FROM (
 SELECT 1 FROM catalog.teacher_homepage_requests WHERE submitter=$1 AND created_at>$2 LIMIT $3
 ) recent`, submitter, since, limit).Scan(&count)
	return count, safe(err)
}

func (t *transaction) SaveTeacherHomepageRequestPhoto(school, requestID string, photo c.TeacherHomepageImage) error {
	return t.writeTeacherImage(c.TeacherImageKey{Kind: c.RequestPortrait, School: school, OwnerID: requestID, Digest: photo.ID}, photo.Bytes, func(inline []byte) error {
		result, err := t.tx.ExecContext(t.ctx, `INSERT INTO catalog.teacher_homepage_request_photos
 (request_id,photo_id,media_type,width,height,data)
 SELECT r.id,$3,$4,$5,$6,$7 FROM catalog.teacher_homepage_requests r
 WHERE r.school_id=$1 AND r.id=$2 AND r.status='pending'`,
			school, requestID, photo.ID, photo.MediaType, photo.Width, photo.Height, inline)
		if err != nil {
			return safe(err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return safe(err)
		}
		if count == 0 {
			return c.ErrNotFound
		}
		return nil
	})
}

func (t *transaction) ReadTeacherHomepageRequestPhoto(school, requestID string) (c.TeacherHomepageImage, error) {
	var out c.TeacherHomepageImage
	err := t.tx.QueryRowContext(t.ctx, `SELECT p.photo_id,p.media_type,p.width,p.height
 FROM catalog.teacher_homepage_request_photos p JOIN catalog.teacher_homepage_requests r ON r.id=p.request_id
 WHERE r.school_id=$1 AND r.id=$2 AND `+readableTeacherHomepageRequest, school, requestID).
		Scan(&out.ID, &out.MediaType, &out.Width, &out.Height)
	if err != nil {
		return out, safe(err)
	}
	return t.readImage(c.TeacherImageKey{Kind: c.RequestPortrait, School: school, OwnerID: requestID, Digest: out.ID}, out)
}

func (t *transaction) LinkTeacherHomepageRequestReplay(requestID, replayKey string) error {
	_, err := t.tx.ExecContext(t.ctx, `INSERT INTO catalog.teacher_homepage_request_replays(request_id,key)
 VALUES($1,$2)`, requestID, replayKey)
	return safe(err)
}

func teacherHomepageRequests(rows []storedTeacherHomepageRequest) []c.TeacherHomepageRequest {
	items := make([]c.TeacherHomepageRequest, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.request())
	}
	return items
}

func (t *transaction) ListTeacherHomepageRequests(q c.TeacherHomepageRequestQuery) ([]c.TeacherHomepageRequest, int64, bool, error) {
	var afterCreatedAt *time.Time
	if !q.AfterCreatedAt.IsZero() {
		afterCreatedAt = &q.AfterCreatedAt
	}
	// Use stored sizes to choose the page before reading or serializing bodies.
	// A separate response check covers JSON escaping differences in Go.
	result, err := read[struct {
		Items []storedTeacherHomepageRequest `json:"items"`
		Total int64                          `json:"total"`
		More  bool                           `json:"more"`
	}](t, `WITH filtered AS NOT MATERIALIZED (
 SELECT id,created_at,data_bytes FROM catalog.teacher_homepage_requests
 WHERE school_id=$1 AND ($2='' OR teacher_id=$2) AND ($3='' OR submitter=$3) AND ($4='' OR status=$4)
 AND `+readableTeacherHomepageRequest+`
 ), candidates AS (
 SELECT * FROM filtered WHERE ($5::timestamptz IS NULL OR (created_at,id)<($5,$6::text COLLATE "C"))
 ORDER BY created_at DESC,id DESC LIMIT ($7::integer+1)
 ), ranked AS (
 SELECT id,row_number() OVER ordered AS position,sum(data_bytes+1024) OVER ordered AS running_bytes
 FROM candidates WINDOW ordered AS (ORDER BY created_at DESC,id DESC ROWS UNBOUNDED PRECEDING)
 ), page AS (
	SELECT id FROM ranked WHERE position<=$7 AND (running_bytes<=$8 OR position=1)
 ) SELECT jsonb_build_object('total',(SELECT count(*) FROM filtered),
 'more',(SELECT count(*) FROM candidates)>(SELECT count(*) FROM page),
 'items',COALESCE((SELECT jsonb_agg(`+teacherHomepageRequestJSON+` ORDER BY r.created_at DESC,r.id DESC)
 FROM page JOIN catalog.teacher_homepage_requests r USING(id)),'[]'::jsonb))`,
		q.SchoolID, q.TeacherID, q.Submitter, q.Status, afterCreatedAt, q.After, q.Limit, 3<<20)
	if err != nil {
		return nil, 0, false, err
	}
	return teacherHomepageRequests(result.Items), result.Total, result.More, nil
}

func (t *transaction) ResolveTeacherHomepageRequest(in c.TeacherHomepageRequest) error {
	// Keep the original request immutable. Only its pending row at the
	// expected revision can be resolved by the school's write transaction.
	result, err := t.tx.ExecContext(t.ctx, `UPDATE catalog.teacher_homepage_requests
 SET status=$4,revision=$5,resolved_at=$6,
 data=data||jsonb_strip_nulls(jsonb_build_object('resolution_note',NULLIF($7::text,'')))
 WHERE school_id=$1 AND id=$2 AND teacher_id=$3 AND status='pending' AND revision=$5-1`,
		in.SchoolID, in.ID, in.TeacherID, in.Status, in.Revision, in.ResolvedAt, in.ResolutionNote)
	if err != nil {
		return safe(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return safe(err)
	}
	if count == 0 {
		return c.ErrConflict
	}
	return nil
}

// PruneTeacherHomepageRequests uses the isolated maintenance connection. The
// database owns the UTC retention deadline and the bounded deletion batch.
func (r *Repository) PruneTeacherHomepageRequests(ctx context.Context) (int64, error) {
	var removed int64
	err := r.DB.QueryRowContext(ctx, `SELECT catalog.cleanup_teacher_homepage_requests()`).Scan(&removed)
	return removed, safe(err)
}
