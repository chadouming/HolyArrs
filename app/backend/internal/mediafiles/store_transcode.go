package mediafiles

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
)

// ErrTranscodeNotMain is returned when a file is not a present main movie or episode video.
var ErrTranscodeNotMain = errors.New("not a present main video of a movie or episode")

// TranscodeFileContext holds the Library records needed to transcode one File.
type TranscodeFileContext struct {
	File    File
	Title   Title
	Item    Item
	Version Version
	LibPath string
}

// TranscodeLibraryFile is one present main movie or episode file in the Library.
type TranscodeLibraryFile struct {
	FileID          int64
	TitleID         int64
	Title           string
	MediaKind       string
	LibraryFolderID int64
	Season          *int
	Episode         *int
	EpisodeName     string
	RelativePath    string
	Size            int64
	Quality         Quality
	MediaInfo       map[string]any
}

// TranscodeLibraryFolder is a movie or show library folder for transcode profile assignment.
type TranscodeLibraryFolder struct {
	ID    int64
	Label string
	Path  string
	Kind  string
}

// TranscodeFileSummary is a lightweight file record without media_info or full Quality unmarshaling.
type TranscodeFileSummary struct {
	FileID          int64
	TitleID         int64
	Title           string
	MediaKind       string
	LibraryFolderID int64
	LibPath         string
	Season          *int
	Episode         *int
	EpisodeName     string
	RelativePath    string
	Size            int64
	SourceCodec     string
}

// LoadTranscodeFile loads a present main movie or episode file and its parent records.
func LoadTranscodeFile(ctx context.Context, db *sql.DB, fileID int64) (TranscodeFileContext, error) {
	var fc TranscodeFileContext
	if err := database.Get(ctx, db, "files", fileID, &fc.File); err != nil {
		return fc, err
	}
	if fc.File.Role != "main" || fc.File.State != "present" || len(fc.File.SlotIDs) == 0 {
		return fc, ErrTranscodeNotMain
	}
	var itemID int64
	if err := db.QueryRowContext(ctx, "SELECT item_id FROM slots WHERE id = ?", fc.File.SlotIDs[0]).Scan(&itemID); err != nil {
		return fc, ErrTranscodeNotMain
	}
	if err := database.Get(ctx, db, "items", itemID, &fc.Item); err != nil {
		return fc, err
	}
	if fc.Item.Kind != "movie" && fc.Item.Kind != "episode" {
		return fc, ErrTranscodeNotMain
	}
	if err := database.Get(ctx, db, "versions", fc.File.VersionID, &fc.Version); err != nil {
		return fc, err
	}
	if err := database.Get(ctx, db, "titles", fc.Item.TitleID, &fc.Title); err != nil {
		return fc, err
	}
	if fc.Title.State == "removed" {
		return fc, ErrTranscodeNotMain
	}
	if err := db.QueryRowContext(ctx, "SELECT path FROM library_folders WHERE id = ?", fc.Version.LibraryFolderID).Scan(&fc.LibPath); err != nil {
		return fc, err
	}
	return fc, nil
}

// ListPresentMainVideos returns all present main movie and episode files in the Library.
func ListPresentMainVideos(ctx context.Context, db *sql.DB) ([]TranscodeLibraryFile, error) {
	rows, err := db.QueryContext(ctx, `SELECT f.id, t.id, t.name, i.kind, v.library_folder_id,
		json_extract(i.numbers, '$.season'), json_extract(i.numbers, '$.episode'),
		COALESCE(i.name, ''), f.relative_path, f.size, f.quality, f.media_info
		FROM files f
		JOIN slots s ON s.id = json_extract(f.slot_ids, '$[0]')
		JOIN items i ON i.id = s.item_id
		JOIN titles t ON t.id = i.title_id
		JOIN versions v ON v.id = f.version_id
		WHERE f.role = 'main' AND f.state = 'present' AND t.state != 'removed' AND i.kind IN ('movie', 'episode')
		GROUP BY f.id
		ORDER BY t.sort_name, json_extract(i.numbers, '$.season'), json_extract(i.numbers, '$.episode'), f.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TranscodeLibraryFile{}
	for rows.Next() {
		var row TranscodeLibraryFile
		var qualRaw, infoRaw sql.NullString
		if err := rows.Scan(&row.FileID, &row.TitleID, &row.Title, &row.MediaKind, &row.LibraryFolderID,
			&row.Season, &row.Episode, &row.EpisodeName, &row.RelativePath,
			&row.Size, &qualRaw, &infoRaw); err != nil {
			return nil, err
		}
		if qualRaw.Valid && qualRaw.String != "" {
			_ = json.Unmarshal([]byte(qualRaw.String), &row.Quality)
		}
		if infoRaw.Valid && infoRaw.String != "" {
			_ = json.Unmarshal([]byte(infoRaw.String), &row.MediaInfo)
		}
		if row.MediaInfo == nil {
			row.MediaInfo = map[string]any{}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ListTranscodeLibraryFolders returns movie and show library folders for profile assignment.
func ListTranscodeLibraryFolders(ctx context.Context, db *sql.DB) ([]TranscodeLibraryFolder, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, label, path, kind FROM library_folders WHERE kind IN ('movie', 'show') ORDER BY kind, label, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TranscodeLibraryFolder{}
	for rows.Next() {
		var f TranscodeLibraryFolder
		if err := rows.Scan(&f.ID, &f.Label, &f.Path, &f.Kind); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetTranscodeFileMeta loads lightweight display metadata for a single file.
func GetTranscodeFileMeta(ctx context.Context, db *sql.DB, fileID int64) (TranscodeLibraryFile, error) {
	var row TranscodeLibraryFile
	var qualRaw, infoRaw sql.NullString
	err := db.QueryRowContext(ctx, `SELECT f.id, t.id, t.name, i.kind, v.library_folder_id,
		json_extract(i.numbers, '$.season'), json_extract(i.numbers, '$.episode'),
		COALESCE(i.name, ''), f.relative_path, f.size, f.quality, f.media_info
		FROM files f
		JOIN slots s ON s.id = json_extract(f.slot_ids, '$[0]')
		JOIN items i ON i.id = s.item_id
		JOIN titles t ON t.id = i.title_id
		JOIN versions v ON v.id = f.version_id
		WHERE f.id = ? LIMIT 1`, fileID).Scan(
		&row.FileID, &row.TitleID, &row.Title, &row.MediaKind, &row.LibraryFolderID,
		&row.Season, &row.Episode, &row.EpisodeName, &row.RelativePath,
		&row.Size, &qualRaw, &infoRaw)
	if err != nil {
		return row, err
	}
	if qualRaw.Valid && qualRaw.String != "" {
		_ = json.Unmarshal([]byte(qualRaw.String), &row.Quality)
	}
	if infoRaw.Valid && infoRaw.String != "" {
		_ = json.Unmarshal([]byte(infoRaw.String), &row.MediaInfo)
	}
	if row.MediaInfo == nil {
		row.MediaInfo = map[string]any{}
	}
	return row, nil
}

// UpdateTranscodedFileTx updates an existing files row in place after transcoding,
// preserving its ID so linked beside subtitles remain attached.
func UpdateTranscodedFileTx(ctx context.Context, tx *sql.Tx, fileID, versionID int64, relPath string, size int64, qual Quality, langs []string, mediaInfo map[string]any, by database.Actor) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM files WHERE version_id = ? AND relative_path = ? AND id != ? AND state != 'present'`,
		versionID, relPath, fileID); err != nil {
		return err
	}
	qualJSON, err := json.Marshal(qual)
	if err != nil {
		return err
	}
	if langs == nil {
		langs = []string{}
	}
	langJSON, err := json.Marshal(langs)
	if err != nil {
		return err
	}
	if mediaInfo == nil {
		mediaInfo = map[string]any{}
	}
	infoJSON, err := json.Marshal(mediaInfo)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE files SET relative_path = ?, size = ?, quality = ?, languages = ?, media_info = ?, updated_at = ?, updated_by = ? WHERE id = ?`,
		relPath, size, string(qualJSON), string(langJSON), string(infoJSON),
		database.FormatTime(time.Now().UTC()), actorJSON(by), fileID)
	return err
}

// MainFileIDsForSlots returns the distinct present main file IDs for the given slots.
func MainFileIDsForSlots(ctx context.Context, db *sql.DB, slotIDs []int64) ([]int64, error) {
	if len(slotIDs) == 0 {
		return nil, nil
	}
	seen := make(map[int64]bool, len(slotIDs))
	var out []int64
	for _, sid := range slotIDs {
		var fid sql.NullInt64
		if err := db.QueryRowContext(ctx,
			`SELECT s.file_id FROM slots s
			 JOIN files f ON f.id = s.file_id
			 WHERE s.id = ? AND f.role = 'main' AND f.state = 'present'`, sid).Scan(&fid); err != nil {
			continue
		}
		if fid.Valid && fid.Int64 > 0 && !seen[fid.Int64] {
			seen[fid.Int64] = true
			out = append(out, fid.Int64)
		}
	}
	return out, nil
}

// ListPresentMainVideoIDs returns the IDs of all present main movie and episode files.
func ListPresentMainVideoIDs(ctx context.Context, db *sql.DB) ([]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT f.id
		FROM files f
		JOIN slots s ON s.id = json_extract(f.slot_ids, '$[0]')
		JOIN items i ON i.id = s.item_id
		JOIN titles t ON t.id = i.title_id
		WHERE f.role = 'main' AND f.state = 'present' AND t.state != 'removed' AND i.kind IN ('movie', 'episode')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ListPresentMainVideoRuntimes returns a map of file ID to positive runtime_seconds
// for all present main movie and episode files.
func ListPresentMainVideoRuntimes(ctx context.Context, db *sql.DB) (map[int64]float64, error) {
	rows, err := db.QueryContext(ctx, `SELECT f.id, COALESCE(CAST(json_extract(f.media_info, '$.runtime_seconds') AS REAL), 0)
		FROM files f
		JOIN slots s ON s.id = json_extract(f.slot_ids, '$[0]')
		JOIN items i ON i.id = s.item_id
		JOIN titles t ON t.id = i.title_id
		WHERE f.role = 'main' AND f.state = 'present' AND t.state != 'removed' AND i.kind IN ('movie', 'episode')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]float64{}
	for rows.Next() {
		var id int64
		var rt float64
		if err := rows.Scan(&id, &rt); err != nil {
			return nil, err
		}
		if rt > 0 {
			out[id] = rt
		}
	}
	return out, rows.Err()
}

// ListPresentMainVideoScanRows returns lightweight summaries including library folder paths
// for all present main movie and episode files without reading media_info JSON.
func ListPresentMainVideoScanRows(ctx context.Context, db *sql.DB) ([]TranscodeFileSummary, error) {
	rows, err := db.QueryContext(ctx, `SELECT f.id, t.id, t.name, i.kind, v.library_folder_id, lf.path,
		json_extract(i.numbers, '$.season'), json_extract(i.numbers, '$.episode'),
		COALESCE(i.name, ''), f.relative_path, f.size,
		LOWER(COALESCE(json_extract(f.quality, '$.codec'), ''))
		FROM files f
		JOIN slots s ON s.id = json_extract(f.slot_ids, '$[0]')
		JOIN items i ON i.id = s.item_id
		JOIN titles t ON t.id = i.title_id
		JOIN versions v ON v.id = f.version_id
		JOIN library_folders lf ON lf.id = v.library_folder_id
		WHERE f.role = 'main' AND f.state = 'present' AND t.state != 'removed' AND i.kind IN ('movie', 'episode')
		GROUP BY f.id
		ORDER BY t.sort_name, json_extract(i.numbers, '$.season'), json_extract(i.numbers, '$.episode'), f.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TranscodeFileSummary{}
	for rows.Next() {
		var row TranscodeFileSummary
		if err := rows.Scan(&row.FileID, &row.TitleID, &row.Title, &row.MediaKind,
			&row.LibraryFolderID, &row.LibPath, &row.Season, &row.Episode,
			&row.EpisodeName, &row.RelativePath, &row.Size, &row.SourceCodec); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// GetTranscodeFileSummariesByIDs batch-loads lightweight summaries for the given file IDs.
func GetTranscodeFileSummariesByIDs(ctx context.Context, db *sql.DB, fileIDs []int64) (map[int64]TranscodeFileSummary, error) {
	out := map[int64]TranscodeFileSummary{}
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
		rows, err := db.QueryContext(ctx, `SELECT f.id, t.id, t.name, i.kind, v.library_folder_id, lf.path,
			json_extract(i.numbers, '$.season'), json_extract(i.numbers, '$.episode'),
			COALESCE(i.name, ''), f.relative_path, f.size,
			LOWER(COALESCE(json_extract(f.quality, '$.codec'), ''))
			FROM files f
			JOIN slots s ON s.id = json_extract(f.slot_ids, '$[0]')
			JOIN items i ON i.id = s.item_id
			JOIN titles t ON t.id = i.title_id
			JOIN versions v ON v.id = f.version_id
			JOIN library_folders lf ON lf.id = v.library_folder_id
			WHERE f.id IN (`+marks+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var row TranscodeFileSummary
			if err := rows.Scan(&row.FileID, &row.TitleID, &row.Title, &row.MediaKind,
				&row.LibraryFolderID, &row.LibPath, &row.Season, &row.Episode,
				&row.EpisodeName, &row.RelativePath, &row.Size, &row.SourceCodec); err != nil {
				rows.Close()
				return nil, err
			}
			out[row.FileID] = row
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// GetFileRuntimesByIDs batch-loads runtime_seconds from media_info for the given file IDs.
func GetFileRuntimesByIDs(ctx context.Context, db *sql.DB, ids []int64) (map[int64]float64, error) {
	out := map[int64]float64{}
	seen := make(map[int64]bool, len(ids))
	unique := make([]int64, 0, len(ids))
	for _, id := range ids {
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
		rows, err := db.QueryContext(ctx, `SELECT id, COALESCE(CAST(json_extract(media_info, '$.runtime_seconds') AS REAL), 0) FROM files WHERE id IN (`+marks+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var sec float64
			if err := rows.Scan(&id, &sec); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = sec
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}
