package main

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.senan.xyz/sqlb"
	"go.senan.xyz/wrtag"
	"go.senan.xyz/wrtag/musicbrainz"
	"go.senan.xyz/wrtag/notifications"
	"go.senan.xyz/wrtag/researchlink"
	"go.senan.xyz/wrtag/tags"
	"go.senan.xyz/wrtag/tags/normtag"
	"golang.org/x/time/rate"
)

const processJobReleaseID = "e47d04a4-7460-427d-a731-cc82386d85f1"

func processJobFixture(t *testing.T) (*wrtag.Config, *sql.DB, string) {
	t.Helper()
	response, err := os.ReadFile(filepath.Join("..", "wrtag", "testdata", "responses", "musicbrainz", "ws", "2", "release", processJobReleaseID))
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/release/"+processJobReleaseID {
			t.Errorf("unexpected metadata request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(response)
	}))
	t.Cleanup(server.Close)
	cfg := &wrtag.Config{
		MusicBrainzClient: musicbrainz.MBClient{
			BaseURL: server.URL, HTTPClient: server.Client(), Limiter: rate.NewLimiter(rate.Inf, 1),
		},
		FileMode: 0o640,
	}
	require.NoError(t, cfg.PathFormat.Parse(filepath.Join(t.TempDir(), "library", "{{ .Release.Title | safepath }}", "{{ .Track.Position }}{{ .Ext }}")))
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "jobs.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, dbMigrate(t.Context(), db))
	source := t.TempDir()
	processJobTracks(t, source)
	return cfg, db, source
}

func processJobTracks(t *testing.T, dir string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "tags", "testdata", "empty.flac"))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o750))
	for i := 1; i <= 3; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.flac", i)), data, 0o640))
	}
}

func insertProcessJob(t *testing.T, db *sql.DB, source, operation string) Job {
	t.Helper()
	var job Job
	require.NoError(t, sqlb.QueryRow(t.Context(), db, &job, `insert into jobs
		(status, source_path, operation, time, use_mbid, confirm)
		values (?, ?, ?, ?, ?, ?) returning *`,
		StatusInProgress, source, operation, time.Now(), processJobReleaseID, true))
	return job
}

func runProcessJob(t *testing.T, cfg *wrtag.Config, db *sql.DB, job Job) Job {
	t.Helper()
	var updates []uint64
	require.NoError(t, processJob(t.Context(), cfg, &notifications.Notifications{}, &researchlink.Builder{}, "", db, func(id uint64) {
		updates = append(updates, id)
	}, job))
	require.Equal(t, []uint64{job.ID, job.ID}, updates)
	var stored Job
	require.NoError(t, sqlb.QueryRow(t.Context(), db, &stored, "select * from jobs where id=?", job.ID))
	return stored
}

func requireProcessJobTracks(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 3)
	for i, title := range []string{"Alarms", "The Bells", "The Bells (Festival mix)"} {
		trackTags, err := tags.ReadTags(filepath.Join(dir, fmt.Sprintf("%d.flac", i+1)))
		require.NoError(t, err)
		require.Equal(t, title, normtag.Get(trackTags, normtag.Title))
		require.Equal(t, processJobReleaseID, normtag.Get(trackTags, normtag.MusicBrainzReleaseID))
	}
}

func TestProcessJobExternalImportThenSamePathRetag(t *testing.T) {
	t.Parallel()

	cfg, db, source := processJobFixture(t)
	job := runProcessJob(t, cfg, db, insertProcessJob(t, db, source, OperationCopy))
	dest := filepath.Join(cfg.PathFormat.Root(), "Kat Moda")
	require.Equal(t, StatusComplete, job.Status)
	require.Empty(t, job.Error)
	require.Equal(t, dest, job.DestPath)
	require.Equal(t, dest, job.SourcePath)
	require.Equal(t, OperationMove, job.Operation)
	require.Empty(t, job.UseMBID)
	require.False(t, job.Confirm)
	requireProcessJobTracks(t, dest)
	for i := 1; i <= 3; i++ {
		data, err := os.ReadFile(filepath.Join(source, fmt.Sprintf("%d.flac", i)))
		require.NoError(t, err)
		original, err := os.ReadFile(filepath.Join("..", "..", "tags", "testdata", "empty.flac"))
		require.NoError(t, err)
		require.Equal(t, original, data)
	}

	require.NoError(t, tags.WriteTags(filepath.Join(dest, "1.flac"), map[string][]string{normtag.Title: {"wrong title"}}, 0))
	require.NoError(t, sqlb.QueryRow(t.Context(), db, &job, "update jobs set status=?, confirm=? where id=? returning *", StatusInProgress, true, job.ID))
	job = runProcessJob(t, cfg, db, job)
	require.Equal(t, StatusComplete, job.Status)
	require.Empty(t, job.Error)
	require.Equal(t, dest, job.SourcePath)
	require.Equal(t, dest, job.DestPath)
	require.False(t, job.Confirm)
	requireProcessJobTracks(t, dest)
}

func TestProcessJobExternalReplacementMatchingNames(t *testing.T) {
	t.Parallel()

	cfg, db, source := processJobFixture(t)
	dest := filepath.Join(cfg.PathFormat.Root(), "Kat Moda")
	processJobTracks(t, dest)
	require.NoError(t, tags.WriteTags(filepath.Join(dest, "1.flac"), map[string][]string{normtag.Title: {"old destination"}}, 0))

	job := runProcessJob(t, cfg, db, insertProcessJob(t, db, source, OperationMove))

	require.Equal(t, StatusComplete, job.Status)
	require.Empty(t, job.Error)
	require.Equal(t, dest, job.SourcePath)
	require.Equal(t, dest, job.DestPath)
	require.NoDirExists(t, source)
	requireProcessJobTracks(t, dest)
}

func TestProcessJobUnresolvedRecoveryPreservesSource(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"stage", "backup"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()

			cfg, db, source := processJobFixture(t)
			sum := sha256.Sum256([]byte("kat moda"))
			recovery := filepath.Join(cfg.PathFormat.Root(), fmt.Sprintf(".wrtag-kat moda-%x-%s-test", sum[:8], kind))
			require.NoError(t, os.MkdirAll(recovery, 0o750))
			marker := filepath.Join(recovery, "evidence")
			require.NoError(t, os.WriteFile(marker, []byte("preserve recovery"), 0o640))
			before, err := os.ReadFile(filepath.Join(source, "1.flac"))
			require.NoError(t, err)

			job := runProcessJob(t, cfg, db, insertProcessJob(t, db, source, OperationMove))

			require.Equal(t, StatusError, job.Status)
			require.Contains(t, job.Error, "in-progress or interrupted")
			require.Contains(t, job.Error, recovery)
			require.Equal(t, source, job.SourcePath)
			require.Empty(t, job.DestPath)
			require.Equal(t, processJobReleaseID, job.UseMBID)
			require.False(t, job.Confirm)
			for i := 1; i <= 3; i++ {
				after, err := os.ReadFile(filepath.Join(source, fmt.Sprintf("%d.flac", i)))
				require.NoError(t, err)
				require.Equal(t, before, after)
			}
			data, err := os.ReadFile(marker)
			require.NoError(t, err)
			require.Equal(t, "preserve recovery", string(data))
			require.NoDirExists(t, filepath.Join(cfg.PathFormat.Root(), "Kat Moda"))
		})
	}
}
