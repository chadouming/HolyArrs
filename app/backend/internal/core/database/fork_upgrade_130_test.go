package database

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func oldForkMigrations(t *testing.T) fstest.MapFS {
	t.Helper()
	src, err := fs.Sub(embedded, "migrations/main")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations(src)
	if err != nil {
		t.Fatal(err)
	}
	remapOld := map[int]string{
		160: "0134_title_part_names.sql",
		161: "0135_subtitle_transcribed.sql",
		162: "0136_subtitle_integrity.sql",
		163: "0137_transcode.sql",
		164: "0138_subtitle_skipped.sql",
		165: "0139_transcode_actor_json.sql",
	}
	out := fstest.MapFS{}
	for _, m := range ms {
		if m.version <= 133 {
			out[m.name] = &fstest.MapFile{Data: []byte(m.sql)}
			continue
		}
		if oldName, ok := remapOld[m.version]; ok {
			out[oldName] = &fstest.MapFile{Data: []byte(m.sql)}
		}
	}
	return out
}

func TestForkUpgradeTo130(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	old, err := Open(dir, Options{MainMigrations: oldForkMigrations(t)})
	if err != nil {
		t.Fatalf("open old fork schema: %v", err)
	}
	if v, err := old.Version(); err != nil || v != 139 {
		t.Fatalf("old fork version = %d (%v), want 139", v, err)
	}

	const partNamesJSON = `[{"names":["Final Season Part 2"],"season":4,"offset":16,"source":"anime_map"}]`
	err = old.Write(ctx, func(tx *sql.Tx) error {
		var rulesID int64
		if err := tx.QueryRow(`SELECT min(id) FROM pick_rules`).Scan(&rulesID); err != nil {
			return err
		}
		for _, q := range []string{
			`INSERT INTO library_folders(id, created_at, created_by, updated_at, updated_by, path, kind, label) VALUES
				(1, 't', '{}', 't', '{}', '/shows', 'show', 'Shows')`,
			`INSERT INTO titles(id, created_at, created_by, updated_at, updated_by, kind, name, sort_name, metadata_source, why_added, state, part_names) VALUES
				(1, 't', '{}', 't', '{}', 'show', 'Attack on Titan', 'attack on titan', 'tmdb', 'by_hand', 'active', '` + partNamesJSON + `')`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(
			`INSERT INTO versions(id, created_at, created_by, updated_at, updated_by, title_id, label, pick_rules_id, library_folder_id, folder_name, is_main)
			 VALUES (1, 't', '{}', 't', '{}', 1, 'HD', ?, 1, 'Attack on Titan', 1)`, rulesID,
		); err != nil {
			return err
		}
		for _, q := range []string{
			`INSERT INTO files(id, created_at, created_by, updated_at, updated_by, slot_ids, version_id, relative_path, added_at, origin, placed_by) VALUES
				(1, 't', '{}', 't', '{}', '[]', 1, 'Attack on Titan/S04E17.mkv', 't', 'imported', 'copy')`,
			`INSERT INTO subtitles(id, created_at, created_by, updated_at, updated_by, file_id, language, place, origin, state, made, transcribed, integrity_state) VALUES
				(1, 't', '{}', 't', '{}', 1, 'en', 'wish', 'fetched', 'skipped', 'ai_translated', 1, 'ok')`,
			`INSERT INTO subtitle_transcripts(file_id, state, reason, tries, next_try_at, updated_at) VALUES
				(1, 'final', 'already done', 2, NULL, 't')`,
			`INSERT INTO transcode_profiles(id, name, description, is_default, config_json, created_at, updated_at, created_by, updated_by) VALUES
				(1, 'HEVC', 'Default HEVC', 1, '{"codec":"hevc"}', 't', 't', '{"kind":"system"}', '{"kind":"system"}')`,
			`INSERT INTO transcode_items(id, file_id, profile_id, state, source_codec, target_codec, created_at, updated_at, created_by, updated_by) VALUES
				(1, 1, 1, 'done', 'h264', 'hevc', 't', 't', '{"kind":"system"}', '{"kind":"system"}')`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed old fork data: %v", err)
	}
	_ = old.Close()

	updating := 0
	s, err := Open(dir, Options{OnUpdating: func() { updating++ }})
	if err != nil {
		t.Fatalf("upgrade to 1.3.0: %v", err)
	}
	defer s.Close()

	if updating != 1 {
		t.Fatalf("OnUpdating called %d times, want 1", updating)
	}
	if v, err := s.Version(); err != nil || v != 165 {
		t.Fatalf("upgraded version = %d (%v), want 165", v, err)
	}

	backups, err := filepath.Glob(filepath.Join(dir, "backups", "main-pre-update-*.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("pre-update backups = %v (%v), want 1", backups, err)
	}
	if err := CheckBackupSum(backups[0]); err != nil {
		t.Fatalf("backup checksum: %v", err)
	}
	b, err := sql.Open("sqlite", fileURI(backups[0])+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	var old134Name string
	if err := b.QueryRow(`SELECT name FROM schema_migrations WHERE version = 134`).Scan(&old134Name); err != nil || old134Name != "0134_title_part_names.sql" {
		_ = b.Close()
		t.Fatalf("backup version 134 name = %q (%v), want pre-remap 0134_title_part_names.sql", old134Name, err)
	}
	_ = b.Close()

	wantMigrations, err := EmbeddedMigrations("migrations/main")
	if err != nil {
		t.Fatal(err)
	}
	if len(wantMigrations) == 0 || wantMigrations[len(wantMigrations)-1].Version != 165 {
		t.Fatalf("embedded main migrations = %d, want last version 165", len(wantMigrations))
	}
	rows, err := s.Read().Query(`SELECT version, name FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	gotMigrations := map[int]string{}
	for rows.Next() {
		var v int
		var name string
		if err := rows.Scan(&v, &name); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		gotMigrations[v] = name
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(gotMigrations) != len(wantMigrations) {
		t.Fatalf("schema_migrations count = %d, want %d", len(gotMigrations), len(wantMigrations))
	}
	for _, m := range wantMigrations {
		if gotMigrations[m.Version] != m.Name {
			t.Fatalf("schema_migrations[%d] = %q, want %q", m.Version, gotMigrations[m.Version], m.Name)
		}
	}

	// Verify upstream 1.3.0 columns and tables exist.
	for _, check := range []string{
		`SELECT cleaned_at FROM downloads LIMIT 0`,
		`SELECT imdb_rating FROM titles LIMIT 0`,
		`SELECT title_id FROM trakt_watch_state LIMIT 0`,
		`SELECT plugin_id FROM plugin_links LIMIT 0`,
		`SELECT via_plugin FROM asks LIMIT 0`,
	} {
		if _, err := s.Read().Exec(check); err != nil {
			t.Fatalf("upstream schema check %q failed: %v", check, err)
		}
	}

	// Verify all pre-existing fork data stayed intact.
	var gotPartNames string
	if err := s.Read().QueryRow(`SELECT part_names FROM titles WHERE id = 1`).Scan(&gotPartNames); err != nil || gotPartNames != partNamesJSON {
		t.Fatalf("titles.part_names = %q (%v), want %q", gotPartNames, err, partNamesJSON)
	}
	var gotState, gotIntState string
	var gotTranscribed int
	if err := s.Read().QueryRow(`SELECT state, transcribed, integrity_state FROM subtitles WHERE id = 1`).Scan(&gotState, &gotTranscribed, &gotIntState); err != nil ||
		gotState != "skipped" || gotTranscribed != 1 || gotIntState != "ok" {
		t.Fatalf("subtitles row = (%q, %d, %q, %v), want (skipped, 1, ok)", gotState, gotTranscribed, gotIntState, err)
	}
	var gotTrState, gotTrReason string
	var gotTrTries int
	if err := s.Read().QueryRow(`SELECT state, reason, tries FROM subtitle_transcripts WHERE file_id = 1`).Scan(&gotTrState, &gotTrReason, &gotTrTries); err != nil ||
		gotTrState != "final" || gotTrReason != "already done" || gotTrTries != 2 {
		t.Fatalf("subtitle_transcripts = (%q, %q, %d, %v)", gotTrState, gotTrReason, gotTrTries, err)
	}
	var gotTcState, gotTcSrc, gotTcDst string
	if err := s.Read().QueryRow(`SELECT state, source_codec, target_codec FROM transcode_items WHERE id = 1`).Scan(&gotTcState, &gotTcSrc, &gotTcDst); err != nil ||
		gotTcState != "done" || gotTcSrc != "h264" || gotTcDst != "hevc" {
		t.Fatalf("transcode_items = (%q, %q, %q, %v)", gotTcState, gotTcSrc, gotTcDst, err)
	}

	// Second open does nothing and creates no extra backup.
	_ = s.Close()
	s2, err := Open(dir, Options{OnUpdating: func() { updating++ }})
	if err != nil {
		t.Fatalf("re-open after upgrade: %v", err)
	}
	defer s2.Close()
	if updating != 1 {
		t.Fatalf("second open called OnUpdating (%d)", updating)
	}
	backups2, _ := filepath.Glob(filepath.Join(dir, "backups", "main-pre-update-*.db"))
	if len(backups2) != 1 {
		t.Fatalf("second open created extra backups: %v", backups2)
	}
}

func TestPureUpstreamUpgradeSkipsForkRemap(t *testing.T) {
	dir := t.TempDir()
	old, err := Open(dir, Options{MainMigrations: upTo(t, 159)})
	if err != nil {
		t.Fatalf("open pure upstream 1.3.0: %v", err)
	}
	_ = old.Close()

	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("upgrade pure upstream to 165: %v", err)
	}
	defer s.Close()

	if v, err := s.Version(); err != nil || v != 165 {
		t.Fatalf("version = %d (%v), want 165", v, err)
	}
	var name134 string
	if err := s.Read().QueryRow(`SELECT name FROM schema_migrations WHERE version = 134`).Scan(&name134); err != nil || name134 != "0134_download_cleaned_at.sql" {
		t.Fatalf("version 134 name = %q (%v), want 0134_download_cleaned_at.sql", name134, err)
	}
}
