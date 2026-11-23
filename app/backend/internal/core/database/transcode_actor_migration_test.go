package database

import (
	"context"
	"database/sql"
	"testing"
)

// 0139 repairs plain 'system' actor values in transcode tables into valid JSON objects.
func TestTranscodeActorMigration(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	old, err := Open(dir, Options{MainMigrations: upTo(t, 138)})
	if err != nil {
		t.Fatal(err)
	}

	const userActor = `{"kind":"user","id":7}`
	const sysActor = `{"kind":"system"}`

	err = old.Write(ctx, func(tx *sql.Tx) error {
		var rulesID int64
		if err := tx.QueryRow(`SELECT min(id) FROM pick_rules`).Scan(&rulesID); err != nil {
			return err
		}
		for _, q := range []string{
			`INSERT INTO library_folders(id, created_at, created_by, updated_at, updated_by, path, kind, label) VALUES
				(1, 't', '{}', 't', '{}', '/movies', 'movie', 'Movies'),
				(2, 't', '{}', 't', '{}', '/shows', 'show', 'Shows')`,
			`INSERT INTO titles(id, created_at, created_by, updated_at, updated_by, kind, name, sort_name, metadata_source, why_added, state) VALUES
				(1, 't', '{}', 't', '{}', 'movie', 'Film', 'Film', 'tmdb', 'by_hand', 'active')`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`INSERT INTO versions(id, created_at, created_by, updated_at, updated_by, title_id, label, pick_rules_id, library_folder_id, folder_name, is_main) VALUES (1, 't', '{}', 't', '{}', 1, 'HD', ?, 1, 'Film', 1)`, rulesID); err != nil {
			return err
		}
		for _, q := range []string{
			`INSERT INTO files(id, created_at, created_by, updated_at, updated_by, slot_ids, version_id, relative_path, added_at, origin, placed_by) VALUES
				(1, 't', '{}', 't', '{}', '[]', 1, 'Film/Film1.mkv', 't', 'imported', 'copy'),
				(2, 't', '{}', 't', '{}', '[]', 1, 'Film/Film2.mkv', 't', 'imported', 'copy')`,
			`INSERT INTO transcode_profiles(id, name, description, is_default, config_json, created_at, updated_at, created_by, updated_by) VALUES
				(1, 'Legacy Default', '', 1, '{}', 't', 't', 'system', 'system'),
				(2, 'Custom', '', 0, '{}', 't', 't', '` + userActor + `', '` + userActor + `')`,
			`INSERT INTO transcode_library_profiles(library_folder_id, enabled, profile_id, updated_at, updated_by) VALUES
				(1, 1, 1, 't', 'system'),
				(2, 1, 2, 't', '` + userActor + `')`,
			`INSERT INTO transcode_items(id, file_id, profile_id, state, created_at, updated_at, created_by, updated_by) VALUES
				(1, 1, 1, 'pending', 't', 't', 'system', 'system'),
				(2, 2, 2, 'pending', 't', 't', '` + userActor + `', '` + userActor + `')`,
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
	_ = old.Close()

	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	assertActors := func(query string, wantCreated, wantUpdated string) {
		t.Helper()
		var gotCreated, gotUpdated string
		if err := s.Read().QueryRow(query).Scan(&gotCreated, &gotUpdated); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		if gotCreated != wantCreated || gotUpdated != wantUpdated {
			t.Fatalf("%s: got (%q, %q), want (%q, %q)", query, gotCreated, gotUpdated, wantCreated, wantUpdated)
		}
	}

	assertActors(`SELECT created_by, updated_by FROM transcode_profiles WHERE id = 1`, sysActor, sysActor)
	assertActors(`SELECT created_by, updated_by FROM transcode_profiles WHERE id = 2`, userActor, userActor)
	assertActors(`SELECT created_by, updated_by FROM transcode_items WHERE id = 1`, sysActor, sysActor)
	assertActors(`SELECT created_by, updated_by FROM transcode_items WHERE id = 2`, userActor, userActor)

	assertUpdatedBy := func(folderID int64, want string) {
		t.Helper()
		var got string
		if err := s.Read().QueryRow(`SELECT updated_by FROM transcode_library_profiles WHERE library_folder_id = ?`, folderID).Scan(&got); err != nil {
			t.Fatalf("library_folder_id=%d: %v", folderID, err)
		}
		if got != want {
			t.Fatalf("transcode_library_profiles(%d): got %q, want %q", folderID, got, want)
		}
	}
	assertUpdatedBy(1, sysActor)
	assertUpdatedBy(2, userActor)

	var fk int
	if err := s.Read().QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d %v", fk, err)
	}
}
