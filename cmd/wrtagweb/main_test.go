package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.senan.xyz/sqlb"
	"go.senan.xyz/wrtag"
	"go.senan.xyz/wrtag/musicbrainz"
	"go.senan.xyz/wrtag/notifications"
	"go.senan.xyz/wrtag/researchlink"
	"golang.org/x/time/rate"
)

type importTransport func(*http.Request) (*http.Response, error)

func (f importTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProcessJobsLifecycle(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	ctx := context.WithValue(t.Context(), jobLoggerKey{}, slog.New(slog.NewJSONHandler(&logs, nil)))
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "jobs.db"))
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, dbMigrate(ctx, db))
	logs.Reset()
	cfg := &wrtag.Config{FileMode: 0o600}
	require.NoError(t, cfg.PathFormat.Parse(filepath.Join(t.TempDir(), "{{ .Release.Title | safepath }}", "{{ .Track.Position }} {{ .Track.Title | safepath }}{{ .Ext }}")))
	cfg.MusicBrainzClient = musicbrainz.MBClient{
		BaseURL: "https://example.invalid", Limiter: rate.NewLimiter(rate.Inf, 1),
		HTTPClient: &http.Client{Transport: importTransport(func(r *http.Request) (*http.Response, error) {
			code := http.StatusOK
			body := `{"id":"00000000-0000-0000-0000-000000000001","title":"remote album","media":[{"position":1,"tracks":[{"position":1,"number":"1","title":"remote track","length":1000}]}]}`
			if strings.Contains(r.URL.Path, "000000000002") {
				code = http.StatusServiceUnavailable
				body = ""
			}
			return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})},
	}
	seed, err := os.ReadFile("../wrtag/testdata/empty.flac")
	require.NoError(t, err)
	var jobs []Job
	for _, tc := range []struct {
		name    string
		confirm bool
	}{
		{"local-failure", false}, {"provider-failure", false}, {"needs-input", false}, {"success", true},
	} {
		src := t.TempDir()
		if tc.name != "local-failure" {
			require.NoError(t, os.WriteFile(filepath.Join(src, "track.flac"), seed, 0o600))
		}
		var j Job
		mbid := "00000000-0000-0000-0000-000000000001"
		if tc.name == "provider-failure" {
			mbid = "00000000-0000-0000-0000-000000000002"
		}
		require.NoError(t, sqlb.QueryRow(ctx, db, &j, "insert into jobs (source_path, operation, use_mbid, confirm, time) values (?, ?, ?, ?, ?) returning *", src, OperationCopy, mbid, tc.confirm, time.Now()))
		jobs = append(jobs, j)
	}
	var updates []uint64
	require.NoError(t, processJobs(ctx, cfg, &notifications.Notifications{}, &researchlink.Builder{}, "", db, func(id uint64) { updates = append(updates, id) }))
	require.Len(t, updates, 8)
	expected := []JobStatus{StatusError, StatusError, StatusNeedsInput, StatusComplete}
	events := []string{"import failed", "import failed", "import needs input", "import completed"}
	var records []map[string]any
	decoder := json.NewDecoder(&logs)
	for decoder.More() {
		var r map[string]any
		require.NoError(t, decoder.Decode(&r))
		message, ok := r["msg"].(string)
		if ok && strings.HasPrefix(message, "import ") {
			records = append(records, r)
		}
	}
	require.Len(t, records, 8)
	for i, original := range jobs {
		var saved Job
		require.NoError(t, sqlb.QueryRow(ctx, db, &saved, "select * from jobs where id=?", original.ID))
		require.Equal(t, expected[i], saved.Status, saved.Error)
		if saved.Status != StatusComplete {
			require.NotEmpty(t, saved.Error)
		} else {
			require.Empty(t, saved.Error)
			require.Equal(t, saved.DestPath, saved.SourcePath)
			require.Equal(t, OperationMove, saved.Operation)
		}
		start, finish := records[2*i], records[2*i+1]
		require.Equal(t, "import started", start["msg"])
		require.Equal(t, events[i], finish["msg"])
		level := "INFO"
		if saved.Status == StatusError {
			level = "ERROR"
		}
		require.Equal(t, level, finish["level"])
		for _, r := range []map[string]any{start, finish} {
			require.EqualValues(t, original.ID, r["job_id"])
			require.Equal(t, original.SourcePath, r["src"])
			require.Equal(t, OperationCopy, r["operation"])
			require.Contains(t, r, "duration")
			duration, ok := r["duration"].(float64)
			require.True(t, ok)
			require.Positive(t, duration)
		}
		if saved.DestPath != "" {
			require.Equal(t, saved.DestPath, finish["dst"])
		}
		if saved.Error != "" {
			require.Equal(t, saved.Error, finish["err"])
		}
	}
	require.Contains(t, jobsError(t, ctx, db, jobs[1].ID), "503")
}

func jobsError(t *testing.T, ctx context.Context, db *sql.DB, id uint64) string {
	t.Helper()
	var j Job
	require.NoError(t, sqlb.QueryRow(ctx, db, &j, "select * from jobs where id=?", id))
	return j.Error
}

func TestProcessJobConcurrentCorrelation(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	ctx := context.WithValue(t.Context(), jobLoggerKey{}, slog.New(slog.NewJSONHandler(&logs, nil)))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 8 {
		db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "jobs.db"))
		require.NoError(t, err)
		defer db.Close()
		require.NoError(t, dbMigrate(ctx, db))
		var j Job
		require.NoError(t, sqlb.QueryRow(ctx, db, &j, "insert into jobs (id, source_path, operation, time) values (?, ?, ?, ?) returning *", i+1, t.TempDir(), OperationCopy, time.Now()))
		wg.Go(func() {
			<-start
			if err := processJobs(ctx, &wrtag.Config{}, &notifications.Notifications{}, &researchlink.Builder{}, "", db, func(uint64) {}); err != nil {
				t.Error(err)
			}
		})
	}
	close(start)
	wg.Wait()
	records := map[float64][]map[string]any{}
	decoder := json.NewDecoder(&logs)
	for decoder.More() {
		var r map[string]any
		require.NoError(t, decoder.Decode(&r))
		if id, ok := r["job_id"].(float64); ok {
			records[id] = append(records[id], r)
		}
	}
	require.Len(t, records, 8)
	for _, pair := range records {
		require.Len(t, pair, 2)
		require.Equal(t, "import started", pair[0]["msg"])
		require.Equal(t, "import failed", pair[1]["msg"])
		require.Equal(t, pair[0]["src"], pair[1]["src"])
		require.Equal(t, pair[0]["operation"], pair[1]["operation"])
	}
}
