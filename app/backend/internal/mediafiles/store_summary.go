package mediafiles

import (
	"context"
	"database/sql"
	"strings"
)

// FileSummary is a lightweight file record without media_info or full Quality unmarshaling.
type FileSummary = TranscodeFileSummary

// GetFileSummariesByIDs batch-loads lightweight summaries for the given file IDs.
func GetFileSummariesByIDs(ctx context.Context, db *sql.DB, fileIDs []int64) (map[int64]FileSummary, error) {
	return GetTranscodeFileSummariesByIDs(ctx, db, fileIDs)
}

// GetPresentMainFileSummariesByIDs batch-loads lightweight summaries for present main movie and episode files.
func GetPresentMainFileSummariesByIDs(ctx context.Context, db *sql.DB, fileIDs []int64) (map[int64]FileSummary, error) {
	out := map[int64]FileSummary{}
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
			WHERE f.id IN (`+marks+`) AND f.role = 'main' AND f.state = 'present' AND t.state != 'removed' AND i.kind IN ('movie', 'episode')`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var row FileSummary
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
