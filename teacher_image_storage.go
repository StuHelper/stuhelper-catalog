package catalog

import "context"

// TeacherImageKey identifies immutable bytes. A body digest is shared across
// schools; official, approved and pending portraits have separate owners.
type TeacherImageKey struct {
	Kind    TeacherImageKind
	School  string
	OwnerID string
	Digest  string
}

type TeacherImageKind string

const (
	BodyImageObject  TeacherImageKind = "body"
	SitePortrait     TeacherImageKind = "site-portrait"
	OverridePortrait TeacherImageKind = "override-portrait"
	RequestPortrait  TeacherImageKind = "request-portrait"
)

// TeacherImageStorage owns bytes, not publication or authorization. Write must
// call publish exactly once after the bytes are durable. The default stores
// them inline in the current SQL transaction. An external adapter publishes
// nil and uses the stable key for retrieval. Writes must be idempotent: the
// referencing transaction can roll back even after Write returns success.
// External garbage collection must reconcile committed references, including
// retained private requests, before removing an object.
type TeacherImageStorage interface {
	Write(ctx context.Context, key TeacherImageKey, data []byte, publish func(inline []byte) error) error
	Read(ctx context.Context, key TeacherImageKey, readInline func() ([]byte, error)) ([]byte, error)
}

// TeacherImageObjects separates the private original from the edge-readable
// copy. Both are content-addressed; retained hidden originals are available
// to moderators and for rollback without exposing a live delivery URL.
type TeacherImageObjects interface {
	TeacherImageStorage
	PublishCopy(context.Context, TeacherImageKey) error
	RemoveObject(ctx context.Context, key TeacherImageKey, original bool) error
}

// TeacherImageIsPublished evaluates the same field and source rules as a
// student-facing read. Maintenance uses it only to revoke old delivery copies.
func TeacherImageIsPublished(t Transaction, teacher Object, k TeacherImageKey) (bool, error) {
	if k.Kind == BodyImageObject {
		media, err := publishedTeacherHomepageMedia(t, teacher, k.Digest)
		return len(media) > 0, err
	}
	profile, err := publishedTeacherHomepage(t, teacher)
	return profile != nil && profile.Photo != nil && profile.Photo.ID == k.Digest, err
}
