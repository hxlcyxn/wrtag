package wrtag

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.senan.xyz/wrtag/addon"
	"go.senan.xyz/wrtag/musicbrainz"
	"go.senan.xyz/wrtag/tags"
	"golang.org/x/time/rate"
)

const (
	lockTestScenarioEnv = "WRTAG_LOCK_TEST_SCENARIO"
	lockTestTimeout     = time.Minute
)

func TestProcessDirUnlock(t *testing.T) {
	t.Parallel()

	if scenario := os.Getenv(lockTestScenarioEnv); scenario != "" {
		testProcessDirUnlock(t, scenario)
		return
	}

	for _, scenario := range []string{"path", "cancel", "tag", "cover", "addon", "keep", "success", "cleanup", "process-success"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(t.Context(), lockTestTimeout)
			defer cancel()

			binary, err := os.Executable()
			require.NoError(t, err)
			cmd := exec.CommandContext(ctx, binary, "-test.run=^TestProcessDirUnlock$", "-test.count=1")
			cmd.Env = append(os.Environ(), lockTestScenarioEnv+"="+scenario)
			output, err := cmd.CombinedOutput()
			require.NoError(t, ctx.Err(), "import or lock reacquisition blocked: %s", output)
			require.NoError(t, err, "%s", output)
		})
	}
}

func testProcessDirUnlock(t *testing.T, scenario string) {
	t.Helper()

	root := t.TempDir()
	src := filepath.Join(root, "source")
	require.NoError(t, os.Mkdir(src, 0o700))
	data, err := os.ReadFile("cmd/wrtag/testdata/empty.flac")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(src, "track.flac"), data, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(src, "cover.jpg"), []byte("cover"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(src, "notes.txt"), []byte("notes"), 0o600))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	goroutines := runtime.NumGoroutine()

	if scenario == "addon" || scenario == "process-success" {
		testProcessDirBuiltinUnlock(t, ctx, root, src, scenario == "addon")
	} else {
		testImportAlbumUnlock(t, ctx, cancel, src, filepath.Join(root, "library", "album"), scenario)
	}

	// Reacquire synchronously: a leaked lock blocks this bounded subprocess,
	// rather than leaving a blocked waiter behind in the parent test process.
	unlock := lockPaths(src, filepath.Join(root, "library", "album"))
	unlock()
	for deadline := time.Now().Add(time.Second); runtime.NumGoroutine() > goroutines && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	require.LessOrEqual(t, runtime.NumGoroutine(), goroutines, "import leaked goroutines")
}

func testImportAlbumUnlock(t *testing.T, ctx context.Context, cancel context.CancelFunc, src, dest, scenario string) {
	t.Helper()
	files := []albumFile{
		{source: filepath.Join(src, "track.flac"), name: "track.flac"},
		{source: filepath.Join(src, "cover.jpg"), name: "cover.jpg"},
		{source: filepath.Join(src, "notes.txt"), name: "notes.txt", protected: true},
	}
	ops := defaultAlbumIO()
	transfer := ops.transfer
	remove := ops.remove
	injected := errors.New("injected " + scenario + " failure")
	var wantErr error
	var wantMessage string
	failedSource := ""
	switch scenario {
	case "path", "cover", "keep":
		failedSource = map[string]string{"path": "track.flac", "cover": "cover.jpg", "keep": "notes.txt"}[scenario]
		wantErr, wantMessage = injected, "prepare asset"
	case "cancel":
		wantErr = context.Canceled
	case "tag":
		wantMessage = "write tag file"
	case "cleanup":
		wantErr, wantMessage = injected, "source cleanup incomplete"
	case "success":
	default:
		t.Fatalf("unknown scenario %q", scenario)
	}
	transfers := 0
	ops.transfer = func(ctx context.Context, op FileSystemOperation, dc DirContext, source, target string, mode os.FileMode) error {
		transfers++
		if filepath.Base(source) == failedSource {
			return injected
		}
		err := transfer(ctx, op, dc, source, target, mode)
		if scenario == "cancel" {
			cancel()
		}
		return err
	}
	cleanupAttempted := false
	ops.remove = func(p string) error {
		if p == files[0].source {
			cleanupAttempted = true
			if scenario == "cleanup" {
				return injected
			}
		}
		return remove(p)
	}
	prepared := false
	prepare := func(stage string) error {
		prepared = true
		if scenario == "tag" {
			target := filepath.Join(stage, "track.flac")
			if err := os.Remove(target); err != nil {
				return err
			}
			if err := tags.WriteTags(target, map[string][]string{"TITLE": {"tagged"}}, tags.Clear); err != nil {
				wantErr = err
				return fmt.Errorf("write tag file: %w", err)
			}
			return errors.New("tagging unexpectedly succeeded")
		}
		return nil
	}
	err := importAlbum(ctx, NewMove(false), src, dest, files, 0o600, prepare, ops)
	if wantErr != nil {
		require.ErrorIs(t, err, wantErr)
	} else {
		require.NoError(t, err)
	}
	if wantMessage != "" {
		require.ErrorContains(t, err, wantMessage)
	}
	require.Positive(t, transfers)
	published := scenario == "success" || scenario == "cleanup"
	require.Equal(t, published, cleanupAttempted)
	require.Equal(t, published || scenario == "tag", prepared)
	if published {
		for _, f := range files {
			require.FileExists(t, filepath.Join(dest, f.name))
		}
		if scenario == "success" {
			require.NoDirExists(t, src)
		} else {
			require.FileExists(t, files[0].source)
		}
	} else {
		require.NoDirExists(t, dest)
		for _, f := range files {
			require.FileExists(t, f.source)
		}
	}
	paths, err := associatedRecoveryPaths(dest)
	require.NoError(t, err)
	require.Empty(t, paths)
}

func testProcessDirBuiltinUnlock(t *testing.T, ctx context.Context, root, src string, failAddon bool) {
	t.Helper()
	const mbid = "00000000-0000-0000-0000-000000000001"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"id":"%s","title":"album","media":[{"position":1,"track-count":1,"tracks":[{"position":1,"number":"1","title":"track"}]}]}`, mbid)
	}))
	defer server.Close()
	injected := errors.New("addon failed")
	called := false
	cfg := Config{
		MusicBrainzClient: musicbrainz.MBClient{
			BaseURL: server.URL, HTTPClient: server.Client(), Limiter: rate.NewLimiter(rate.Inf, 1),
		},
		FileMode:  0o600,
		KeepFiles: map[string]struct{}{"notes.txt": {}},
		Addons: []addon.Addon{lockTestAddon{process: func(cover string, tracks []string) error {
			called = true
			require.FileExists(t, cover)
			require.Len(t, tracks, 1)
			require.FileExists(t, tracks[0])
			if failAddon {
				return injected
			}
			return nil
		}}},
	}
	require.NoError(t, cfg.PathFormat.Parse(filepath.Join(root, "library", "{{.Release.Title | safepath}}", "{{.Track.Position}} {{.Track.Title | safepath}}{{.Ext}}")))
	result, err := ProcessDir(ctx, &cfg, NewMove(false), src, Always, mbid)
	require.True(t, called)
	dest := filepath.Join(root, "library", "album")
	if failAddon {
		require.ErrorIs(t, err, injected)
		require.ErrorContains(t, err, "process addon")
		require.Nil(t, result)
		require.NoDirExists(t, dest)
		require.FileExists(t, filepath.Join(src, "track.flac"))
	} else {
		require.NoError(t, err)
		require.Equal(t, dest, result.DestDir)
		require.NoDirExists(t, src)
		for _, name := range []string{"1 track.flac", "cover.jpg", "notes.txt"} {
			require.FileExists(t, filepath.Join(dest, name))
		}
	}
	server.Client().CloseIdleConnections()
}

type lockTestAddon struct {
	process func(string, []string) error
}

func (lockTestAddon) Check() error { return nil }

func (a lockTestAddon) ProcessRelease(_ context.Context, cover string, tracks []string) error {
	return a.process(cover, tracks)
}
