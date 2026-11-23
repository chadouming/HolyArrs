package transcode

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
)

// ItemRecord maps one row of the transcode_items table.
type ItemRecord struct {
	database.Meta
	FileID          int64      `db:"file_id"`
	ProfileID       *int64     `db:"profile_id"`
	State           string     `db:"state"`
	SkipReason      *string    `db:"skip_reason"`
	FileFlowsTagged bool       `db:"fileflows_tagged"`
	FileMtimeNs     int64      `db:"file_mtime_ns"`
	FileSizeBytes   int64      `db:"file_size_bytes"`
	SourceCodec     string     `db:"source_codec"`
	SourceWidth     int        `db:"source_width"`
	SourceHeight    int        `db:"source_height"`
	SourceHDR       string     `db:"source_hdr"`
	TargetCodec     string     `db:"target_codec"`
	SelectedQ       *int       `db:"selected_q"`
	MeanVMAF        *float64   `db:"mean_vmaf"`
	GPUDevice       string     `db:"gpu_device"`
	InputBytes      int64      `db:"input_bytes"`
	OutputBytes     int64      `db:"output_bytes"`
	SavedBytes      int64      `db:"saved_bytes"`
	EncodeSeconds   float64    `db:"encode_seconds"`
	AvgFPS          float64    `db:"avg_fps"`
	AvgSpeed        float64    `db:"avg_speed"`
	EDLApplied      bool       `db:"edl_applied"`
	Tonemapped      bool       `db:"tonemapped"`
	JobID           *int64     `db:"job_id"`
	LastError       *string    `db:"last_error"`
	ProbedAt        *time.Time `db:"probed_at"`
	CompletedAt     *time.Time `db:"completed_at"`
}

// profileRow maps one row of transcode_profiles.
type profileRow struct {
	database.Meta
	Name        string `db:"name"`
	Description string `db:"description"`
	IsDefault   bool   `db:"is_default"`
	ConfigJSON  string `db:"config_json"`
}

// libraryProfileRow maps one row of transcode_library_profiles.
type libraryProfileRow struct {
	LibraryFolderID int64  `db:"library_folder_id"`
	Enabled         bool   `db:"enabled"`
	ProfileID       *int64 `db:"profile_id"`
	UpdatedAt       string `db:"updated_at"`
	UpdatedBy       string `db:"updated_by"`
}

func getItemByFileID(ctx context.Context, db database.Querier, fileID int64) (ItemRecord, bool, error) {
	cols := database.SelectList(&ItemRecord{})
	var row ItemRecord
	err := db.QueryRowContext(ctx, "SELECT "+cols+" FROM transcode_items WHERE file_id = ?", fileID).Scan(database.ScanInto(&row)...)
	if errors.Is(err, sql.ErrNoRows) {
		return row, false, nil
	}
	if err != nil {
		return row, false, err
	}
	return row, true, nil
}

func listItemsByFileID(ctx context.Context, db *sql.DB) (map[int64]ItemRecord, error) {
	rows, err := database.Select[ItemRecord](ctx, db, "transcode_items", "1 = 1")
	if err != nil {
		return nil, err
	}
	out := make(map[int64]ItemRecord, len(rows))
	for _, r := range rows {
		out[r.FileID] = r
	}
	return out, nil
}

type itemOverviewAgg struct {
	MatchedRows  int
	Done         int
	Skipped      int
	Pending      int
	Failed       int
	InputBytes   int64
	OutputBytes  int64
	SavedBytes   int64
	FPSSum       float64
	SpeedSum     float64
	SpeedSamples int
}

func summarizeItemsOverview(ctx context.Context, db *sql.DB, fileIDs []int64) (itemOverviewAgg, error) {
	var agg itemOverviewAgg
	if len(fileIDs) == 0 {
		return agg, nil
	}
	idsJSON, err := json.Marshal(fileIDs)
	if err != nil {
		return agg, err
	}
	err = db.QueryRowContext(ctx, `SELECT
		COUNT(*),
		COALESCE(SUM(CASE WHEN ti.state = 'done' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN ti.state = 'skipped' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN ti.state NOT IN ('done', 'skipped') THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN COALESCE(ti.last_error, '') != '' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN ti.state = 'done' THEN ti.input_bytes ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN ti.state = 'done' THEN ti.output_bytes ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN ti.state = 'done' THEN ti.saved_bytes ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN ti.state = 'done' AND (ti.avg_fps > 0 OR ti.avg_speed > 0) THEN ti.avg_fps ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN ti.state = 'done' AND (ti.avg_fps > 0 OR ti.avg_speed > 0) THEN ti.avg_speed ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN ti.state = 'done' AND (ti.avg_fps > 0 OR ti.avg_speed > 0) THEN 1 ELSE 0 END), 0)
	FROM transcode_items ti
	WHERE ti.file_id IN (SELECT DISTINCT value FROM json_each(?))`, string(idsJSON)).Scan(
		&agg.MatchedRows,
		&agg.Done,
		&agg.Skipped,
		&agg.Pending,
		&agg.Failed,
		&agg.InputBytes,
		&agg.OutputBytes,
		&agg.SavedBytes,
		&agg.FPSSum,
		&agg.SpeedSum,
		&agg.SpeedSamples,
	)
	return agg, err
}

func listItemsByFileIDs(ctx context.Context, db *sql.DB, fileIDs []int64) (map[int64]ItemRecord, error) {
	out := map[int64]ItemRecord{}
	if len(fileIDs) == 0 {
		return out, nil
	}
	seen := make(map[int64]bool, len(fileIDs))
	unique := make([]int64, 0, len(fileIDs))
	for _, id := range fileIDs {
		if id > 0 && !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	if len(unique) == 0 {
		return out, nil
	}
	const chunkSize = 400
	for start := 0; start < len(unique); start += chunkSize {
		end := min(start+chunkSize, len(unique))
		chunk := unique[start:end]
		marks := strings.TrimSuffix(strings.Repeat("?, ", len(chunk)), ", ")
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := database.Select[ItemRecord](ctx, db, "transcode_items", "file_id IN ("+marks+")", args...)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out[r.FileID] = r
		}
	}
	return out, nil
}

type doneEncodeSample struct {
	FileID        int64
	EncodeSeconds float64
	AvgSpeed      float64
}

func recentDoneEncodeStats(ctx context.Context, db *sql.DB, limit int) ([]doneEncodeSample, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.QueryContext(ctx,
		`SELECT file_id, encode_seconds, avg_speed
		 FROM transcode_items
		 WHERE state = 'done' AND encode_seconds > 0
		 ORDER BY COALESCE(completed_at, updated_at) DESC, id DESC
		 LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]doneEncodeSample, 0, limit)
	for rows.Next() {
		var s doneEncodeSample
		if err := rows.Scan(&s.FileID, &s.EncodeSeconds, &s.AvgSpeed); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func listFinishedFileIDs(ctx context.Context, db *sql.DB) (map[int64]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT file_id FROM transcode_items WHERE state IN ('done', 'skipped')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var fid int64
		if err := rows.Scan(&fid); err != nil {
			return nil, err
		}
		out[fid] = true
	}
	return out, rows.Err()
}

func historyFilterWhere(stateFilter string) (string, bool) {
	switch stateFilter {
	case "done":
		return "state = 'done'", true
	case "skipped":
		return "state = 'skipped'", true
	case "history":
		return "state IN ('done', 'skipped')", true
	case "failed":
		return "COALESCE(last_error, '') != ''", true
	default:
		return "", false
	}
}

func listItemsByFilterPage(ctx context.Context, db *sql.DB, stateFilter string, limit, offset int) ([]ItemRecord, int, error) {
	where, ok := historyFilterWhere(stateFilter)
	if !ok {
		return []ItemRecord{}, 0, nil
	}
	var total int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM transcode_items WHERE "+where).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := database.Select[ItemRecord](ctx, db, "transcode_items",
		where+" ORDER BY COALESCE(completed_at, updated_at) DESC, id DESC LIMIT ? OFFSET ?", limit, offset)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func listItemsByFilterAll(ctx context.Context, db *sql.DB, stateFilter string) ([]ItemRecord, error) {
	where, ok := historyFilterWhere(stateFilter)
	if !ok {
		return []ItemRecord{}, nil
	}
	return database.Select[ItemRecord](ctx, db, "transcode_items",
		where+" ORDER BY COALESCE(completed_at, updated_at) DESC, id DESC")
}

func upsertItemsBatch(ctx context.Context, st *database.Store, recs []ItemRecord, by domain.Actor) error {
	if len(recs) == 0 {
		return nil
	}
	return st.Write(ctx, func(tx *sql.Tx) error {
		for i := range recs {
			if err := upsertItemTx(ctx, tx, &recs[i], by); err != nil {
				return err
			}
		}
		return nil
	})
}

func upsertItemTx(ctx context.Context, tx *sql.Tx, rec *ItemRecord, by domain.Actor) error {
	var existingID int64
	err := tx.QueryRowContext(ctx, "SELECT id FROM transcode_items WHERE file_id = ?", rec.FileID).Scan(&existingID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if existingID > 0 {
		rec.ID = existingID
		return database.Update(tx, "transcode_items", rec, by)
	}
	_, err = database.Insert(tx, "transcode_items", rec, by)
	return err
}

func upsertItem(ctx context.Context, st *database.Store, rec *ItemRecord, by domain.Actor) error {
	return st.Write(ctx, func(tx *sql.Tx) error {
		return upsertItemTx(ctx, tx, rec, by)
	})
}

func markItemError(ctx context.Context, st *database.Store, fileID, jobID int64, errMsg string) {
	_ = st.Write(ctx, func(tx *sql.Tx) error {
		ts := database.FormatTime(time.Now().UTC())
		actor, _ := json.Marshal(domain.System)
		res, err := tx.ExecContext(ctx,
			`UPDATE transcode_items SET job_id = ?, last_error = ?, updated_at = ?, updated_by = ? WHERE file_id = ?`,
			jobID, errMsg, ts, string(actor), fileID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return nil
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO transcode_items (file_id, state, job_id, last_error, created_at, updated_at, created_by, updated_by) VALUES (?, 'pending', ?, ?, ?, ?, ?, ?)`,
			fileID, jobID, errMsg, ts, ts, string(actor), string(actor))
		return err
	})
}

func clearItemError(ctx context.Context, st *database.Store, fileID int64) {
	_ = st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE transcode_items SET last_error = NULL, updated_at = ? WHERE file_id = ?`,
			database.FormatTime(time.Now().UTC()), fileID)
		return err
	})
}

func ensureDefaultProfileTx(ctx context.Context, tx *sql.Tx) error {
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM transcode_profiles").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	cfg := DefaultProfileConfig()
	cfg.ApplySettingsOverlay(ReadSettings(ctx, tx))
	if cfg.Validate() != nil {
		cfg = DefaultProfileConfig()
	}
	b, _ := json.Marshal(cfg)
	row := &profileRow{
		Name:        "Default",
		Description: "Alchemist 10-bit AV1 1080p VMAF profile",
		IsDefault:   true,
		ConfigJSON:  string(b),
	}
	_, err := database.Insert(tx, "transcode_profiles", row, domain.System)
	return err
}

func syncSettingsFromDefaultProfileTx(ctx context.Context, tx *sql.Tx, cfg ProfileConfig, by domain.Actor) error {
	st := ReadSettings(ctx, tx)
	st.ApplyProfileOverlay(cfg)
	if err := st.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return database.PutSetting(tx, settingsGroup, settingsKey, string(b), by, time.Now().UTC())
}

func syncDefaultProfileFromSettingsTx(ctx context.Context, tx *sql.Tx, st Settings, by domain.Actor) error {
	if err := ensureDefaultProfileTx(ctx, tx); err != nil {
		return err
	}
	rows, err := database.Select[profileRow](ctx, tx, "transcode_profiles", "is_default = 1 ORDER BY id LIMIT 1")
	if err != nil || len(rows) == 0 {
		return err
	}
	r := rows[0]
	cfg := DefaultProfileConfig()
	_ = json.Unmarshal([]byte(r.ConfigJSON), &cfg)
	cfg.ApplySettingsOverlay(st)
	if err := cfg.Validate(); err != nil {
		return err
	}
	b, _ := json.Marshal(cfg)
	r.ConfigJSON = string(b)
	return database.Update(tx, "transcode_profiles", &r, by)
}

func countProfiles(ctx context.Context, db database.Querier) (int, error) {
	var count int
	err := db.QueryRowContext(ctx, "SELECT count(*) FROM transcode_profiles").Scan(&count)
	return count, err
}

func listProfileRows(ctx context.Context, db database.Lister) ([]profileRow, error) {
	rows, err := database.Select[profileRow](ctx, db, "transcode_profiles", "1 = 1 ORDER BY is_default DESC, id ASC")
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func getProfileRow(ctx context.Context, db database.Querier, id int64) (profileRow, error) {
	var r profileRow
	err := database.Get(ctx, db, "transcode_profiles", id, &r)
	return r, err
}

func listLibraryProfileRows(ctx context.Context, db *sql.DB) (map[int64]libraryProfileRow, error) {
	rows, err := db.QueryContext(ctx, "SELECT library_folder_id, enabled, profile_id, updated_at, updated_by FROM transcode_library_profiles")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]libraryProfileRow{}
	for rows.Next() {
		var r libraryProfileRow
		if err := rows.Scan(&r.LibraryFolderID, &r.Enabled, &r.ProfileID, &r.UpdatedAt, &r.UpdatedBy); err != nil {
			return nil, err
		}
		out[r.LibraryFolderID] = r
	}
	return out, rows.Err()
}

func upsertLibraryProfile(ctx context.Context, st *database.Store, folderID int64, enabled bool, profileID *int64, by domain.Actor) error {
	if profileID != nil {
		var exists int
		if err := st.Read().QueryRowContext(ctx, "SELECT 1 FROM transcode_profiles WHERE id = ?", *profileID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return database.ErrNotFound
			}
			return err
		}
	}
	actor, _ := json.Marshal(by)
	ts := database.FormatTime(time.Now().UTC())
	return st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO transcode_library_profiles (library_folder_id, enabled, profile_id, updated_at, updated_by)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(library_folder_id) DO UPDATE SET
			   enabled = excluded.enabled,
			   profile_id = excluded.profile_id,
			   updated_at = excluded.updated_at,
			   updated_by = excluded.updated_by`,
			folderID, enabled, profileID, ts, string(actor))
		return err
	})
}

func insertProfileTx(ctx context.Context, tx *sql.Tx, row *profileRow, by domain.Actor) error {
	if row.IsDefault {
		if _, err := tx.ExecContext(ctx, "UPDATE transcode_profiles SET is_default = 0 WHERE is_default = 1"); err != nil {
			return err
		}
	}
	_, err := database.Insert(tx, "transcode_profiles", row, by)
	return err
}

func updateProfileTx(ctx context.Context, tx *sql.Tx, row *profileRow, by domain.Actor) error {
	if row.IsDefault {
		if _, err := tx.ExecContext(ctx, "UPDATE transcode_profiles SET is_default = 0 WHERE id != ? AND is_default = 1", row.ID); err != nil {
			return err
		}
	}
	return database.Update(tx, "transcode_profiles", row, by)
}

func deleteProfileTx(ctx context.Context, tx *sql.Tx, id int64) error {
	var total int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM transcode_profiles").Scan(&total); err != nil {
		return err
	}
	var isDef bool
	if err := tx.QueryRowContext(ctx, "SELECT is_default FROM transcode_profiles WHERE id = ?", id).Scan(&isDef); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return database.ErrNotFound
		}
		return err
	}
	if total <= 1 {
		return ErrLastProfile
	}
	if isDef {
		return ErrDefaultProfile
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM transcode_profiles WHERE id = ?", id)
	return err
}
