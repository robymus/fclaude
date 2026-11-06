package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeNet struct {
	files map[string]string
	gets  []string
}

func (f *fakeNet) fetch(_ context.Context, url string) (io.ReadCloser, error) {
	f.gets = append(f.gets, strings.TrimPrefix(url, ImageBase))
	b, ok := f.files[strings.TrimPrefix(url, ImageBase)]
	if !ok {
		return nil, errors.New("404 " + url)
	}
	return io.NopCloser(strings.NewReader(b)), nil
}

func sum(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func TestFetchImage(t *testing.T) {
	ctx := context.Background()
	img := strings.Repeat("vmdk", 1000)
	n := &fakeNet{files: map[string]string{
		"SHA256SUMS": sum("other") + " *ubuntu-24.04-server-cloudimg-amd64.img\n" + sum(img) + " *" + ImageName + "\n",
		ImageName:    img,
	}}
	dir := t.TempDir()
	var last int64
	p, err := FetchImage(ctx, n.fetch, dir, func(b int64) { last = b })
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != img || last != int64(len(img)) {
		t.Errorf("content or progress wrong (progress %d)", last)
	}
	// Cached and valid: no second image download.
	n.gets = nil
	if _, err := FetchImage(ctx, n.fetch, dir, nil); err != nil || strings.Join(n.gets, ",") != "SHA256SUMS" {
		t.Errorf("cache not reused: %v %v", n.gets, err)
	}
	// Corrupt cache: replaced.
	_ = os.WriteFile(p, []byte("corrupt"), 0o600)
	if _, err := FetchImage(ctx, n.fetch, dir, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != img {
		t.Error("corrupt cache not replaced")
	}
}

func TestFetchImageRejects(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	n := &fakeNet{files: map[string]string{"SHA256SUMS": sum("expected") + " *" + ImageName + "\n", ImageName: "tampered"}}
	if _, err := FetchImage(ctx, n.fetch, dir, nil); !errors.Is(err, ErrChecksum) {
		t.Errorf("err = %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("unverified data left behind: %v", entries)
	}
	n.files["SHA256SUMS"] = "garbage\n"
	if _, err := FetchImage(ctx, n.fetch, dir, nil); err == nil || !strings.Contains(err.Error(), "no entry") {
		t.Errorf("missing entry: %v", err)
	}
	delete(n.files, "SHA256SUMS")
	if _, err := FetchImage(ctx, n.fetch, dir, nil); err == nil {
		t.Error("missing SHA256SUMS accepted")
	}
	n.files["SHA256SUMS"] = sum("x") + "  " + ImageName + "\n" // two-space form
	delete(n.files, ImageName)
	if _, err := FetchImage(ctx, n.fetch, dir, nil); err == nil {
		t.Error("missing image accepted")
	}
	if _, err := FetchImage(ctx, n.fetch, filepath.Join(dir, "f", string([]byte{0})), nil); err == nil {
		t.Error("bad dir accepted")
	}
}

func TestHTTPFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			_, _ = io.WriteString(w, "hello")
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	ctx := context.Background()
	r, err := HTTPFetch(ctx, srv.URL+"/ok")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	_ = r.Close()
	if string(b) != "hello" {
		t.Errorf("%q", b)
	}
	if _, err := HTTPFetch(ctx, srv.URL+"/missing"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("404: %v", err)
	}
	if _, err := HTTPFetch(ctx, "://bad"); err == nil {
		t.Error("bad URL accepted")
	}
}
