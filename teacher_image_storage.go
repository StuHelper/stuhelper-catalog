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
