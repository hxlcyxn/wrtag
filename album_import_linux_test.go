package wrtag

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAlbumDownloadedCoverCopiesAcrossFilesystems(t *testing.T) {
	t.Parallel()
	src, dest, _ := albumFixture(t)
	tmp, err := os.MkdirTemp("/dev/shm", "wrtag-cover-test-")
	if err != nil {
		t.Skipf("separate temporary filesystem unavailable: %v", err)
	}
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(tmp)) })
	sourceStat, err := os.Stat(src)
	require.NoError(t, err)
	tmpStat, err := os.Stat(tmp)
	require.NoError(t, err)
	sourceSys, ok := sourceStat.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	tmpSys, ok := tmpStat.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	if sourceSys.Dev == tmpSys.Dev {
		t.Skip("TMPDIR is not a distinct filesystem")
	}
	cover := filepath.Join(tmp, "download.jpg")
	require.NoError(t, os.WriteFile(cover, []byte("downloaded cover"), 0600))
	files := []albumFile{{source: cover, name: "cover.jpg", copyOnly: true}}
	ops := defaultAlbumIO()
	transfer := ops.transfer
	ops.transfer = func(ctx context.Context, op FileSystemOperation, dc DirContext, src, dest string, mode os.FileMode) error {
		require.IsType(t, Copy{}, op)
		return transfer(ctx, op, dc, src, dest, mode)
	}
	require.NoError(t, importAlbum(context.Background(), NewReflink(false), src, dest, files, 0660, func(string) error { return nil }, ops))
	require.FileExists(t, cover)
	require.FileExists(t, filepath.Join(dest, "cover.jpg"))
}

func TestAlbumAddonRenamedAssetGroupFinalized(t *testing.T) {
	t.Parallel()
	src, dest, files := albumFixture(t)
	groups, err := os.Getgroups()
	require.NoError(t, err)
	parent, err := os.Stat(filepath.Dir(dest))
	require.NoError(t, err)
	parentSys, ok := parent.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	parentGID := int(parentSys.Gid)
	foreignGID := -1
	for _, gid := range groups {
		if gid != parentGID {
			foreignGID = gid
			break
		}
	}
	if foreignGID < 0 {
		t.Skip("requires a second supplementary group")
	}
	external := filepath.Join(t.TempDir(), "addon.txt")
	require.NoError(t, os.WriteFile(external, []byte("addon asset"), 0600))
	require.NoError(t, os.Chown(external, -1, foreignGID))
	original, err := os.Stat(external)
	require.NoError(t, err)
	require.NoError(t, importAlbum(context.Background(), NewCopy(false), src, dest, files, 0660, func(stage string) error {
		return os.Rename(external, filepath.Join(stage, "addon.txt"))
	}, defaultAlbumIO()))
	info, err := os.Stat(filepath.Join(dest, "addon.txt"))
	require.NoError(t, err)
	fileSys, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	require.Equal(t, uint32(parentGID), fileSys.Gid)
	require.Equal(t, os.FileMode(0660), info.Mode().Perm())
	require.False(t, os.SameFile(original, info))
	content, err := os.ReadFile(filepath.Join(dest, "addon.txt"))
	require.NoError(t, err)
	require.Equal(t, "addon asset", string(content))
}
