package subtitles

import (
	"context"
	"database/sql"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
)

func listPresentBesideForVerify(ctx context.Context, db *sql.DB) ([]Subtitle, error) {
	rows, err := database.Select[Subtitle](ctx, db, "subtitles",
		"place = 'beside' AND state = 'present' AND COALESCE(integrity_state, '') != 'accepted' ORDER BY id")
	if rows == nil {
		rows = []Subtitle{}
	}
	return rows, err
}

func listBadBeside(ctx context.Context, db *sql.DB) ([]Subtitle, error) {
	rows, err := database.Select[Subtitle](ctx, db, "subtitles",
		"place = 'beside' AND state = 'present' AND integrity_state = 'bad' ORDER BY integrity_checked_at DESC, id DESC")
	if rows == nil {
		rows = []Subtitle{}
	}
	return rows, err
}

type integrityResultUpdate struct {
	id       int64
	state    string
	issue    string
	note     string
	coverage float64
	lastMS   int64
	videoSec float64
	at       time.Time
}

func execIntegrityResult(ctx context.Context, stmt *sql.Stmt, u integrityResultUpdate) error {
	var issueVal, noteVal any
	if u.state == "bad" {
		issueVal = u.issue
		noteVal = u.note
	}
	var covVal, videoVal any
	if u.videoSec > 0 {
		covVal = u.coverage
		videoVal = u.videoSec
	}
	var lastVal any
	if u.lastMS > 0 {
		lastVal = u.lastMS
	}
	ts := database.FormatTime(u.at)
	_, err := stmt.ExecContext(ctx, u.state, issueVal, noteVal, covVal, lastVal, videoVal, ts, ts, u.id)
	return err
}

func setIntegrityResultsBatch(ctx context.Context, st *database.Store, updates []integrityResultUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	return st.Write(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `UPDATE subtitles SET integrity_state = ?, integrity_issue = ?, integrity_note = ?,
			integrity_coverage = ?, integrity_last_ms = ?, integrity_video_s = ?, integrity_checked_at = ?, updated_at = ?
			WHERE id = ?`)
		if err != nil {
			return err
		}
		defer func() { _ = stmt.Close() }()
		for _, u := range updates {
			if err := execIntegrityResult(ctx, stmt, u); err != nil {
				return err
			}
		}
		return nil
	})
}

func setIntegrityResult(ctx context.Context, st *database.Store, id int64, state, issue, note string, coverage float64, lastMS int64, videoSec float64, at time.Time) error {
	return setIntegrityResultsBatch(ctx, st, []integrityResultUpdate{{
		id:       id,
		state:    state,
		issue:    issue,
		note:     note,
		coverage: coverage,
		lastMS:   lastMS,
		videoSec: videoSec,
		at:       at,
	}})
}

type integrityFileBucket struct {
	fileID int64
	state  string
	count  int
	maxAt  string
}

func integritySummaryStore(ctx context.Context, db *sql.DB) ([]integrityFileBucket, error) {
	rows, err := db.QueryContext(ctx, `SELECT file_id, COALESCE(integrity_state, ''), count(*), COALESCE(max(integrity_checked_at), '')
		FROM subtitles WHERE place = 'beside' AND state = 'present' GROUP BY file_id, COALESCE(integrity_state, '')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []integrityFileBucket
	for rows.Next() {
		var b integrityFileBucket
		if err := rows.Scan(&b.fileID, &b.state, &b.count, &b.maxAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func markReplacedAndTurnDown(ctx context.Context, st *database.Store, row Subtitle, reason string, by domain.Actor, at time.Time) error {
	ts := database.FormatTime(at)
	actor := actorJSON(by)
	return st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE subtitles SET state = 'replaced', updated_at = ?, updated_by = ? WHERE id = ?", ts, actor, row.ID); err != nil {
			return err
		}
		if row.SiteName != nil && row.SiteKey != nil && row.Made == nil {
			if _, err := tx.ExecContext(ctx, `INSERT INTO subtitle_turned_down (created_at, created_by, updated_at, updated_by, file_id, site_name, site_key, reason)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (file_id, site_name, site_key) DO NOTHING`,
				ts, actor, ts, actor, row.FileID, *row.SiteName, *row.SiteKey, reason); err != nil {
				return err
			}
		}
		return nil
	})
}

func acceptIntegrity(ctx context.Context, st *database.Store, id int64, by domain.Actor, at time.Time) error {
	ts := database.FormatTime(at)
	actor := actorJSON(by)
	return st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE subtitles SET integrity_state = 'accepted', integrity_checked_at = ?, updated_at = ?, updated_by = ?
			WHERE id = ? AND place = 'beside' AND state = 'present'`, ts, ts, actor, id)
		return err
	})
}
