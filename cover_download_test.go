package wrtag

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.senan.xyz/wrtag/musicbrainz"
	"golang.org/x/time/rate"
)

type coverRoundTripper func(*http.Request) (*http.Response, error)

func (f coverRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type coverBody struct {
	io.Reader
	read   int
	closed bool
}

func (b *coverBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}
func (b *coverBody) Close() error { b.closed = true; return nil }

func TestMaybeFetchUpgradedCoverStreaming(t *testing.T) {
	for _, tc := range []struct {
		name         string
		length       int64
		size         int
		wantDownload bool
		wantRead     int
	}{
		{"exact limit", 8, 8, true, 8},
		{"declared oversize", 9, 9, false, 0},
		{"unknown exact limit", -1, 8, true, 8},
		{"chunked oversize", -1, 100, false, 9},
		{"misleading short length", 2, 100, false, 9},
		{"limit plus one", -1, 9, false, 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			oldCover := filepath.Join(t.TempDir(), "cover.jpg")
			require.NoError(t, os.WriteFile(oldCover, []byte("old"), 0o600))
			body := &coverBody{Reader: strings.NewReader(strings.Repeat("x", tc.size))}
			caa := &musicbrainz.CAAClient{
				BaseURL: "http://cover.test/", Limiter: rate.NewLimiter(rate.Inf, 1),
				HTTPClient: &http.Client{Transport: coverRoundTripper(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path == "/release/id" {
						return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"images":[{"front":true,"image":"http://cover.test/image.jpg"}]}`)), Header: make(http.Header)}, nil
					}
					return &http.Response{StatusCode: http.StatusOK, ContentLength: tc.length, Body: body, Header: make(http.Header)}, nil
				})},
			}
			release := &musicbrainz.Release{ID: "id"}
			release.CoverArtArchive.Front = true
			got, err := maybeFetchUpgradedCover(t.Context(), caa, release, []musicbrainz.CoverSource{musicbrainz.CoverSourceRelease}, oldCover, 8)
			require.NoError(t, err)
			require.Equal(t, tc.wantRead, body.read)
			require.True(t, body.closed)
			old, err := os.ReadFile(oldCover)
			require.NoError(t, err)
			require.Equal(t, "old", string(old))
			entries, err := os.ReadDir(tmp)
			require.NoError(t, err)
			if tc.wantDownload {
				require.Len(t, entries, 1)
				data, err := os.ReadFile(got)
				require.NoError(t, err)
				require.Equal(t, strings.Repeat("x", tc.size), string(data))
			} else {
				require.Empty(t, got)
				require.Empty(t, entries)
			}
		})
	}
}

type failingCoverTemp struct {
	*os.File
	closeErr error
}

func (f failingCoverTemp) Close() error { return errors.Join(f.File.Close(), f.closeErr) }

type interruptedCoverBody struct{}

func (interruptedCoverBody) Read(p []byte) (int, error) {
	return copy(p, "partial"), io.ErrUnexpectedEOF
}

func TestWriteCoverDownloadFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		body     io.Reader
		closeErr error
		wantErr  error
	}{
		{"interrupted body", interruptedCoverBody{}, nil, io.ErrUnexpectedEOF},
		{"close failure", bytes.NewBufferString("cover"), os.ErrPermission, os.ErrPermission},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			f, err := os.CreateTemp(dir, "cover-*")
			require.NoError(t, err)
			got, oversized, err := writeCoverDownload(tc.body, failingCoverTemp{f, tc.closeErr}, 8)
			require.ErrorIs(t, err, tc.wantErr)
			require.False(t, oversized)
			require.Empty(t, got)
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}
