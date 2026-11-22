package catalog_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	c "github.com/StuHelper/stuhelper-catalog"
)

type privateImageStore map[c.TeacherImageKey][]byte

func (m privateImageStore) Write(_ context.Context, k c.TeacherImageKey, b []byte, publish func([]byte) error) error {
	m[k] = bytes.Clone(b)
	return publish(nil) // SQL contains only the authorized metadata and references.
}

func (m privateImageStore) Read(_ context.Context, k c.TeacherImageKey, _ func() ([]byte, error)) ([]byte, error) {
	data, ok := m[k]
	if !ok {
		return nil, fmt.Errorf("missing object of kind %s", k.Kind)
	}
	return bytes.Clone(data), nil
}

func TestTeacherImageStorageKeepsAuthorizationAndPrivateNamespaces(t *testing.T) {
	store := privateImageStore{}
	t.Run("body", func(t *testing.T) { checkBodyMediaStorage(t, store) })
	t.Run("source-and-override", func(t *testing.T) { checkOverridePortraitStorage(t, store) })
	t.Run("pending-request", func(t *testing.T) { checkRequestPortraitStorage(t, store) })
	kinds := map[c.TeacherImageKind]bool{}
	for k := range store {
		kinds[k.Kind] = true
		if k.Kind != c.BodyImageObject && (k.School == "" || k.OwnerID == "") {
			t.Fatal("unscoped portrait")
		}
	}
	for _, k := range []c.TeacherImageKind{c.BodyImageObject, c.SitePortrait, c.OverridePortrait, c.RequestPortrait} {
		if !kinds[k] {
			t.Errorf("missing kind %s", k)
		}
	}
}
