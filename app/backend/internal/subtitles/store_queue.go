package subtitles

import (
	"context"
	"database/sql"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
)

// pendingTranslateWish is one unmet wish row on a File that already has a
// readable text subtitle on disk or inside the container.
type pendingTranslateWish struct {
	FileID   int64
	Language string
	Forced   bool
}

// listPendingSpeechFileIDs returns distinct file IDs with open wishes that
// lack any readable text subtitle and are not permanently held from speech.
func listPendingSpeechFileIDs(ctx context.Context, db *sql.DB) ([]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT s.file_id FROM subtitles s
		WHERE s.place = 'wish' AND s.state IN ('wanted', 'none_good_enough')
			AND NOT `+textHere+`
			AND NOT EXISTS (
				SELECT 1 FROM subtitle_transcripts st
				WHERE st.file_id = s.file_id
					AND (st.state = 'final' OR st.tries >= ? OR (st.next_try_at IS NOT NULL AND st.next_try_at > ?))
			)
		ORDER BY s.file_id`, transcribeTries, database.FormatTime(now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// listPendingTranslateWishes returns unmet wish rows on Files that already have
// a readable text subtitle to translate from.
func listPendingTranslateWishes(ctx context.Context, db *sql.DB) ([]pendingTranslateWish, error) {
	rows, err := db.QueryContext(ctx, `SELECT s.file_id, s.language, s.forced FROM subtitles s
		WHERE s.place = 'wish' AND s.state IN ('wanted', 'none_good_enough')
			AND `+textHere+`
		ORDER BY s.file_id, s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pendingTranslateWish
	for rows.Next() {
		var w pendingTranslateWish
		if err := rows.Scan(&w.FileID, &w.Language, &w.Forced); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// listPendingTranslateWishesForFile returns unmet wish rows for one File when
// it has a readable text subtitle.
func listPendingTranslateWishesForFile(ctx context.Context, db *sql.DB, fileID int64) ([]pendingTranslateWish, error) {
	rows, err := db.QueryContext(ctx, `SELECT s.file_id, s.language, s.forced FROM subtitles s
		WHERE s.file_id = ? AND s.place = 'wish' AND s.state IN ('wanted', 'none_good_enough')
			AND `+textHere+`
		ORDER BY s.id`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pendingTranslateWish
	for rows.Next() {
		var w pendingTranslateWish
		if err := rows.Scan(&w.FileID, &w.Language, &w.Forced); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// listPresentSubtitlesForFile returns present beside and built-in subtitle
// rows for fileID without touching disk.
func listPresentSubtitlesForFile(ctx context.Context, db *sql.DB, fileID int64) ([]Subtitle, error) {
	return database.Select[Subtitle](ctx, db, "subtitles",
		"file_id = ? AND place IN ('beside', 'built_in') AND state = 'present'", fileID)
}
