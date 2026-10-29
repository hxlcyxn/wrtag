package wrtag

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.senan.xyz/wrtag/addon"
	"go.senan.xyz/wrtag/musicbrainz"
	"golang.org/x/time/rate"
)

const lockTestScenarioEnv = "WRTAG_LOCK_TEST_SCENARIO"

func TestProcessDirUnlock(t *testing.T) {
	t.Parallel()

	if scenario := os.Getenv(lockTestScenarioEnv); scenario != "" {
		testProcessDirUnlock(t, scenario)
		return
	}

	for _, scenario := range []string{"path", "cancel", "tag", "cover", "addon", "keep", "success", "cleanup"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
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

	const mbid = "00000000-0000-0000-0000-000000000001"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"id":"%s","title":"album","media":[{"position":1,"track-count":1,"tracks":[{"position":1,"number":"1","title":"track"}]}]}`, mbid)
	}))
	defer server.Close()

	cfg := Config{
		MusicBrainzClient: musicbrainz.MBClient{
			BaseURL: server.URL, HTTPClient: server.Client(), Limiter: rate.NewLimiter(rate.Inf, 1),
		},
		FileMode:  0o600,
		KeepFiles: map[string]struct{}{"notes.txt": {}},
	}
	require.NoError(t, cfg.PathFormat.Parse(filepath.Join(root, "library", "{{.Release.Title | safepath}}", "{{.Track.Position}} {{.Track.Title | safepath}}{{.Ext}}")))
	dest := filepath.Join(root, "library", "album")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	op := &lockTestOperation{Move: NewMove(false)}
	var wantErr error
	var wantMessage string
	switch scenario {
	case "path", "cover", "keep":
		op.failSource = map[string]string{"path": "track.flac", "cover": "cover.jpg", "keep": "notes.txt"}[scenario]
		op.pathErr = fs.ErrPermission
		wantErr = fs.ErrPermission
		wantMessage = map[string]string{"path": "process path", "cover": "place cover", "keep": "process keep file"}[scenario]
	case "cancel":
		op.failSource = "track.flac"
		op.pathErr = context.Canceled
		op.cancel = cancel
		wantErr = context.Canceled
		wantMessage = "process path"
	case "tag":
		op.removeDest = true
		wantMessage = "write tag file"
	case "addon":
		wantErr = errors.New("addon failed")
		wantMessage = "process addon"
		cfg.Addons = []addon.Addon{lockTestAddon{err: wantErr}}
	case "cleanup":
		wantErr = errors.New("cleanup failed")
		wantMessage = "clean"
		op.cleanupErr = wantErr
	case "success":
	default:
		t.Fatalf("unknown scenario %q", scenario)
	}

	result, err := ProcessDir(ctx, &cfg, op, src, Always, mbid)
	if wantMessage != "" {
		require.ErrorContains(t, err, wantMessage)
		require.Nil(t, result)
		if wantErr != nil {
			require.ErrorIs(t, err, wantErr)
		}
	} else {
		require.NoError(t, err)
		require.Equal(t, dest, result.DestDir)
	}

	unlock := lockPaths(src, dest)
	unlock()

	wantCleanup := scenario == "success" || scenario == "cleanup"
	require.Equal(t, wantCleanup, op.cleaned)
	if wantCleanup {
		require.NoDirExists(t, src)
		require.FileExists(t, filepath.Join(dest, "1 track.flac"))
		require.FileExists(t, filepath.Join(dest, "cover.jpg"))
		require.FileExists(t, filepath.Join(dest, "notes.txt"))
	}
}

type lockTestOperation struct {
	Move
	failSource string
	pathErr    error
	cancel     context.CancelFunc
	removeDest bool
	cleanupErr error
	cleaned    bool
}

func (op *lockTestOperation) ProcessPath(ctx context.Context, dc DirContext, src, dest string, mode os.FileMode) error {
	if filepath.Base(src) == op.failSource {
		if op.cancel != nil {
			op.cancel()
		}
		return op.pathErr
	}
	if err := op.Move.ProcessPath(ctx, dc, src, dest, mode); err != nil {
		return err
	}
	if op.removeDest {
		return os.Remove(dest)
	}
	return nil
}

func (op *lockTestOperation) PostSource(ctx context.Context, dc DirContext, limit, src string) error {
	op.cleaned = true
	if err := op.Move.PostSource(ctx, dc, limit, src); err != nil {
		return err
	}
	return op.cleanupErr
}

type lockTestAddon struct {
	err error
}

func (lockTestAddon) Check() error { return nil }

func (a lockTestAddon) ProcessRelease(context.Context, string, []string) error { return a.err }
