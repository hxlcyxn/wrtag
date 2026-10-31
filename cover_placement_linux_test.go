package wrtag

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlaceDownloadedCoverSetgid(t *testing.T) {
	t.Parallel()
	groups, err := os.Getgroups()
	require.NoError(t, err)
	gid := -1
	for _, group := range groups {
		if group != os.Getgid() {
			gid = group
			break
		}
	}
	if gid == -1 {
		t.Skip("requires a supplementary group different from the primary group")
	}
	for _, upgrade := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial", true: "upgrade"}[upgrade], func(t *testing.T) {
			t.Parallel()
			src, dest := coverPlacementFixture(t)
			require.NoError(t, os.Chown(src, -1, os.Getgid()))
			require.NoError(t, os.Chown(filepath.Dir(dest), -1, gid))
			require.NoError(t, os.Chmod(filepath.Dir(dest), os.ModeSetgid|0o770))
			if !upgrade {
				require.NoError(t, os.Remove(dest))
			}
			require.NoError(t, placeDownloadedCover(src, dest, 0o660, downloadedCoverFileOps()))
			info, err := os.Stat(dest)
			require.NoError(t, err)
			stat, ok := info.Sys().(*syscall.Stat_t)
			require.True(t, ok)
			assert.Equal(t, uint32(gid), stat.Gid)
			assert.Equal(t, os.FileMode(0o660), info.Mode().Perm())
		})
	}
}

func TestPlaceDownloadedCoverAcrossFilesystems(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("/dev/shm", "wrtag-cover-test-*")
	if err != nil {
		t.Skipf("requires writable /dev/shm: %v", err)
	}
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	src := filepath.Join(dir, "download.jpg")
	dest := filepath.Join(t.TempDir(), "album", "cover.jpg")
	var srcStat, destStat syscall.Stat_t
	require.NoError(t, syscall.Stat(dir, &srcStat))
	require.NoError(t, syscall.Stat(filepath.Dir(filepath.Dir(dest)), &destStat))
	if srcStat.Dev == destStat.Dev {
		t.Skip("/dev/shm and destination are on the same filesystem")
	}
	require.NoError(t, os.WriteFile(src, []byte("new cover"), 0o600))
	require.NoError(t, placeDownloadedCover(src, dest, 0o660, downloadedCoverFileOps()))
	assertCoverContents(t, dest, "new cover")
	_, err = os.Stat(src)
	require.ErrorIs(t, err, os.ErrNotExist)
	assertNoCoverStages(t, dest)
}
