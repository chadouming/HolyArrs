package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
)

// Promote moves a queued Job to the front of the execution queue with priority 'now'.
func (q *Queue) Promote(ctx context.Context, id int64) (Job, error) {
	var fresh Job
	err := q.s.Write(ctx, func(tx *sql.Tx) error {
		var j Job
		if err := database.Get(ctx, tx, "jobs", id, &j); err != nil {
			return err
		}
		if j.State != "queued" {
			return fmt.Errorf("%w: %s", ErrNotQueued, j.State)
		}
		now := time.Now().UTC()
		topRunAfter := now
		var minRaw sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT MIN(run_after) FROM jobs WHERE state = 'queued' AND priority = 'now' AND id != ?", id).Scan(&minRaw); err != nil {
			return err
		}
		if minRaw.Valid && minRaw.String != "" {
			if minRunAfter, parseErr := time.Parse(time.RFC3339Nano, minRaw.String); parseErr == nil && !minRunAfter.After(now) {
				topRunAfter = minRunAfter.Add(-1 * time.Second)
			}
		}
		res, err := tx.ExecContext(ctx,
			"UPDATE jobs SET priority = 'now', run_after = ?, updated_at = ? WHERE id = ? AND state = 'queued'",
			database.FormatTime(topRunAfter), database.FormatTime(now), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("%w: %s", ErrNotQueued, j.State)
		}
		return database.Get(ctx, tx, "jobs", id, &fresh)
	})
	if err != nil {
		return Job{}, err
	}
	q.notify(fresh)
	q.poke()
	return fresh, nil
}

// DropQueued removes a queued Job from the jobs table and emits a cancelled notification
// so live subscribers refresh without leaving a cancelled row in history.
func (q *Queue) DropQueued(ctx context.Context, id int64) (Job, error) {
	var j Job
	err := q.s.Write(ctx, func(tx *sql.Tx) error {
		if err := database.Get(ctx, tx, "jobs", id, &j); err != nil {
			return err
		}
		if j.State != "queued" {
			return fmt.Errorf("%w: %s", ErrNotQueued, j.State)
		}
		res, err := tx.ExecContext(ctx, "DELETE FROM jobs WHERE id = ? AND state = 'queued'", id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("%w: %s", ErrNotQueued, j.State)
		}
		q.clearPrepared(id)
		return nil
	})
	if err != nil {
		return Job{}, err
	}
	j.State = "cancelled"
	q.notify(j)
	return j, nil
}
