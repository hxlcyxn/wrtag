package wrtag

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func coverPlacementFixture(t *testing.T) (string, string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "download.JPG")
	dest := filepath.Join(t.TempDir(), "cover.jpg")
	require.NoError(t, os.WriteFile(src, []byte("new cover"), 0o600))
	require.NoError(t, os.WriteFile(dest, []byte("old cover"), 0o600))
	return src, dest
}

func assertCoverContents(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, want, string(data))
}

func assertNoCoverStages(t *testing.T, dest string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(dest), ".wrtag-cover-stage-*"))
	require.NoError(t, err)
	assert.Empty(t, paths)
}

func TestPlaceDownloadedCover(t *testing.T) {
	t.Parallel()
	for _, upgrade := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial", true: "upgrade"}[upgrade], func(t *testing.T) {
			t.Parallel()
			src, dest := coverPlacementFixture(t)
			if !upgrade {
				require.NoError(t, os.Remove(dest))
			}
			ops := downloadedCoverFileOps()
			closed := false
			ops.close = func(f *os.File) error {
				closed = true
				return f.Close()
			}
			ops.rename = func(from, to string) error {
				assert.Equal(t, filepath.Dir(dest), filepath.Dir(from))
				assert.True(t, closed)
				info, err := os.Stat(from)
				require.NoError(t, err)
				assert.Equal(t, os.FileMode(0o660), info.Mode().Perm())
				assertCoverContents(t, from, "new cover")
				if upgrade {
					assertCoverContents(t, dest, "old cover")
				}
				return os.Rename(from, to)
			}
			require.NoError(t, placeDownloadedCover(src, dest, 0o660, ops))
			assertCoverContents(t, dest, "new cover")
			_, err := os.Stat(src)
			require.ErrorIs(t, err, os.ErrNotExist)
			assertNoCoverStages(t, dest)
		})
	}
}

func TestPlaceDownloadedCoverFailures(t *testing.T) {
	t.Parallel()
	injected := errors.New("injected failure")
	for _, point := range []string{"copy", "chmod", "close", "publish"} {
		t.Run(point, func(t *testing.T) {
			t.Parallel()
			src, dest := coverPlacementFixture(t)
			ops := downloadedCoverFileOps()
			switch point {
			case "copy":
				ops.copy = func(w io.Writer, _ io.Reader) (int64, error) {
					_, err := w.Write([]byte("partial"))
					require.NoError(t, err)
					return 7, injected
				}
			case "chmod":
				ops.chmod = func(*os.File, os.FileMode) error { return injected }
			case "close":
				ops.close = func(f *os.File) error {
					require.NoError(t, f.Close())
					return injected
				}
			case "publish":
				ops.rename = func(string, string) error { return injected }
			}
			require.ErrorIs(t, placeDownloadedCover(src, dest, 0o660, ops), injected)
			assertCoverContents(t, dest, "old cover")
			_, err := os.Stat(src)
			require.ErrorIs(t, err, os.ErrNotExist)
			assertNoCoverStages(t, dest)
		})
	}
}

func TestPlaceDownloadedCoverCleanupFailures(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"download", "stage"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			src, dest := coverPlacementFixture(t)
			ops := downloadedCoverFileOps()
			cleanupErr := errors.New("cleanup denied")
			publishErr := errors.New("publication denied")
			if target == "stage" {
				ops.rename = func(string, string) error { return publishErr }
			}
			ops.remove = func(path string) error {
				if (target == "download") == (path == src) {
					return cleanupErr
				}
				return os.Remove(path)
			}
			err := placeDownloadedCover(src, dest, 0o660, ops)
			require.ErrorIs(t, err, cleanupErr)
			if target == "stage" {
				require.ErrorIs(t, err, publishErr)
				assertCoverContents(t, dest, "old cover")
			} else {
				assertCoverContents(t, dest, "new cover")
			}
		})
	}
}

func TestPlaceDownloadedCoverDestinationFailure(t *testing.T) {
	t.Parallel()
	src, dest := coverPlacementFixture(t)
	err := placeDownloadedCover(src, filepath.Join(dest, "cover.jpg"), 0o660, downloadedCoverFileOps())
	require.Error(t, err)
	assertCoverContents(t, dest, "old cover")
	_, err = os.Stat(src)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestProcessCoverDownloadedDryRun(t *testing.T) {
	t.Parallel()
	src, dest := coverPlacementFixture(t)
	dc := NewDirContext()
	got, err := processCover(context.Background(), NewMove(true), dc, filepath.Dir(dest), dest, src, 0o660)
	require.NoError(t, err)
	assert.Equal(t, dest, got)
	assert.Contains(t, dc.knownDestPaths, dest)
	assertCoverContents(t, dest, "old cover")
	assertCoverContents(t, src, "new cover")
	assertNoCoverStages(t, dest)
}

func TestProcessCoverDownloaded(t *testing.T) {
	t.Parallel()
	src, dest := coverPlacementFixture(t)
	dc := NewDirContext()
	got, err := processCover(context.Background(), NewCopy(false), dc, filepath.Dir(dest), dest, src, 0o660)
	require.NoError(t, err)
	assert.Equal(t, dest, got)
	assert.Contains(t, dc.knownDestPaths, dest)
	assertCoverContents(t, dest, "new cover")
	_, err = os.Stat(src)
	require.ErrorIs(t, err, os.ErrNotExist)
}
