package subtitles

import (
	"context"
	"database/sql"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

// transcriptHeld reports whether a File's transcript is not to be asked for
// by itself right now: it ended in a way that will not change, it failed too
// often, or it failed less than a day ago.
func (s *Service) transcriptHeld(ctx context.Context, fileID int64) bool {
	var state string
	var tries int
	var next sql.NullString
	err := s.Store.Read().QueryRowContext(ctx, "SELECT state, tries, next_try_at FROM subtitle_transcripts WHERE file_id = ?", fileID).Scan(&state, &tries, &next)
	if err != nil {
		return false
	}
	if state == "final" || tries >= transcribeTries {
		return true
	}
	return next.Valid && next.String > database.FormatTime(now())
}

// noteTranscript records how a transcript ended when nothing was saved.
func (s *Service) noteTranscript(ctx context.Context, fileID int64, final bool, reason string) {
	state := "failed"
	if final {
		state = "final"
	}
	ts := database.FormatTime(now())
	_ = s.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO subtitle_transcripts (file_id, state, reason, tries, next_try_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)
			ON CONFLICT (file_id) DO UPDATE SET state = excluded.state, reason = excluded.reason, tries = tries + 1, next_try_at = excluded.next_try_at, updated_at = excluded.updated_at`,
			fileID, state, reason, database.FormatTime(now().Add(transcribeHold)), ts)
		return err
	})
}

func (s *Service) clearTranscript(ctx context.Context, fileID int64) {
	_ = s.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM subtitle_transcripts WHERE file_id = ?", fileID)
		return err
	})
}

// termSource is one subtitle a title's terms can be gathered from.
type termSource struct {
	language, place, path, codec, video, lib string
	track                                    int
}

// termSources lists the present subtitles of a title's Files that a person
// made, beside the video or inside it, in the order of the Files' paths.
func termSources(ctx context.Context, db *sql.DB, titleID int64) ([]termSource, error) {
	versions, err := mediafiles.VersionsOfTitle(ctx, db, titleID)
	if err != nil {
		return nil, err
	}
	var out []termSource
	for _, v := range versions {
		var lf mediafiles.LibraryFolder
		if err := database.Get(ctx, db, "library_folders", v.LibraryFolderID, &lf); err != nil {
			continue
		}
		files, err := database.Select[mediafiles.File](ctx, db, "files", "version_id = ? ORDER BY relative_path", v.ID)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			subs, err := database.Select[Subtitle](ctx, db, "subtitles", "file_id = ? AND state = 'present' AND made IS NULL AND place IN ('beside', 'built_in') ORDER BY id", f.ID)
			if err != nil {
				return nil, err
			}
			for _, sb := range subs {
				x := termSource{language: sb.Language, place: sb.Place, video: f.RelativePath, lib: lf.Path, track: -1}
				if sb.RelativePath != nil {
					x.path = *sb.RelativePath
				}
				if sb.Codec != nil {
					x.codec = *sb.Codec
				}
				if sb.TrackIndex != nil {
					x.track = *sb.TrackIndex
				}
				out = append(out, x)
			}
		}
	}
	return out, nil
}
