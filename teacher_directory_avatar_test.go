package catalog_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"testing"

	c "github.com/StuHelper/stuhelper-catalog"
)

func TestTeacherDirectoryAvatarUsesCurrentPhotoPermissions(t *testing.T) {
	s, _, _, source := homepageFixture(t)
	access := &bodyMediaReadAccess{}
	s.ReadAccess = access
	h := homepageHandler(s)
	const directory = "/api/v1/catalog/directory?kind=teacher&limit=20"
	url := homepagePublic + "/photos/" + source.Sites[0].Photo.ID + "/thumbnail"
	checkList := func(want string) {
		t.Helper()
		page := homepageDecode[struct {
			Items []struct {
				Object c.Object
				Avatar *string `json:"avatar_url"`
			}
		}](t, homepageCall(t, h, "GET", directory, homepageReader, "", "", 200))
		found := false
		for _, item := range page.Items {
			if item.Object.ID == "official" {
				found = true
			}
			if item.Object.ID == "official" && want != "" {
				if item.Avatar == nil || *item.Avatar != want {
					t.Fatal("directory missing current teacher thumbnail")
				}
			} else if item.Avatar != nil {
				t.Fatal("directory disclosed hidden or unrelated avatar")
			}
		}
		if !found {
			t.Fatal("official teacher absent from directory fixture")
		}
	}
	checkList(url)
	thumb := homepageCall(t, h, "GET", url, homepageReader, "", "", 200)
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(thumb.Body.Bytes()))
	if err != nil || cfg.Width != 128 || cfg.Height != 128 || thumb.Body.Len() > 32<<10 || thumb.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatal("invalid thumbnail", cfg, err)
	}
	homepageCall(t, h, "GET", url, "", "", "", 401)
	homepageCall(t, h, "GET", url, "issuer:verified-other", "", "", 403)
	homepageCall(t, h, "GET", directory+"&school_id=other", homepageReader, "", "", 403)
	access.revoked = true
	homepageCall(t, h, "GET", directory, homepageReader, "", "", 403)
	homepageCall(t, h, "HEAD", url, homepageReader, "", "", 403)
	access.revoked = false
	rev := homepageSet(t, h, 0, "visible", `[{"field":{"field":"photo"},"hidden":true}]`, "hide-avatar")
	checkList("")
	homepageCall(t, h, "GET", url, homepageReader, "", "", 404)
	rev = homepageSet(t, h, rev, "visible", "[]", "show-avatar")
	homepageCall(t, h, "PUT", homepageAllowlist, homepageAdmin, "no-photos", `{"expected_revision":0,"fields":[{"field":"name"}]}`, 200)
	checkList("")
	homepageCall(t, h, "GET", url, homepageReader, "", "", 404)
	upload, err := json.Marshal(c.TeacherHomepageFieldPhoto{ContentType: "image/png", Data: homepagePortrait(t)})
	if err != nil {
		t.Fatal(err)
	}
	rev = homepageSet(t, h, rev, "visible", fmt.Sprintf(`[{"field":{"field":"photo"},"hidden":false,"override":{"photo":%s}}]`, upload), "override-avatar")
	profile := homepagePublicProfile(t, h, homepagePublic)
	if profile == nil || profile.Photo == nil {
		t.Fatal("override missing")
	}
	replacement := profile.Photo.URL + "/thumbnail"
	checkList(replacement)
	homepageCall(t, h, "GET", replacement, homepageReader, "", "", 200)
	homepageCall(t, h, "GET", url, homepageReader, "", "", 404)
	homepageSet(t, h, rev, "stopped", "[]", "withdraw-page")
	checkList("")
	homepageCall(t, h, "HEAD", replacement, homepageReader, "", "", 404)
}
