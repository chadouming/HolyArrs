package subtitles

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/importer"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/subtext"
)

type heldSubtitle struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// saveNote adds the exact held-aside files to the saved subtitle row, so an
// interrupted save can be undone. Notes without them still decode.
type saveNote struct {
	Subtitle
	Held     []heldSubtitle `json:"_held,omitempty"`
	NewProof string         `json:"_new_proof,omitempty"`
}

// writeAt is save at a path relative to the Library folder. A file already at
// rel must belong to old or be one the app saved, and it is held aside.
func (s *Service) writeAt(ctx context.Context, fc *fileCtx, row *Subtitle, text, rel string, old *Subtitle, by domain.Actor) (string, error) {
	full := filepath.Join(fc.lib, filepath.FromSlash(rel))
	dir := filepath.Dir(full)
	if !strings.HasPrefix(full, filepath.Clean(fc.lib)+string(filepath.Separator)) || !importer.InsideReal(fc.lib, dir) {
		return "", fmt.Errorf("%s is outside the Library folder", rel)
	}
	// Collect what this save replaces: the previous saved subtitle when this is a
	// better one, and whatever sits at the new name.
	var replace []Subtitle
	if old != nil {
		replace = append(replace, *old)
	}
	if fi, err := os.Lstat(full); err == nil {
		if !fi.Mode().IsRegular() {
			return "", fmt.Errorf("%s is not a regular file", rel)
		}
		if old == nil || old.RelativePath == nil || *old.RelativePath != rel {
			mine, err := database.Select[Subtitle](ctx, s.Store.Read(), "subtitles",
				"file_id = ? AND place = 'beside' AND state = 'present' AND relative_path = ? AND origin IN ('fetched', 'by_hand')", fc.file.ID, rel)
			if err != nil {
				return "", err
			}
			if len(mine) == 0 {
				return "", ErrNotOurs
			}
			replace = append(replace, mine[0])
		}
	}
	tmp, err := importer.CreateTemp(dir, ".holyarrs-sub-", "")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // already gone after the rename; removes the leftover on failure
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	// Record the save before touching the disk, so a save cut off after its file
	// is in place gets finished by the next look.
	t := now()
	row.RelativePath, row.SavedAt = &rel, &t
	// Choose and record the exact holding names before moving anything. If the
	// process stops after a hold but before the new file lands, recovery can put
	// the old subtitle back instead of losing its only usable name.
	var held []heldSubtitle
	seen := map[string]bool{}
	for _, r := range replace {
		if r.RelativePath == nil {
			continue
		}
		from := filepath.Join(fc.lib, filepath.FromSlash(*r.RelativePath))
		if seen[from] {
			continue
		}
		seen[from] = true
		fi, statErr := os.Lstat(from)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return "", statErr
		}
		if !fi.Mode().IsRegular() {
			return "", fmt.Errorf("%s is not a regular file", *r.RelativePath)
		}
		to, err := importer.HoldPath(fc.lib, *r.RelativePath, now())
		if err != nil {
			return "", err
		}
		held = append(held, heldSubtitle{From: from, To: to})
	}
	rowJSON, err := json.Marshal(saveNote{Subtitle: *row, Held: held, NewProof: bytesProof([]byte(text))})
	if err != nil {
		return "", err
	}
	var replaceIDs []int64
	for _, r := range replace {
		replaceIDs = append(replaceIDs, r.ID)
	}
	idsJSON, _ := json.Marshal(replaceIDs)
	var saveID int64
	err = s.Store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "INSERT INTO subtitle_saves (created_at, file_id, relative_path, row, replace_ids) VALUES (?, ?, ?, ?, ?)",
			database.FormatTime(t), row.FileID, rel, string(rowJSON), string(idsJSON))
		if err == nil {
			saveID, err = res.LastInsertId()
		}
		return err
	})
	if err != nil {
		return "", err
	}
	forget := func() {
		_ = s.Store.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "DELETE FROM subtitle_saves WHERE id = ?", saveID)
			return err
		})
	}
	putBack := func() {
		for i := len(held) - 1; i >= 0; i-- {
			_ = os.Rename(held[i].To, held[i].From)
		}
	}
	for i, h := range held {
		err := os.MkdirAll(filepath.Dir(h.To), 0o777)
		if err != nil {
			putBack()
			forget()
			return "", err
		}
		if err := os.Rename(h.From, h.To); err != nil {
			putBack()
			forget()
			return "", err
		}
		if saveHoldCrash != nil {
			if err := saveHoldCrash(i + 1); err != nil {
				return "", err
			}
		}
	}
	if saveHoldCrash != nil && len(held) == 0 {
		if err := saveHoldCrash(0); err != nil {
			return "", err
		}
	}
	if err := renameInto(tmpName, full); err != nil {
		// Move the held files back to their names so nothing has changed.
		putBack()
		forget()
		return "", err
	}
	if saveCrash != nil {
		if err := saveCrash(); err != nil {
			return "", err
		}
	}
	err = s.Store.Write(ctx, func(tx *sql.Tx) error {
		return s.recordSave(ctx, tx, row, replaceIDs, saveID, by)
	})
	return rel, err
}

// renameInto puts a written subtitle at its final name. Tests swap it to fail.
var renameInto = os.Rename

// saveCrash lets tests stop a save after its file is in place and before its
// row is written, as if the process died at that point.
var saveCrash func() error

// saveHoldCrash lets tests stop a save after old subtitles were held and
// before the new file was installed, as if the process died at that point.
var saveHoldCrash func(held int) error

// recordSave writes the row of a finished save. It marks the rows it replaces
// as replaced, removes the wish it meets and deletes its subtitle_saves note.
func (s *Service) recordSave(ctx context.Context, tx *sql.Tx, row *Subtitle, replaceIDs []int64, saveID int64, by domain.Actor) error {
	ts := database.FormatTime(now())
	for _, id := range replaceIDs {
		if _, err := tx.ExecContext(ctx, "UPDATE subtitles SET state = 'replaced', updated_at = ? WHERE id = ?", ts, id); err != nil {
			return err
		}
	}
	// Any other present row at the same name is replaced too, since one file has one row.
	if _, err := tx.ExecContext(ctx, "UPDATE subtitles SET state = 'replaced', updated_at = ? WHERE file_id = ? AND place = 'beside' AND state = 'present' AND relative_path = ?",
		ts, row.FileID, *row.RelativePath); err != nil {
		return err
	}
	if _, err := database.Insert(tx, "subtitles", row, by); err != nil {
		return err
	}
	// The saved file meets the wish for its own language and for its base
	// language, as a pt-BR file meets a pt wish.
	if _, err := tx.ExecContext(ctx, "DELETE FROM subtitles WHERE file_id = ? AND place = 'wish' AND language IN (?, ?) AND forced = ?",
		row.FileID, row.Language, baseOf(row.Language), row.Forced); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM subtitle_saves WHERE id = ?", saveID)
	return err
}

// finishSaves completes a File's saves that were cut off. A save whose file is
// in place gets its row, marked as the app's own rather than found. A save
// whose file is absent is dropped.
func (s *Service) finishSaves(ctx context.Context, tx *sql.Tx, fc *fileCtx) error {
	rows, err := tx.QueryContext(ctx, "SELECT id, relative_path, row, replace_ids FROM subtitle_saves WHERE file_id = ? ORDER BY id", fc.file.ID)
	if err != nil {
		return err
	}
	type save struct {
		id            int64
		rel, row, ids string
	}
	var ss []save
	for rows.Next() {
		var v save
		if err := rows.Scan(&v.id, &v.rel, &v.row, &v.ids); err != nil {
			rows.Close()
			return err
		}
		ss = append(ss, v)
	}
	rows.Close()
	for _, v := range ss {
		var note saveNote
		var ids []int64
		if json.Unmarshal([]byte(v.row), &note) != nil || json.Unmarshal([]byte(v.ids), &ids) != nil ||
			note.RelativePath == nil || *note.RelativePath != v.rel {
			if _, err := tx.ExecContext(ctx, "DELETE FROM subtitle_saves WHERE id = ?", v.id); err != nil {
				return err
			}
			continue
		}
		target := filepath.Join(fc.lib, filepath.FromSlash(v.rel))
		installed, err := savedSubtitleInstalled(fc.lib, target, note)
		if err != nil {
			return err
		}
		if !installed {
			if err := restoreHeldSubtitles(fc.lib, note.Held); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM subtitle_saves WHERE id = ?", v.id); err != nil {
				return err
			}
			continue
		}
		note.ID = 0
		if err := s.recordSave(ctx, tx, &note.Subtitle, ids, v.id, domain.System); err != nil {
			return err
		}
	}
	return nil
}

func bytesProof(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%d:%s", len(b), hex.EncodeToString(sum[:]))
}

func fileMatchesProof(name, proof string) bool {
	sizeText, wantHash, ok := strings.Cut(proof, ":")
	if !ok {
		return false
	}
	wantSize, err := strconv.ParseInt(sizeText, 10, 64)
	if err != nil || wantSize < 0 || wantSize > subtext.MaxEntryText {
		return false
	}
	st, err := os.Lstat(name)
	if err != nil || !st.Mode().IsRegular() || st.Size() != wantSize {
		return false
	}
	f, err := os.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, wantSize+1))
	return err == nil && n == wantSize && hex.EncodeToString(h.Sum(nil)) == wantHash
}

func savedSubtitleInstalled(lib, target string, note saveNote) (bool, error) {
	if err := validateHeldSubtitles(lib, note.Held); err != nil {
		return false, err
	}
	if !fileMatchesProof(target, note.NewProof) {
		return false, nil
	}
	for _, h := range note.Held {
		_, fromErr := os.Lstat(h.From)
		_, toErr := os.Lstat(h.To)
		if filepath.Clean(h.From) == filepath.Clean(target) {
			if fromErr == nil && toErr == nil {
				continue
			}
			return false, nil
		}
		if errors.Is(fromErr, os.ErrNotExist) && toErr == nil {
			continue
		}
		return false, nil
	}
	return true, nil
}

func validateHeldSubtitles(lib string, held []heldSubtitle) error {
	lib = filepath.Clean(lib)
	holding := filepath.Join(lib, importer.HoldingDir)
	for _, h := range held {
		if !strings.HasPrefix(filepath.Clean(h.From), lib+string(filepath.Separator)) ||
			!strings.HasPrefix(filepath.Clean(h.To), holding+string(filepath.Separator)) ||
			!importer.InsideReal(lib, filepath.Dir(h.From)) || !importer.InsideReal(lib, filepath.Dir(h.To)) {
			return errors.New("a held subtitle path leaves the Library folder")
		}
	}
	return nil
}

func restoreHeldSubtitles(lib string, held []heldSubtitle) error {
	if err := validateHeldSubtitles(lib, held); err != nil {
		return err
	}
	for i := len(held) - 1; i >= 0; i-- {
		h := held[i]
		_, fromErr := os.Lstat(h.From)
		_, toErr := os.Lstat(h.To)
		switch {
		case fromErr == nil && errors.Is(toErr, os.ErrNotExist):
			continue // already put back
		case errors.Is(fromErr, os.ErrNotExist) && errors.Is(toErr, os.ErrNotExist):
			continue // the file vanished before it could be held
		case errors.Is(fromErr, os.ErrNotExist) && toErr == nil:
			if err := os.Rename(h.To, h.From); err != nil {
				return err
			}
		case fromErr == nil && toErr == nil:
			return fmt.Errorf("both the subtitle and its held copy exist at %s", filepath.Base(h.From))
		default:
			return errors.Join(fromErr, toErr)
		}
	}
	return nil
}
