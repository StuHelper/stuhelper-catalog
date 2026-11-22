package catalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/png"
	"strings"
	"testing"
)

func TestTeacherHomepageBodyMediaSnapshotValidation(t *testing.T) {
	site := TeacherSite{URL: "https://shi.buaa.edu.cn/tester/en/index.htm"}
	article := "https://shi.buaa.edu.cn/tester/en/laboratory/821.htm"
	valid := TeacherHomepageBodyMedia{Kind: "image", Field: "sections", Section: "Publications", LinkURL: article}
	hosts := []string{"shi.buaa.edu.cn"}
	for _, tc := range []struct {
		name        string
		version3    bool
		item        TeacherHomepageBodyMedia
		shouldAllow bool
	}{
		{"original article", true, valid, true},
		{"v2 rejects v3 media", false, valid, false},
		{"unrelated teacher", true, TeacherHomepageBodyMedia{Kind: "image", Field: "biography", LinkURL: "https://shi.buaa.edu.cn/other/en/index.htm"}, false},
		{"outside approved school", true, TeacherHomepageBodyMedia{Kind: "image", Field: "biography", LinkURL: "https://publisher.example.org/diagram.png"}, false},
		{"encoded separator", true, TeacherHomepageBodyMedia{Kind: "image", Field: "biography", LinkURL: "https://shi.buaa.edu.cn/tester/en/laboratory%2F821.htm"}, false},
		{"image query", true, TeacherHomepageBodyMedia{Kind: "image", Field: "biography", LinkURL: article + "?u=1"}, false},
		{"image fragment", true, TeacherHomepageBodyMedia{Kind: "image", Field: "biography", LinkURL: article + "#"}, false},
		{"missing section", true, TeacherHomepageBodyMedia{Kind: "image", Field: "sections", LinkURL: article}, false},
		{"misscoped section", true, TeacherHomepageBodyMedia{Kind: "image", Field: "research", Section: "Research", LinkURL: article}, false},
		{"original attachment", true, TeacherHomepageBodyMedia{Kind: "attachment", Field: "research", LinkURL: "https://publisher.example.org/download.pdf?name=%E7%94%A8%E6%88%B7#page=2"}, true},
		{"original video", true, TeacherHomepageBodyMedia{Kind: "video", Field: "research", LinkURL: "http://videos.example.org:80/player"}, true},
		{"control in decoded fragment", true, TeacherHomepageBodyMedia{Kind: "video", Field: "research", LinkURL: "https://videos.example.org/player#%0a"}, false},
		{"browser private address", true, TeacherHomepageBodyMedia{Kind: "attachment", Field: "biography", LinkURL: "http://127.0.0.1/"}, false},
		{"browser internal name", true, TeacherHomepageBodyMedia{Kind: "attachment", Field: "biography", LinkURL: "http://printer.office.local/read"}, false},
		{"embedded URL credentials", true, TeacherHomepageBodyMedia{Kind: "attachment", Field: "biography", LinkURL: "https://user@publisher.example.org/file"}, false},
		{"executable scheme", true, TeacherHomepageBodyMedia{Kind: "attachment", Field: "biography", LinkURL: "javascript:void(0)"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			site.BodyMedia = []TeacherHomepageBodyMedia{tc.item}
			if err := validateTeacherHomepageBodyMedia(site, tc.version3, hosts); (err == nil) != tc.shouldAllow {
				t.Fatalf("%+v returned %v", tc.item, err)
			}
		})
	}
	site.BodyMedia = make([]TeacherHomepageBodyMedia, teacherHomepageBodyMediaLimit)
	for i := range site.BodyMedia {
		site.BodyMedia[i] = valid
	}
	if err := validateTeacherHomepageBodyMedia(site, true, hosts); err != nil {
		t.Fatalf("exact item limit: %v", err)
	}
	site.BodyMedia = append(site.BodyMedia, valid)
	if err := validateTeacherHomepageBodyMedia(site, true, hosts); err == nil {
		t.Fatal("accepted oversized list")
	}
}

func TestTeacherHomepageBodyImageValidatesBytes(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	data := encoded.Bytes()
	digest := sha256.Sum256(data)
	meta := TeacherHomepageBodyImage{ID: hex.EncodeToString(digest[:]), MediaType: "image/png", Width: 4, Height: 3, SizeBytes: int64(len(data))}
	if err := ValidateTeacherHomepageBodyImage(meta, data); err != nil {
		t.Fatalf("valid body image: %v", err)
	}
	atLimit := meta
	atLimit.SizeBytes = TeacherHomepageMaxBodyImageBytes
	if !validTeacherHomepageBodyImage(atLimit) {
		t.Fatal("the exact 20 MiB limit is allowed")
	}
	for _, changed := range []TeacherHomepageBodyImage{
		{ID: strings.Repeat("1", 64), MediaType: "image/png", Width: 4, Height: 3, SizeBytes: meta.SizeBytes},
		{ID: meta.ID, MediaType: "image/jpeg", Width: 4, Height: 3, SizeBytes: meta.SizeBytes},
		{ID: meta.ID, MediaType: "image/png", Width: 3, Height: 4, SizeBytes: meta.SizeBytes},
		{ID: meta.ID, MediaType: "image/png", Width: 4, Height: 3, SizeBytes: TeacherHomepageMaxBodyImageBytes + 1},
		{ID: meta.ID, MediaType: "image/png", Width: 8192, Height: 8192, SizeBytes: meta.SizeBytes},
	} {
		if err := ValidateTeacherHomepageBodyImage(changed, data); err == nil {
			t.Fatalf("verified incorrect claim: %+v", changed)
		}
	}
	cut := data[:30]
	digest = sha256.Sum256(cut)
	meta.ID, meta.SizeBytes = hex.EncodeToString(digest[:]), int64(len(cut))
	if err := ValidateTeacherHomepageBodyImage(meta, cut); err == nil {
		t.Fatal("truncated PNG passed full visual decode")
	}
}
