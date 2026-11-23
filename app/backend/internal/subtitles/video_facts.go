package subtitles

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/container"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/sites"
)

// videoStamp is what the last look saw of a video: its size, modified time and
// the built-in settings its tracks were read with. A video with the same stamp
// is not read again, so a sleeping disk only has to answer a stat.
type videoStamp struct {
	Size     int64
	Modified int64 // unix nanoseconds
	Read     string
}

// stampOf stats the video now. ok is false when it cannot be read.
func stampOf(video string, set Settings) (videoStamp, bool) {
	st, err := os.Stat(video)
	if err != nil || !st.Mode().IsRegular() {
		return videoStamp{}, false
	}
	return videoStamp{Size: st.Size(), Modified: st.ModTime().UnixNano(), Read: fmt.Sprintf("%t/%t", set.UseBuiltIn, set.IgnorePictureTracks)}, true
}

// storedStamp returns the stamp and hash kept from the last look at fileID.
func storedStamp(ctx context.Context, db *sql.DB, fileID int64) (videoStamp, string, bool) {
	st, hash, _, ok := storedLook(ctx, db, fileID)
	return st, hash, ok
}

// storedLook returns the stamp, hash, and wish JSON kept from the last look at fileID.
func storedLook(ctx context.Context, db *sql.DB, fileID int64) (videoStamp, string, string, bool) {
	var size, mod sql.NullInt64
	var read, hash, wish sql.NullString
	err := db.QueryRowContext(ctx, "SELECT video_size, video_modified, video_read, video_hash, wish FROM subtitle_looks WHERE file_id = ?", fileID).
		Scan(&size, &mod, &read, &hash, &wish)
	if err != nil || !size.Valid || !mod.Valid || !read.Valid {
		return videoStamp{}, "", "", false
	}
	return videoStamp{Size: size.Int64, Modified: mod.Int64, Read: read.String}, hash.String, wish.String, true
}

// storedBuiltIn turns the recorded built-in rows back into Haves.
func storedBuiltIn(ctx context.Context, db *sql.DB, fileID int64) ([]Have, error) {
	rows, err := ListFor(ctx, db, fileID)
	if err != nil {
		return nil, err
	}
	return builtInFromRows(rows), nil
}

func builtInFromRows(rows []Subtitle) []Have {
	var out []Have
	for _, r := range rows {
		if r.Place != "built_in" || r.State != "present" {
			continue
		}
		h := Have{Language: r.Language, Forced: r.Forced, HearingImpaired: r.HearingImpaired, Place: "built_in"}
		if r.TrackIndex != nil {
			h.Track = *r.TrackIndex
		}
		if r.Codec != nil {
			h.Codec, h.Picture = *r.Codec, container.PictureCodec(*r.Codec)
		}
		out = append(out, h)
	}
	return out
}

// videoHash returns the file hash that sites match on. It reuses the stored
// hash while the video is unchanged since the last look, and otherwise reads
// the file and stores the result.
func (s *Service) videoHash(ctx context.Context, fc *fileCtx, set Settings) (string, error) {
	now, nowOK := stampOf(fc.video(), set)
	if was, hash, ok := storedStamp(ctx, s.Store.Read(), fc.file.ID); ok && nowOK && hash != "" && was == now {
		return hash, nil
	}
	h, err := sites.FileHash(fc.video())
	if err != nil || !nowOK {
		return h, err
	}
	_ = s.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE subtitle_looks SET video_hash = ? WHERE file_id = ? AND video_size = ? AND video_modified = ?",
			h, fc.file.ID, now.Size, now.Modified)
		return err
	})
	return h, nil
}
