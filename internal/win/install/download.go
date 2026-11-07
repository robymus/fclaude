package install

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// The Ubuntu cloud image. The .vmdk is the variant VMware can import (the .img
// is qcow2). Its hash comes from the SHA256SUMS published beside it.
const (
	ImageBase = "https://cloud-images.ubuntu.com/releases/noble/release/"
	ImageName = "ubuntu-24.04-server-cloudimg-amd64.vmdk"
)

// Fetcher opens a URL for reading (http.Get in production).
type Fetcher func(ctx context.Context, url string) (io.ReadCloser, error)

// HTTPFetch is the production Fetcher.
func HTTPFetch(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp.Body, nil
}

// ErrChecksum means a downloaded or cached file does not match SHA256SUMS.
var ErrChecksum = errors.New("checksum mismatch")

// FetchImage returns the path of a verified copy of the cloud image in dir,
// downloading it if needed. The expected hash is read from SHA256SUMS first; a
// cached file is re-hashed before it is trusted; a download goes to a .part
// file and only becomes the image once its hash matches. progress (may be
// nil) receives the bytes written so far.
func FetchImage(ctx context.Context, fetch Fetcher, dir string, progress func(int64)) (string, error) {
	want, err := expectedHash(ctx, fetch)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(dir, ImageName)
	if got, err := hashFile(dst); err == nil {
		if got == want {
			return dst, nil
		}
		_ = os.Remove(dst) // stale or corrupt cache
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	body, err := fetch(ctx, ImageBase+ImageName)
	if err != nil {
		return "", err
	}
	defer func() { _ = body.Close() }()
	part := dst + ".part"
	f, err := os.Create(part)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	w := io.MultiWriter(f, h, progressWriter(progress))
	_, err = io.Copy(w, body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(part)
		return "", fmt.Errorf("download %s: %w", ImageName, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		_ = os.Remove(part)
		return "", fmt.Errorf("%s: %w (got %s, want %s)", ImageName, ErrChecksum, got, want)
	}
	return dst, os.Rename(part, dst)
}

func expectedHash(ctx context.Context, fetch Fetcher) (string, error) {
	r, err := fetch(ctx, ImageBase+"SHA256SUMS")
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()
	return parseSHA256SUMS(r, ImageName)
}

// parseSHA256SUMS finds name in a SHA256SUMS listing ("<hex> *<name>" or
// "<hex>  <name>").
func parseSHA256SUMS(r io.Reader, name string) (string, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 64 {
			if _, err := hex.DecodeString(f[0]); err == nil {
				return strings.ToLower(f[0]), nil
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("SHA256SUMS has no entry for %s", name)
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type progressFunc func(int64)

func progressWriter(fn func(int64)) io.Writer {
	if fn == nil {
		return io.Discard
	}
	return &counter{fn: fn}
}

type counter struct {
	n  int64
	fn progressFunc
}

func (c *counter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	c.fn(c.n)
	return len(p), nil
}
