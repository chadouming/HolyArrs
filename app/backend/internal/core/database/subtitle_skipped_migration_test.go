package database

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
)

// 0138 widens subtitles.state CHECK to allow 'skipped' and adds history/slot indexes.
func TestSubtitleSkippedMigration(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	old, err := Open(dir, Options{MainMigrations: upTo(t, 137)})
	if err != nil {
		t.Fatal(err)
	}
	err = old.Write(ctx, func(tx *sql.Tx) error {
		var rulesID int64
		if err := tx.QueryRow(`SELECT min(id) FROM pick_rules`).Scan(&rulesID); err != nil {
			return err
		}
		for _, q := range []string{
			`INSERT INTO library_folders(id, created_at, created_by, updated_at, updated_by, path, kind, label) VALUES (1, 't', '{}', 't', '{}', '/movies', 'movie', 'Movies')`,
			`INSERT INTO titles(id, created_at, created_by, updated_at, updated_by, kind, name, sort_name, metadata_source, why_added, state) VALUES (1, 't', '{}', 't', '{}', 'movie', 'Film', 'Film', 'tmdb', 'by_hand', 'active')`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`INSERT INTO versions(id, created_at, created_by, updated_at, updated_by, title_id, label, pick_rules_id, library_folder_id, folder_name, is_main) VALUES (1, 't', '{}', 't', '{}', 1, 'HD', ?, 1, 'Film', 1)`, rulesID); err != nil {
			return err
		}
		for _, q := range []string{
			`INSERT INTO files(id, created_at, created_by, updated_at, updated_by, slot_ids, version_id, relative_path, added_at, origin, placed_by) VALUES (1, 't', '{}', 't', '{}', '[]', 1, 'Film/Film.mkv', 't', 'imported', 'copy')`,
			`INSERT INTO subtitles(id, created_at, created_by, updated_at, updated_by, file_id, language, place, origin, state, made, transcribed, integrity_state) VALUES
				(1, 't', '{}', 't', '{}', 1, 'en', 'beside', 'fetched', 'present', 'ai_translated', 1, 'ok'),
				(2, 't', '{}', 't', '{}', 1, 'es', 'wish', 'fetched', 'wanted', NULL, 0, NULL)`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before := downloaderRows(t, old.Read(), "SELECT * FROM subtitles ORDER BY id")
	_ = old.Close()

	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if after := downloaderRows(t, s.Read(), "SELECT * FROM subtitles ORDER BY id"); !reflect.DeepEqual(after, before) {
		t.Fatalf("rows changed:\n%v\nwant\n%v", after, before)
	}

	insert := func(id int, lang, state string) error {
		return s.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO subtitles(id, created_at, created_by, updated_at, updated_by, file_id, language, place, origin, state) VALUES (?, 't', '{}', 't', '{}', 1, ?, 'wish', 'fetched', ?)`, id, lang, state)
			return err
		})
	}
	if err := insert(3, "fr", "skipped"); err != nil {
		t.Fatalf("skipped state was refused: %v", err)
	}
	if err := insert(4, "de", "bogus"); err == nil {
		t.Fatal("invalid subtitle state was accepted")
	}
	var fk int
	if err := s.Read().QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d %v", fk, err)
	}
}
