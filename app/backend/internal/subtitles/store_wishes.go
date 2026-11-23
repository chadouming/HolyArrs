package subtitles

import (
	"context"
	"database/sql"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
)

type wishKey struct {
	language string
	forced   bool
}

func skippedWishesForFile(ctx context.Context, db *sql.DB, fileID int64) (map[wishKey]bool, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT language, forced FROM subtitles WHERE file_id = ? AND place = 'wish' AND state = 'skipped'", fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[wishKey]bool{}
	for rows.Next() {
		var lang string
		var forced bool
		if err := rows.Scan(&lang, &forced); err != nil {
			return nil, err
		}
		out[wishKey{language: lang, forced: forced}] = true
	}
	return out, rows.Err()
}

func hasOpenNonSkippedWish(ctx context.Context, db *sql.DB, fileID int64) (bool, error) {
	var exists int
	err := db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM subtitles WHERE file_id = ? AND place = 'wish' AND state IN ('wanted', 'none_good_enough'))",
		fileID).Scan(&exists)
	return exists != 0, err
}

func markWishesSkipped(ctx context.Context, st *database.Store, fileID int64, language string, forced bool, all bool, by domain.Actor) (int, error) {
	ts := database.FormatTime(now())
	actor := actorJSON(by)
	var count int
	err := st.Write(ctx, func(tx *sql.Tx) error {
		var res sql.Result
		var err error
		if all {
			res, err = tx.ExecContext(ctx,
				`UPDATE subtitles SET state = 'skipped', next_try_at = NULL, updated_at = ?, updated_by = ?
				WHERE file_id = ? AND place = 'wish' AND state IN ('wanted', 'none_good_enough')`,
				ts, actor, fileID)
		} else {
			res, err = tx.ExecContext(ctx,
				`UPDATE subtitles SET state = 'skipped', next_try_at = NULL, updated_at = ?, updated_by = ?
				WHERE file_id = ? AND place = 'wish' AND language = ? AND forced = ? AND state IN ('wanted', 'none_good_enough')`,
				ts, actor, fileID, language, forced)
		}
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		count = int(n)
		return nil
	})
	return count, err
}

func clearSkippedWishes(ctx context.Context, st *database.Store, fileID int64, language string, forced bool, all bool, by domain.Actor) error {
	ts := database.FormatTime(now())
	actor := actorJSON(by)
	return st.Write(ctx, func(tx *sql.Tx) error {
		if all {
			_, err := tx.ExecContext(ctx,
				`UPDATE subtitles SET state = 'wanted', next_try_at = NULL, updated_at = ?, updated_by = ?
				WHERE file_id = ? AND place = 'wish' AND state = 'skipped'`,
				ts, actor, fileID)
			return err
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE subtitles SET state = 'wanted', next_try_at = NULL, updated_at = ?, updated_by = ?
			WHERE file_id = ? AND place = 'wish' AND language = ? AND forced = ? AND state = 'skipped'`,
			ts, actor, fileID, language, forced)
		return err
	})
}
