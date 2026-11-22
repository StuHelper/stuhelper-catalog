package teacherimages

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/crc64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	c "github.com/StuHelper/stuhelper-catalog"
	cos "github.com/tencentyun/cos-go-sdk-v5"
)

func TestCOSAdapterContentIntegrity(t *testing.T) {
	ctx := context.Background()
	data := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 32)...)
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	key := c.TeacherImageKey{Kind: c.BodyImageObject, Digest: digest}
	var stored []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/body/private/"+digest {
			t.Error("wrong private path")
			w.WriteHeader(404)
			return
		}
		switch r.Method {
		case "PUT":
			stored, _ = io.ReadAll(r.Body)
			w.Header().Set("x-cos-hash-crc64ecma", strconv.FormatUint(crc64.Checksum(stored, crc64.MakeTable(crc64.ECMA)), 10))
			w.WriteHeader(200)
		case "GET":
			_, _ = w.Write(stored)
		default:
			w.WriteHeader(405)
		}
	}))
	defer server.Close()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{cos.NewClient(&cos.BaseURL{BucketURL: u}, server.Client())}
	n := 0
	publish := func(inline []byte) error {
		if inline != nil {
			t.Fatal("unexpected inline write")
		}
		n++
		return nil
	}
	if err = store.Write(ctx, key, data, publish); err != nil || n != 1 {
		t.Fatal("COS PUT did not publish")
	}
	if err = store.Write(ctx, key, []byte("different"), publish); err == nil || n != 1 {
		t.Fatal("wrong digest published")
	}
	if got, err := store.Read(ctx, key, nil); err != nil || !bytes.Equal(got, data) {
		t.Fatal("private read mismatch")
	}
	stored = []byte("corrupt")
	if _, err = store.Read(ctx, key, nil); err == nil {
		t.Fatal("corrupt read accepted")
	}
}

type acceptedBudget struct{}

func (acceptedBudget) PrepareTeacherImage(context.Context, c.TeacherImageKey) error { return nil }
func (acceptedBudget) TakeTeacherImageSignature(context.Context, string, netip.Addr) error {
	return nil
}

func TestTypeDMatchesEdgeRule(t *testing.T) {
	ctx := context.Background()
	secret := make([]byte, 18)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	key := hex.EncodeToString(secret)
	issue := time.Unix(1700000000, 0)
	signer, err := NewDelivery("media.example.test", key, acceptedBudget{})
	if err != nil {
		t.Fatal(err)
	}
	signer.now = func() time.Time { return issue }
	digest := strings.Repeat("a", 64)
	value, err := signer.Issue(ctx, "member", netip.MustParseAddr("127.0.0.1"), c.TeacherImageKey{Kind: c.BodyImageObject, Digest: digest})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(value)
	if err != nil {
		t.Fatal("invalid redirect")
	}
	expected := fmt.Sprintf("%x", md5.Sum([]byte(key+"/body/"+digest+"1700000000")))
	if u.Host != "media.example.test" || u.Scheme != "https" || u.RawQuery != "token="+expected+"&t=1700000000" {
		t.Fatal("redirect differs from EdgeOne Type D")
	}
}
