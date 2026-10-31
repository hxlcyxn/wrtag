package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func syncRecoveryDir(t *testing.T, root, kind string) string {
	t.Helper()
	sum := sha256.Sum256([]byte("album"))
	name := fmt.Sprintf(".wrtag-album-%x-%s-test", sum[:8], kind)
	path := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(path, 0o750))
	return path
}

func TestRunSyncRecoveryPreflight(t *testing.T) {
	t.Parallel()

	for _, dryRun := range []bool{false, true} {
		for _, kind := range []string{"stage", "backup"} {
			t.Run(kind+map[bool]string{false: "/write", true: "/dry-run"}[dryRun], func(t *testing.T) {
				t.Parallel()

				first := t.TempDir()
				track := filepath.Join(first, "track.flac")
				require.NoError(t, os.WriteFile(track, emptyFLAC, 0o640))
				before, err := os.Stat(track)
				require.NoError(t, err)
				later := t.TempDir()
				recovery := syncRecoveryDir(t, filepath.Join(later, "artist"), kind)
				var stats syncStats

				err = runSync(t.Context(), nil, &stats, []string{first, later}, 0, 0, dryRun, 2)

				require.ErrorContains(t, err, "in-progress or interrupted")
				require.ErrorContains(t, err, recovery)
				require.Zero(t, stats.saw.Load())
				require.Zero(t, stats.processed.Load())
				require.Zero(t, stats.errors.Load())
				data, err := os.ReadFile(track)
				require.NoError(t, err)
				require.Equal(t, emptyFLAC, data)
				after, err := os.Stat(track)
				require.NoError(t, err)
				require.Equal(t, before.Mode(), after.Mode())
				require.Equal(t, before.ModTime(), after.ModTime())
				entries, err := os.ReadDir(first)
				require.NoError(t, err)
				require.Len(t, entries, 1)
				require.DirExists(t, recovery)
			})
		}
	}
}

func TestRunSyncRecoveryPreflightScanError(t *testing.T) {
	t.Parallel()

	var stats syncStats
	err := runSync(t.Context(), nil, &stats, []string{t.TempDir(), filepath.Join(t.TempDir(), "missing")}, 0, 0, false, 2)
	require.ErrorContains(t, err, "preflight sync scope")
	require.Zero(t, stats.saw.Load())
}

func TestRunSyncRecoveryPreflightDoesNotFollowSymlinks(t *testing.T) {
	t.Parallel()

	outside := t.TempDir()
	syncRecoveryDir(t, outside, "backup")
	scope := t.TempDir()
	link := filepath.Join(scope, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	var stats syncStats
	err := runSync(t.Context(), nil, &stats, []string{scope, link}, 0, 24*time.Hour, false, 2)
	require.NoError(t, err)
	require.EqualValues(t, 1, stats.saw.Load())
	require.Zero(t, stats.errors.Load())
	require.Zero(t, stats.processed.Load())
}
