package jobs

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
)

func inMarks(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// ListRecentDone returns up to limit recently finished Jobs for the given kinds.
func (q *Queue) ListRecentDone(ctx context.Context, kinds []string, limit int) ([]Job, error) {
	return RecentDone(ctx, q.s.Read(), kinds, limit)
}

// RecentDone returns up to limit recently completed Jobs (state = 'done') for kinds.
func RecentDone(ctx context.Context, db *sql.DB, kinds []string, limit int) ([]Job, error) {
	if len(kinds) == 0 || limit <= 0 {
		return []Job{}, nil
	}
	args := make([]any, 0, len(kinds)+1)
	for _, k := range kinds {
		args = append(args, k)
	}
	args = append(args, limit)
	rows, err := database.Select[Job](ctx, db, "jobs",
		"kind IN ("+inMarks(len(kinds))+") AND state = 'done' ORDER BY COALESCE(ended_at, updated_at) DESC, id DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []Job{}
	}
	return rows, nil
}

// CountByState returns the count of Jobs per state for the given kinds and states.
func CountByState(ctx context.Context, db *sql.DB, kinds []string, states ...string) (map[string]int, error) {
	out := map[string]int{}
	if len(kinds) == 0 || len(states) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(kinds)+len(states))
	for _, k := range kinds {
		args = append(args, k)
	}
	for _, s := range states {
		args = append(args, s)
	}
	rows, err := db.QueryContext(ctx,
		"SELECT state, count(*) FROM jobs WHERE kind IN ("+inMarks(len(kinds))+") AND state IN ("+inMarks(len(states))+") GROUP BY state",
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		out[state] = n
	}
	return out, rows.Err()
}

// CountByKind returns the count of Jobs per kind for the given kinds and state.
func CountByKind(ctx context.Context, db *sql.DB, kinds []string, state string) (map[string]int, error) {
	out := map[string]int{}
	if len(kinds) == 0 || state == "" {
		return out, nil
	}
	args := make([]any, 0, len(kinds)+1)
	for _, k := range kinds {
		args = append(args, k)
	}
	args = append(args, state)
	rows, err := db.QueryContext(ctx,
		"SELECT kind, count(*) FROM jobs WHERE kind IN ("+inMarks(len(kinds))+") AND state = ? GROUP BY kind",
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, err
		}
		out[kind] = n
	}
	return out, rows.Err()
}

// ListByStates returns a page of Jobs matching kinds and states, newest first, and the total count.
func ListByStates(ctx context.Context, db *sql.DB, kinds, states []string, limit, offset int) ([]Job, int, error) {
	if len(kinds) == 0 || len(states) == 0 {
		return []Job{}, 0, nil
	}
	args := make([]any, 0, len(kinds)+len(states)+2)
	for _, k := range kinds {
		args = append(args, k)
	}
	for _, s := range states {
		args = append(args, s)
	}
	where := "kind IN (" + inMarks(len(kinds)) + ") AND state IN (" + inMarks(len(states)) + ")"
	var total int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM jobs WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	pageArgs := append(append([]any{}, args...), limit, offset)
	rows, err := database.Select[Job](ctx, db, "jobs",
		where+" ORDER BY COALESCE(ended_at, updated_at) DESC, id DESC LIMIT ? OFFSET ?", pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	if rows == nil {
		rows = []Job{}
	}
	return rows, total, nil
}

// ListActive returns running and queued Jobs for kinds, running first.
func ListActive(ctx context.Context, db *sql.DB, kinds []string, limit int) ([]Job, error) {
	if len(kinds) == 0 || limit <= 0 {
		return []Job{}, nil
	}
	args := make([]any, 0, len(kinds)+1)
	for _, k := range kinds {
		args = append(args, k)
	}
	args = append(args, limit)
	rows, err := database.Select[Job](ctx, db, "jobs",
		"kind IN ("+inMarks(len(kinds))+") AND state IN ('running', 'queued') ORDER BY CASE state WHEN 'running' THEN 0 ELSE 1 END, CASE priority WHEN 'now' THEN 0 WHEN 'normal' THEN 1 ELSE 2 END, run_after, id LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []Job{}
	}
	return rows, nil
}

// ListQueued returns queued Jobs for kinds in execution order and the total count.
func ListQueued(ctx context.Context, db *sql.DB, kinds []string, limit, offset int) ([]Job, int, error) {
	if len(kinds) == 0 {
		return []Job{}, 0, nil
	}
	if offset < 0 {
		offset = 0
	}
	args := make([]any, 0, len(kinds)+2)
	for _, k := range kinds {
		args = append(args, k)
	}
	where := "kind IN (" + inMarks(len(kinds)) + ") AND state = 'queued'"
	var total int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM jobs WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		return []Job{}, total, nil
	}
	pageArgs := append(append([]any{}, args...), limit, offset)
	rows, err := database.Select[Job](ctx, db, "jobs",
		where+" ORDER BY CASE priority WHEN 'now' THEN 0 WHEN 'normal' THEN 1 ELSE 2 END, run_after ASC, id ASC LIMIT ? OFFSET ?", pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	if rows == nil {
		rows = []Job{}
	}
	return rows, total, nil
}

// ListRunning returns currently running Jobs for kinds, oldest started first.
func ListRunning(ctx context.Context, db *sql.DB, kinds []string, limit int) ([]Job, error) {
	if len(kinds) == 0 || limit <= 0 {
		return []Job{}, nil
	}
	args := make([]any, 0, len(kinds)+1)
	for _, k := range kinds {
		args = append(args, k)
	}
	args = append(args, limit)
	rows, err := database.Select[Job](ctx, db, "jobs",
		"kind IN ("+inMarks(len(kinds))+") AND state = 'running' ORDER BY started_at ASC, id ASC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []Job{}
	}
	return rows, nil
}

// JobByDedupeKey returns the newest live or recent Job ID matching key.
func JobByDedupeKey(ctx context.Context, db *sql.DB, key string) (int64, bool) {
	var id int64
	err := db.QueryRowContext(ctx, "SELECT id FROM jobs WHERE dedupe_key = ? ORDER BY id DESC LIMIT 1", key).Scan(&id)
	return id, err == nil
}

// HasDueQueued reports whether at least one queued job of kind is ready to run now.
func HasDueQueued(ctx context.Context, db *sql.DB, kind string) bool {
	if db == nil || kind == "" {
		return false
	}
	var one int
	err := db.QueryRowContext(ctx,
		"SELECT 1 FROM jobs WHERE kind = ? AND state = 'queued' AND run_after <= ? LIMIT 1",
		kind, database.FormatTime(time.Now()),
	).Scan(&one)
	return err == nil
}
