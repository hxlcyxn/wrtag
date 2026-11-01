package replaygain

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.senan.xyz/wrtag/tags"
	"go.senan.xyz/wrtag/tags/normtag"
)

func TestProcessReleaseCompleteness(t *testing.T) {
	t.Setenv("RSGAIN_FAIL_PATH", "")
	for _, missing := range []string{
		normtag.ReplayGainTrackGain,
		normtag.ReplayGainTrackPeak,
		normtag.ReplayGainAlbumGain,
		normtag.ReplayGainAlbumPeak,
	} {
		t.Run(missing, func(t *testing.T) {
			log := fakeRsgain(t)
			t.Setenv("RSGAIN_LOG", log)
			t.Setenv("PATH", filepath.Dir(log)+string(os.PathListSeparator)+os.Getenv("PATH"))
			paths := releaseFiles(t)
			writeCompleteTags(t, paths[0])
			writeCompleteTags(t, paths[1])
			metadata, err := tags.ReadTags(paths[1])
			require.NoError(t, err)
			delete(metadata, missing)
			require.NoError(t, tags.WriteTags(paths[1], metadata, tags.Clear))

			require.NoError(t, (ReplayGainAddon{}).ProcessRelease(t.Context(), "", paths))
			assertCalculation(t, log, paths, false, 1)
			assertCalculatedTags(t, paths)
		})
	}
}

func TestProcessReleaseComplete(t *testing.T) {
	t.Setenv("RSGAIN_FAIL_PATH", "")
	for _, conf := range []string{"", "force", "force true-peak"} {
		t.Run(conf, func(t *testing.T) {
			log := fakeRsgain(t)
			t.Setenv("RSGAIN_LOG", log)
			t.Setenv("PATH", filepath.Dir(log)+string(os.PathListSeparator)+os.Getenv("PATH"))
			paths := releaseFiles(t)
			for _, path := range paths {
				writeCompleteTags(t, path)
			}
			a, err := NewReplayGainAddon(conf)
			require.NoError(t, err)
			require.NoError(t, a.ProcessRelease(t.Context(), "", paths))
			if conf == "" {
				_, err := os.Stat(log)
				require.True(t, os.IsNotExist(err))
				for _, path := range paths {
					metadata, err := tags.ReadTags(path)
					require.NoError(t, err)
					require.Equal(t, "1.00 dB", normtag.Get(metadata, normtag.ReplayGainTrackGain))
				}
			} else {
				assertCalculation(t, log, paths, a.truePeak, 1)
				assertCalculatedTags(t, paths)
			}
		})
	}
}

func TestProcessReleasePartialWriteRetry(t *testing.T) {
	log := fakeRsgain(t)
	t.Setenv("RSGAIN_LOG", log)
	t.Setenv("PATH", filepath.Dir(log)+string(os.PathListSeparator)+os.Getenv("PATH"))
	paths := releaseFiles(t)
	t.Setenv("RSGAIN_FAIL_PATH", paths[1])
	a := ReplayGainAddon{}
	require.Error(t, a.ProcessRelease(context.Background(), "", paths))
	assertCalculatedTags(t, paths[:1])
	require.NoError(t, os.Remove(paths[1]))
	require.NoError(t, os.Rename(paths[1]+".backup", paths[1]))
	t.Setenv("RSGAIN_FAIL_PATH", "")

	require.NoError(t, a.ProcessRelease(t.Context(), "", paths))
	assertCalculation(t, log, paths, false, 2)
	assertCalculatedTags(t, paths)
	require.NoError(t, a.ProcessRelease(t.Context(), "", paths))
	assertCalculation(t, log, paths, false, 2)
}

func fakeRsgain(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake rsgain requires a POSIX shell")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rsgain"), []byte(`#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$RSGAIN_LOG"
if [ -n "$RSGAIN_FAIL_PATH" ]; then
    mv "$RSGAIN_FAIL_PATH" "$RSGAIN_FAIL_PATH.backup"
    mkdir "$RSGAIN_FAIL_PATH"
fi
printf 'filename\tloudness\tgain\tpeak\tpeakdb\ttype\tadjustment\n'
for path in "$@"; do
    case "$path" in
        *.flac) printf '%s\t-18\t-2.5\t0.75\t0\tsample\t0\n' "$path" ;;
    esac
done
printf 'Album\t-18\t-3.5\t0.9\t0\tsample\t0\n'
`), 0o755))
	return log
}

func releaseFiles(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile("../../tags/testdata/empty.flac")
	require.NoError(t, err)
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "first.flac"), filepath.Join(dir, "later.flac")}
	for _, path := range paths {
		require.NoError(t, os.WriteFile(path, data, 0o600))
	}
	return paths
}

func writeCompleteTags(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, tags.WriteTags(path, map[string][]string{
		normtag.ReplayGainTrackGain: {"1.00 dB"},
		normtag.ReplayGainTrackPeak: {"0.100000"},
		normtag.ReplayGainAlbumGain: {"2.00 dB"},
		normtag.ReplayGainAlbumPeak: {"0.200000"},
	}, tags.Clear))
}

func assertCalculatedTags(t *testing.T, paths []string) {
	t.Helper()
	for _, path := range paths {
		metadata, err := tags.ReadTags(path)
		require.NoError(t, err)
		require.Equal(t, "-2.50 dB", normtag.Get(metadata, normtag.ReplayGainTrackGain))
		require.Equal(t, "0.750000", normtag.Get(metadata, normtag.ReplayGainTrackPeak))
		require.Equal(t, "-3.50 dB", normtag.Get(metadata, normtag.ReplayGainAlbumGain))
		require.Equal(t, "0.900000", normtag.Get(metadata, normtag.ReplayGainAlbumPeak))
	}
}

func assertCalculation(t *testing.T, log string, paths []string, truePeak bool, count int) {
	t.Helper()
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	args := "custom --output --tagmode s "
	if truePeak {
		args += "--true-peak "
	}
	args += "--album " + strings.Join(paths, " ") + "\n"
	require.Equal(t, strings.Repeat(args, count), string(data))
}
