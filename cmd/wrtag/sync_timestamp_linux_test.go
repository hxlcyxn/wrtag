package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestSyncTimestampKernelNow(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	old := time.Unix(946684800, 0)
	require.NoError(t, os.Chtimes(dir, old, old))
	require.NoError(t, syncTimestamp(dir))
	var stat unix.Stat_t
	require.NoError(t, unix.Stat(dir, &stat))
	require.Greater(t, stat.Atim.Sec, old.Unix())
	require.Equal(t, stat.Atim, stat.Mtim)
}

func TestSyncTimestampForeignOwner(t *testing.T) {
	t.Parallel()

	root := os.Getenv("WRTAG_SYNC_TIMESTAMP_FIXTURE")
	if root == "" {
		t.Skip("requires foreign-owned writable and non-writable directories in WRTAG_SYNC_TIMESTAMP_FIXTURE")
	}
	require.NotZero(t, os.Geteuid())
	for _, name := range []string{"writable", "non-writable"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := filepath.Join(root, name)
			var before, after unix.Stat_t
			require.NoError(t, unix.Stat(dir, &before))
			require.NotEqual(t, uint32(os.Geteuid()), before.Uid)
			err := syncTimestamp(dir)
			require.NoError(t, unix.Stat(dir, &after))
			if name == "writable" {
				require.NoError(t, unix.Access(dir, unix.W_OK))
				require.NoError(t, err)
				require.NotEqual(t, before.Mtim, after.Mtim)
				require.Equal(t, after.Atim, after.Mtim)
			} else {
				require.Error(t, unix.Access(dir, unix.W_OK))
				require.ErrorIs(t, err, os.ErrPermission)
				require.Equal(t, before.Atim, after.Atim)
				require.Equal(t, before.Mtim, after.Mtim)
			}
		})
	}
}
