package main

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.senan.xyz/sqlb"
)

func TestMarkInterruptedJobs(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "jobs.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, dbMigrate(ctx, db))

	statuses := []JobStatus{StatusEnqueued, StatusInProgress, StatusNeedsInput, StatusError, StatusComplete, StatusInProgress}
	var before []Job
	for _, status := range statuses {
		var job Job
		err := sqlb.QueryRow(ctx, db, &job, `insert into jobs
			(status, error, operation, time, updated_time, use_mbid, source_path, dest_path, search_result, research_links, confirm)
			values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) returning *`,
			status, "previous error", OperationMove, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			time.Date(2026, 1, 2, 4, 5, 6, 0, time.UTC), "release-id", "/source", "/destination", "{}", "[]", true)
		require.NoError(t, err)
		before = append(before, job)
	}

	for range 2 {
		require.NoError(t, markInterruptedJobs(ctx, db))
		var after []Job
		require.NoError(t, sqlb.QueryRows(ctx, db, sqlb.Append(&after), "select * from jobs order by id"))
		require.Len(t, after, len(before))
		for i, want := range before {
			if want.Status == StatusInProgress {
				want.Status = StatusError
				want.Error = "Operation interrupted by service restart. Inspect source, destination and any recovery paths before explicitly retrying."
			}
			require.Equal(t, want, after[i])
		}
	}

	var queued []Job
	require.NoError(t, sqlb.QueryRows(ctx, db, sqlb.Append(&queued), "select * from jobs where status=? order by id", StatusEnqueued))
	require.Equal(t, []Job{before[0]}, queued)
}

func TestMarkInterruptedJobsDatabaseError(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "jobs.db"))
	require.NoError(t, err)
	require.NoError(t, db.Close())
	require.Error(t, markInterruptedJobs(t.Context(), db))
}
