package transcode

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
)

type testFix struct {
	st        *database.Store
	q         *jobs.Queue
	svc       *Service
	mux       *http.ServeMux
	libMovie  string
	libShow   string
	folderM   int64
	folderS   int64
	movieID   int64
	movieFile int64
	epFile    int64
	movieRel  string
	epRel     string
}

func newTestFix(t *testing.T) *testFix {
	t.Helper()
	st, err := database.Open(t.TempDir(), database.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	libM := t.TempDir()
	libS := t.TempDir()
	f := &testFix{
		st:       st,
		libMovie: libM,
		libShow:  libS,
		movieRel: "Movie (2024)/Movie (2024).mp4",
		epRel:    "AnimeShow (2024)/Season 01/AnimeShow - S01E01.mkv",
	}

	ts := database.FormatTime(time.Now().UTC())
	sysActor := `{"kind":"system"}`
	ins := func(tx *sql.Tx, query string, args ...any) int64 {
		res, err := tx.Exec(query, args...)
		if err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		id, _ := res.LastInsertId()
		return id
	}

	if err := st.Write(context.Background(), func(tx *sql.Tx) error {
		var rules int64
		if err := tx.QueryRow("SELECT min(id) FROM pick_rules").Scan(&rules); err != nil {
			return err
		}
		f.folderM = ins(tx, `INSERT INTO library_folders (created_at, created_by, updated_at, updated_by, path, kind, label, health) VALUES (?, ?, ?, ?, ?, 'movie', 'Movies', 'ok')`,
			ts, sysActor, ts, sysActor, libM)
		f.folderS = ins(tx, `INSERT INTO library_folders (created_at, created_by, updated_at, updated_by, path, kind, label, health) VALUES (?, ?, ?, ?, ?, 'show', 'Anime', 'ok')`,
			ts, sysActor, ts, sysActor, libS)

		mTitle := ins(tx, `INSERT INTO titles (created_at, created_by, updated_at, updated_by, kind, name, sort_name, year, original_language, source_ids, metadata_source, why_added, state)
			VALUES (?, ?, ?, ?, 'movie', 'The Test Movie', 'Test Movie, The', 2024, 'fra', '{"tmdb":"101"}', 'tmdb', 'by_hand', 'active')`,
			ts, sysActor, ts, sysActor)
		mVer := ins(tx, `INSERT INTO versions (created_at, created_by, updated_at, updated_by, title_id, label, pick_rules_id, library_folder_id, folder_name, is_main)
			VALUES (?, ?, ?, ?, ?, 'HD', ?, ?, 'Movie (2024)', 1)`,
			ts, sysActor, ts, sysActor, mTitle, rules, f.folderM)
		mItem := ins(tx, `INSERT INTO items (created_at, created_by, updated_at, updated_by, title_id, kind, numbers) VALUES (?, ?, ?, ?, ?, 'movie', '{}')`,
			ts, sysActor, ts, sysActor, mTitle)
		mSlot := ins(tx, `INSERT INTO slots (created_at, created_by, updated_at, updated_by, item_id, version_id, state) VALUES (?, ?, ?, ?, ?, ?, 'in_library')`,
			ts, sysActor, ts, sysActor, mItem, mVer)
		f.movieID = mTitle
		f.movieFile = ins(tx, `INSERT INTO files (created_at, created_by, updated_at, updated_by, slot_ids, version_id, relative_path, size, added_at, origin, placed_by)
			VALUES (?, ?, ?, ?, ?, ?, ?, 4096, ?, 'imported', 'copy')`,
			ts, sysActor, ts, sysActor, "["+strconv.FormatInt(mSlot, 10)+"]", mVer, f.movieRel, ts)
		if _, err := tx.Exec("UPDATE slots SET file_id = ? WHERE id = ?", f.movieFile, mSlot); err != nil {
			return err
		}

		sTitle := ins(tx, `INSERT INTO titles (created_at, created_by, updated_at, updated_by, kind, name, sort_name, year, original_language, genres, source_ids, metadata_source, why_added, state)
			VALUES (?, ?, ?, ?, 'show', 'Anime Test', 'Anime Test', 2024, 'jpn', '["Anime"]', '{"tmdb":"202"}', 'tmdb', 'by_hand', 'active')`,
			ts, sysActor, ts, sysActor)
		sVer := ins(tx, `INSERT INTO versions (created_at, created_by, updated_at, updated_by, title_id, label, pick_rules_id, library_folder_id, folder_name, is_main)
			VALUES (?, ?, ?, ?, ?, 'HD', ?, ?, 'AnimeShow (2024)', 1)`,
			ts, sysActor, ts, sysActor, sTitle, rules, f.folderS)
		sItem := ins(tx, `INSERT INTO items (created_at, created_by, updated_at, updated_by, title_id, kind, numbers) VALUES (?, ?, ?, ?, ?, 'episode', '{"season":1,"episode":1}')`,
			ts, sysActor, ts, sysActor, sTitle)
		sSlot := ins(tx, `INSERT INTO slots (created_at, created_by, updated_at, updated_by, item_id, version_id, state) VALUES (?, ?, ?, ?, ?, ?, 'in_library')`,
			ts, sysActor, ts, sysActor, sItem, sVer)
		f.epFile = ins(tx, `INSERT INTO files (created_at, created_by, updated_at, updated_by, slot_ids, version_id, relative_path, size, added_at, origin, placed_by)
			VALUES (?, ?, ?, ?, ?, ?, ?, 2048, ?, 'imported', 'copy')`,
			ts, sysActor, ts, sysActor, "["+strconv.FormatInt(sSlot, 10)+"]", sVer, f.epRel, ts)
		_, err := tx.Exec("UPDATE slots SET file_id = ? WHERE id = ?", f.epFile, sSlot)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	writeDummy := func(path string, n int) {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, bytes.Repeat([]byte("A"), n), 0o644)
	}
	writeDummy(filepath.Join(libM, filepath.FromSlash(f.movieRel)), 4096)
	writeDummy(filepath.Join(libS, filepath.FromSlash(f.epRel)), 2048)

	q := jobs.New(st, jobs.Options{Workers: 1})
	svc := NewService(st, nil, q)
	svc.DevDRI = t.TempDir()
	svc.SysfsDRM = t.TempDir()
	svc.Register(q)
	mux := http.NewServeMux()
	NewHandlers(st, svc).Routes(mux)

	f.q = q
	f.svc = svc
	f.mux = mux
	return f
}

func doReq(t *testing.T, mux *http.ServeMux, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		buf = *bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func TestProfilesAndLibraryFoldersRoutes(t *testing.T) {
	f := newTestFix(t)

	// 1. Default profile auto-seeded + folders listed.
	rr := doReq(t, f.mux, "GET", "/api/v1/transcode/profiles", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET profiles %d: %s", rr.Code, rr.Body.String())
	}
	var listResp struct {
		Profiles []Profile           `json:"profiles"`
		Folders  []LibraryAssignment `json:"library_folders"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.Profiles) != 1 || listResp.Profiles[0].Name != "Default" || !listResp.Profiles[0].IsDefault {
		t.Fatalf("unexpected initial profiles: %+v", listResp.Profiles)
	}
	if len(listResp.Folders) != 2 {
		t.Fatalf("expected 2 library folders, got %+v", listResp.Folders)
	}
	for _, lf := range listResp.Folders {
		if lf.ProfileID != nil || lf.ProfileName != "Default" || !lf.Enabled {
			t.Fatalf("expected unassigned folder to have nil profile_id and Default profile_name, got %+v", lf)
		}
	}
	defID := listResp.Profiles[0].ID

	// 2. Cannot delete default or only profile (409).
	delDef := doReq(t, f.mux, "DELETE", "/api/v1/transcode/profiles/"+strconv.FormatInt(defID, 10), nil)
	if delDef.Code != http.StatusConflict {
		t.Fatalf("expected 409 deleting only/default profile, got %d: %s", delDef.Code, delDef.Body.String())
	}

	// 3. Create HEVC 720p Vulkan profile with flat payload matching frontend contract.
	createRR := doReq(t, f.mux, "POST", "/api/v1/transcode/profiles", map[string]any{
		"name":              "TV 720p HEVC",
		"description":       "Compact HEVC profile",
		"target_codec":      "hevc",
		"preferred_encoder": "hevc_vulkan",
		"max_height":        720,
		"scale_mode":        "downscale_only",
		"audio_codec":       "opus",
	})
	if createRR.Code != http.StatusCreated {
		t.Fatalf("POST profile %d: %s", createRR.Code, createRR.Body.String())
	}
	var created Profile
	if err := json.Unmarshal(createRR.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.TargetCodec != "hevc" || created.MaxHeight != 720 || created.ScaleMode != "downscale_only" || created.AudioCodec != "opus" {
		t.Fatalf("unexpected created profile: %+v", created)
	}

	// 4. Assign profile and disable folder.
	assignRR := doReq(t, f.mux, "PUT", "/api/v1/transcode/library-folders/"+strconv.FormatInt(f.folderS, 10), map[string]any{
		"enabled":    false,
		"profile_id": created.ID,
	})
	if assignRR.Code != http.StatusOK {
		t.Fatalf("PUT library folder %d: %s", assignRR.Code, assignRR.Body.String())
	}
	var la LibraryAssignment
	if err := json.Unmarshal(assignRR.Body.Bytes(), &la); err != nil {
		t.Fatal(err)
	}
	if la.Enabled || la.ProfileID == nil || *la.ProfileID != created.ID || la.ProfileName != "TV 720p HEVC" {
		t.Fatalf("unexpected library assignment: %+v", la)
	}

	// 5. Queue single disabled file without force -> 409; with force -> 202.
	qDis := doReq(t, f.mux, "POST", "/api/v1/files/"+strconv.FormatInt(f.epFile, 10)+"/transcode", nil)
	if qDis.Code != http.StatusConflict {
		t.Fatalf("expected 409 queueing disabled folder file without force, got %d: %s", qDis.Code, qDis.Body.String())
	}
	qForce := doReq(t, f.mux, "POST", "/api/v1/files/"+strconv.FormatInt(f.epFile, 10)+"/transcode", map[string]any{"force": true})
	if qForce.Code != http.StatusAccepted {
		t.Fatalf("expected 202 queueing forced file, got %d: %s", qForce.Code, qForce.Body.String())
	}

	// 6. Update created profile as default -> unsets old default, then delete old default succeeds.
	updRR := doReq(t, f.mux, "PUT", "/api/v1/transcode/profiles/"+strconv.FormatInt(created.ID, 10), map[string]any{
		"is_default": true,
	})
	if updRR.Code != http.StatusOK {
		t.Fatalf("PUT profile %d: %s", updRR.Code, updRR.Body.String())
	}
	delOld := doReq(t, f.mux, "DELETE", "/api/v1/transcode/profiles/"+strconv.FormatInt(defID, 10), nil)
	if delOld.Code != http.StatusOK {
		t.Fatalf("expected 200 deleting non-default profile, got %d: %s", delOld.Code, delOld.Body.String())
	}

	// Unassigned folder keeps profile_id nil and follows the new default profile name.
	afterRR := doReq(t, f.mux, "GET", "/api/v1/transcode/profiles", nil)
	if afterRR.Code != http.StatusOK {
		t.Fatalf("GET profiles after default change %d: %s", afterRR.Code, afterRR.Body.String())
	}
	var afterResp struct {
		Folders []LibraryAssignment `json:"library_folders"`
	}
	if err := json.Unmarshal(afterRR.Body.Bytes(), &afterResp); err != nil {
		t.Fatal(err)
	}
	for _, lf := range afterResp.Folders {
		if lf.LibraryFolderID == f.folderM {
			if lf.ProfileID != nil || lf.ProfileName != "TV 720p HEVC" {
				t.Fatalf("expected unassigned folder to follow new default with nil profile_id, got %+v", lf)
			}
		}
	}
}

func TestSettingsOverviewItemsAndJobRoutes(t *testing.T) {
	f := newTestFix(t)

	// Settings PUT with gpu_devices array and selected_gpus.
	putSet := doReq(t, f.mux, "PUT", "/api/v1/transcode/settings", map[string]any{
		"auto_transcode": true,
		"concurrent_jobs": 3,
		"gpu_devices":    []string{"/dev/dri/renderD129"},
		"selected_gpus":  []string{"/dev/dri/renderD129"},
	})
	if putSet.Code != http.StatusOK {
		t.Fatalf("PUT settings %d: %s", putSet.Code, putSet.Body.String())
	}
	getSet := doReq(t, f.mux, "GET", "/api/v1/transcode/settings", nil)
	if getSet.Code != http.StatusOK {
		t.Fatalf("GET settings %d: %s", getSet.Code, getSet.Body.String())
	}
	var st Settings
	_ = json.Unmarshal(getSet.Body.Bytes(), &st)
	if !st.AutoTranscode || st.ConcurrentJobs != 3 || len(st.SelectedGPUs) != 1 || st.SelectedGPUs[0] != "/dev/dri/renderD129" {
		t.Fatalf("unexpected settings after save: %+v", st)
	}

	// Pause PUT.
	pauseRR := doReq(t, f.mux, "PUT", "/api/v1/transcode/pause", map[string]any{"paused": true})
	if pauseRR.Code != http.StatusOK || !f.svc.Paused() {
		t.Fatalf("PUT pause %d: %s", pauseRR.Code, pauseRR.Body.String())
	}

	// Empty-body scan, enqueue-alias scan & queue-all.
	scanRR := doReq(t, f.mux, "POST", "/api/v1/transcode/scan", nil)
	if scanRR.Code != http.StatusAccepted {
		t.Fatalf("POST scan %d: %s", scanRR.Code, scanRR.Body.String())
	}
	scanEnqRR := doReq(t, f.mux, "POST", "/api/v1/transcode/scan", map[string]any{"enqueue": true})
	if scanEnqRR.Code != http.StatusAccepted {
		t.Fatalf("POST scan enqueue %d: %s", scanEnqRR.Code, scanEnqRR.Body.String())
	}
	qAllRR := doReq(t, f.mux, "POST", "/api/v1/transcode/queue-all", nil)
	if qAllRR.Code != http.StatusAccepted {
		t.Fatalf("POST queue-all %d: %s", qAllRR.Code, qAllRR.Body.String())
	}

	// Overview, Items, Failed, Hardware.
	ovRR := doReq(t, f.mux, "GET", "/api/v1/transcode/overview", nil)
	if ovRR.Code != http.StatusOK {
		t.Fatalf("GET overview %d: %s", ovRR.Code, ovRR.Body.String())
	}
	var ov Overview
	if err := json.Unmarshal(ovRR.Body.Bytes(), &ov); err != nil {
		t.Fatal(err)
	}
	if ov.Counts.Total != 2 || !ov.Paused || ov.Counts.Queued < 2 {
		t.Fatalf("unexpected overview: %+v", ov)
	}

	itemsRR := doReq(t, f.mux, "GET", "/api/v1/transcode/items?state=pending&q=Test", nil)
	if itemsRR.Code != http.StatusOK {
		t.Fatalf("GET items %d: %s", itemsRR.Code, itemsRR.Body.String())
	}
	var itemsResp struct {
		Items []ItemRow `json:"items"`
		Total int       `json:"total"`
	}
	if err := json.Unmarshal(itemsRR.Body.Bytes(), &itemsResp); err != nil {
		t.Fatal(err)
	}
	if itemsResp.Total != 2 || itemsResp.Items[0].ProfileName != "Default" {
		t.Fatalf("unexpected items response: %+v", itemsResp)
	}

	// Stop one queued job, then retry it, and check /failed + retry-failed.
	if len(ov.Jobs) == 0 {
		t.Fatal("expected queued jobs in overview")
	}
	jid := strconv.FormatInt(ov.Jobs[0].ID, 10)
	stopRR := doReq(t, f.mux, "POST", "/api/v1/transcode/jobs/"+jid+"/stop", nil)
	if stopRR.Code != http.StatusOK {
		t.Fatalf("POST stop job %d: %s", stopRR.Code, stopRR.Body.String())
	}
	failRR := doReq(t, f.mux, "GET", "/api/v1/transcode/failed?state=cancelled", nil)
	if failRR.Code != http.StatusOK {
		t.Fatalf("GET failed %d: %s", failRR.Code, failRR.Body.String())
	}
	retryRR := doReq(t, f.mux, "POST", "/api/v1/transcode/jobs/"+jid+"/retry", nil)
	if retryRR.Code != http.StatusOK {
		t.Fatalf("POST retry job %d: %s", retryRR.Code, retryRR.Body.String())
	}
	retryAllRR := doReq(t, f.mux, "POST", "/api/v1/transcode/jobs/retry-failed", nil)
	if retryAllRR.Code != http.StatusOK {
		t.Fatalf("POST retry-failed %d: %s", retryAllRR.Code, retryAllRR.Body.String())
	}
	hwRR := doReq(t, f.mux, "GET", "/api/v1/transcode/hardware", nil)
	if hwRR.Code != http.StatusOK {
		t.Fatalf("GET hardware %d: %s", hwRR.Code, hwRR.Body.String())
	}
}

func TestHWPresetSettingsProfilesAndLegacyCompat(t *testing.T) {
	f := newTestFix(t)
	ctx := context.Background()

	// Default settings and default profile start at hw_preset="medium".
	st0 := ReadSettings(ctx, f.st.Read())
	if st0.HWPreset != "medium" {
		t.Fatalf("expected default Settings.HWPreset=medium, got %q", st0.HWPreset)
	}
	profs0, _, err := f.svc.ListProfiles(ctx)
	if err != nil || len(profs0) == 0 {
		t.Fatalf("ListProfiles: %v len=%d", err, len(profs0))
	}
	if profs0[0].HWPreset != "medium" {
		t.Fatalf("expected default Profile.HWPreset=medium, got %q", profs0[0].HWPreset)
	}

	// Updating Settings hw_preset="faster" validates, saves, and syncs default profile.
	putFast := doReq(t, f.mux, "PUT", "/api/v1/transcode/settings", map[string]any{
		"hw_preset": "faster",
	})
	if putFast.Code != http.StatusOK {
		t.Fatalf("PUT settings hw_preset=faster %d: %s", putFast.Code, putFast.Body.String())
	}
	var stFast Settings
	if err := json.Unmarshal(putFast.Body.Bytes(), &stFast); err != nil {
		t.Fatal(err)
	}
	if stFast.HWPreset != "faster" {
		t.Fatalf("expected saved Settings.HWPreset=faster, got %q", stFast.HWPreset)
	}
	profsFast, _, err := f.svc.ListProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if profsFast[0].HWPreset != "faster" {
		t.Fatalf("expected synced default profile hw_preset=faster, got %q", profsFast[0].HWPreset)
	}

	// Creating and updating a profile with custom hw_preset="slow".
	cfgSlow := DefaultProfileConfig()
	cfgSlow.HWPreset = "slow"
	createSlow := doReq(t, f.mux, "POST", "/api/v1/transcode/profiles", ProfileInput{
		Name:   "SlowCustom",
		Config: &cfgSlow,
	})
	if createSlow.Code != http.StatusCreated {
		t.Fatalf("POST profile slow %d: %s", createSlow.Code, createSlow.Body.String())
	}
	var pSlow Profile
	if err := json.Unmarshal(createSlow.Body.Bytes(), &pSlow); err != nil {
		t.Fatal(err)
	}
	if pSlow.HWPreset != "slow" {
		t.Fatalf("expected created profile hw_preset=slow, got %q", pSlow.HWPreset)
	}

	// Invalid hw_preset rejected by Settings PUT, Profile POST, and Profile PUT.
	badSet := doReq(t, f.mux, "PUT", "/api/v1/transcode/settings", map[string]any{
		"hw_preset": "ultrafast",
	})
	if badSet.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid settings hw_preset, got %d: %s", badSet.Code, badSet.Body.String())
	}
	cfgBad := DefaultProfileConfig()
	cfgBad.HWPreset = "bogus"
	badCreate := doReq(t, f.mux, "POST", "/api/v1/transcode/profiles", ProfileInput{
		Name:   "BadPreset",
		Config: &cfgBad,
	})
	if badCreate.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid profile create hw_preset, got %d: %s", badCreate.Code, badCreate.Body.String())
	}
	badUpdate := doReq(t, f.mux, "PUT", "/api/v1/transcode/profiles/"+strconv.FormatInt(pSlow.ID, 10), ProfileInput{
		Name:   "BadUpdate",
		Config: &cfgBad,
	})
	if badUpdate.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid profile update hw_preset, got %d: %s", badUpdate.Code, badUpdate.Body.String())
	}

	// Legacy stored JSON without "hw_preset" cleanly defaults to "medium".
	err = f.st.Write(ctx, func(tx *sql.Tx) error {
		if err := database.PutSetting(tx, settingsGroup, settingsKey, `{"auto_transcode":false,"concurrent_jobs":2}`, domain.Actor{Kind: "test"}, time.Now().UTC()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE transcode_profiles SET config_json='{"target_vmaf":95,"codec":"av1"}' WHERE id=?`,
			pSlow.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	legacySt := ReadSettings(ctx, f.st.Read())
	if legacySt.HWPreset != "medium" {
		t.Fatalf("expected legacy settings hw_preset default medium, got %q", legacySt.HWPreset)
	}
	legacyProfs, _, err := f.svc.ListProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range legacyProfs {
		if p.ID == pSlow.ID {
			found = true
			if p.HWPreset != "medium" {
				t.Fatalf("expected legacy profile hw_preset default medium, got %q", p.HWPreset)
			}
		}
	}
	if !found {
		t.Fatalf("legacy profile id=%d not found", pSlow.ID)
	}
}

func TestHWPresetPipelineAndHelpers(t *testing.T) {
	valid := []struct {
		in    string
		norm  string
		nvenc string
	}{
		{"", "medium", "p4"},
		{"  FASTER ", "faster", "p2"},
		{"veryfast", "veryfast", "p1"},
		{"fast", "fast", "p3"},
		{"medium", "medium", "p4"},
		{"slow", "slow", "p5"},
		{"slower", "slower", "p6"},
		{"veryslow", "veryslow", "p7"},
	}
	for _, tc := range valid {
		got, err := normalizeHWPreset(tc.in)
		if err != nil {
			t.Fatalf("normalizeHWPreset(%q) err: %v", tc.in, err)
		}
		if got != tc.norm {
			t.Fatalf("normalizeHWPreset(%q) = %q, want %q", tc.in, got, tc.norm)
		}
		if qsv := qsvPresetFor(tc.in); qsv != tc.norm {
			t.Fatalf("qsvPresetFor(%q) = %q, want %q", tc.in, qsv, tc.norm)
		}
		if nv := nvencPresetFor(tc.in); nv != tc.nvenc {
			t.Fatalf("nvencPresetFor(%q) = %q, want %q", tc.in, nv, tc.nvenc)
		}
	}
	if _, err := normalizeHWPreset("ultrafast"); err == nil || err.Error() != "hw_preset must be veryfast, faster, fast, medium, slow, slower, or veryslow" {
		t.Fatalf("unexpected error for invalid hw_preset: %v", err)
	}

	qsvHW := HardwareInfo{
		VPPQSVAvail:       true,
		ScaleVAAPIAvail:   true,
		PadVAAPIAvail:     true,
		TonemapVAAPIAvail: true,
	}
	qsvGPU := &GPUDevice{Vendor: "intel", Path: "/dev/dri/renderD129"}
	nvHW := HardwareInfo{}
	nvGPU := &GPUDevice{Vendor: "nvidia", Path: "0"}
	video := ProbeStream{CodecName: "h264", PixFmt: "yuv420p", Width: 1920, Height: 1080}
	plan, err := ComputeScalePlan(1920, 1080, "1:1", DefaultProfileConfig())
	if err != nil {
		t.Fatal(err)
	}

	// Default "" -> NVENC p4 in both full encode and VMAF evalQ.
	preND, encND, _ := hwPipelineArgs(nvHW, video, plan, "av1_nvenc", nvGPU, 24, "", true, false, true)
	if got := valueAfter(append(preND, encND...), "-preset"); got != "p4" {
		t.Fatalf("expected NVENC default preset p4, got %q", got)
	}
	preNDEval, evalND := gpuEvalArgs(nvHW, "av1_nvenc", nvGPU, 24, "", true)
	if got := valueAfter(append(preNDEval, evalND...), "-preset"); got != "p4" {
		t.Fatalf("expected NVENC evalQ default preset p4, got %q", got)
	}

	// "faster" -> QSV faster (-async_depth 4), NVENC p2 in full encode and VMAF evalQ.
	preQF, encQF, _ := hwPipelineArgs(qsvHW, video, plan, "av1_qsv", qsvGPU, 20, "faster", true, false, true)
	argsQF := append(preQF, encQF...)
	if valueAfter(argsQF, "-preset") != "faster" || valueAfter(argsQF, "-async_depth") != "4" {
		t.Fatalf("expected QSV faster/async_depth=4, got %v", argsQF)
	}
	preQFEval, evalQF := gpuEvalArgs(qsvHW, "av1_qsv", qsvGPU, 20, "faster", true)
	argsQFEval := append(preQFEval, evalQF...)
	if valueAfter(argsQFEval, "-preset") != "faster" || valueAfter(argsQFEval, "-async_depth") != "4" {
		t.Fatalf("expected QSV evalQ faster/async_depth=4, got %v", argsQFEval)
	}
	preNF, encNF, _ := hwPipelineArgs(nvHW, video, plan, "hevc_nvenc", nvGPU, 22, "faster", true, false, true)
	if got := valueAfter(append(preNF, encNF...), "-preset"); got != "p2" {
		t.Fatalf("expected NVENC faster->p2, got %q", got)
	}
	preNFEval, evalNF := gpuEvalArgs(nvHW, "hevc_nvenc", nvGPU, 22, "faster", true)
	if got := valueAfter(append(preNFEval, evalNF...), "-preset"); got != "p2" {
		t.Fatalf("expected NVENC evalQ faster->p2, got %q", got)
	}

	// "slow" -> QSV slow (-async_depth 4), NVENC p5 in full encode and VMAF evalQ.
	preQS, encQS, _ := hwPipelineArgs(qsvHW, video, plan, "hevc_qsv", qsvGPU, 20, "slow", true, false, true)
	argsQS := append(preQS, encQS...)
	if valueAfter(argsQS, "-preset") != "slow" || valueAfter(argsQS, "-async_depth") != "4" {
		t.Fatalf("expected QSV slow/async_depth=4, got %v", argsQS)
	}
	preQSEval, evalQS := gpuEvalArgs(qsvHW, "hevc_qsv", qsvGPU, 20, "slow", true)
	argsQSEval := append(preQSEval, evalQS...)
	if valueAfter(argsQSEval, "-preset") != "slow" || valueAfter(argsQSEval, "-async_depth") != "4" {
		t.Fatalf("expected QSV evalQ slow/async_depth=4, got %v", argsQSEval)
	}
	preNS, encNS, _ := hwPipelineArgs(nvHW, video, plan, "av1_nvenc", nvGPU, 22, "slow", true, false, true)
	if got := valueAfter(append(preNS, encNS...), "-preset"); got != "p5" {
		t.Fatalf("expected NVENC slow->p5, got %q", got)
	}
	preNSEval, evalNS := gpuEvalArgs(nvHW, "av1_nvenc", nvGPU, 22, "slow", true)
	if got := valueAfter(append(preNSEval, evalNS...), "-preset"); got != "p5" {
		t.Fatalf("expected NVENC evalQ slow->p5, got %q", got)
	}
}

func TestSkipAndWriteTagsSettingsProfilesAndLegacyCompat(t *testing.T) {
	f := newTestFix(t)
	ctx := context.Background()

	// Default Settings and Default Profile start with DefaultSkipTags and DefaultWriteTag.
	st0 := ReadSettings(ctx, f.st.Read())
	if !slices.Equal(st0.SkipTags, DefaultSkipTags()) || st0.WriteTag != DefaultWriteTag {
		t.Fatalf("unexpected defaults in Settings: skip=%v write=%q", st0.SkipTags, st0.WriteTag)
	}
	profs0, _, err := f.svc.ListProfiles(ctx)
	if err != nil || len(profs0) == 0 {
		t.Fatalf("ListProfiles: %v len=%d", err, len(profs0))
	}
	if !slices.Equal(profs0[0].SkipTags, DefaultSkipTags()) || profs0[0].WriteTag != DefaultWriteTag {
		t.Fatalf("unexpected defaults in Profile: skip=%v write=%q", profs0[0].SkipTags, profs0[0].WriteTag)
	}

	// Custom skip_tags (deduped/trimmed, case preserved) and write_tag sync to default profile.
	putCustom := doReq(t, f.mux, "PUT", "/api/v1/transcode/settings", map[string]any{
		"skip_tags": []string{" CustomOne ", "", "customone", "SecondTag"},
		"write_tag": " CustomOut ",
	})
	if putCustom.Code != http.StatusOK {
		t.Fatalf("PUT settings custom tags %d: %s", putCustom.Code, putCustom.Body.String())
	}
	var stCustom Settings
	if err := json.Unmarshal(putCustom.Body.Bytes(), &stCustom); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(stCustom.SkipTags, []string{"CustomOne", "SecondTag"}) || stCustom.WriteTag != "CustomOut" {
		t.Fatalf("unexpected normalized tags in Settings: %+v", stCustom)
	}
	profsCustom, _, err := f.svc.ListProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(profsCustom[0].SkipTags, []string{"CustomOne", "SecondTag"}) || profsCustom[0].WriteTag != "CustomOut" {
		t.Fatalf("expected default profile synced with custom tags, got %+v", profsCustom[0])
	}

	// Explicit empty [] and "" are preserved in both Settings and Profiles.
	putEmpty := doReq(t, f.mux, "PUT", "/api/v1/transcode/settings", map[string]any{
		"skip_tags": []string{},
		"write_tag": "",
	})
	if putEmpty.Code != http.StatusOK {
		t.Fatalf("PUT settings empty tags %d: %s", putEmpty.Code, putEmpty.Body.String())
	}
	stEmpty := ReadSettings(ctx, f.st.Read())
	if len(stEmpty.SkipTags) != 0 || stEmpty.WriteTag != "" {
		t.Fatalf("expected empty skip_tags and write_tag preserved, got %+v", stEmpty)
	}

	// Validation rejects line breaks, > 200 chars, and > 32 skip_tags.
	longStr := strings.Repeat("a", 201)
	tooMany := make([]string, 33)
	for i := range tooMany {
		tooMany[i] = "tag_" + strconv.Itoa(i)
	}
	for _, payload := range []map[string]any{
		{"skip_tags": []string{"bad\ntag"}},
		{"skip_tags": []string{longStr}},
		{"skip_tags": tooMany},
		{"write_tag": "bad\rtag"},
		{"write_tag": longStr},
	} {
		badSet := doReq(t, f.mux, "PUT", "/api/v1/transcode/settings", payload)
		if badSet.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for settings payload %v, got %d: %s", payload, badSet.Code, badSet.Body.String())
		}
		cfgBad := DefaultProfileConfig()
		if v, ok := payload["skip_tags"].([]string); ok {
			cfgBad.SkipTags = v
		}
		if v, ok := payload["write_tag"].(string); ok {
			cfgBad.WriteTag = v
		}
		badProf := doReq(t, f.mux, "POST", "/api/v1/transcode/profiles", ProfileInput{
			Name:   "InvalidTagProfile",
			Config: &cfgBad,
		})
		if badProf.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for profile payload %v, got %d: %s", payload, badProf.Code, badProf.Body.String())
		}
	}

	// Legacy JSON backward compatibility in Settings and Profiles.
	profRes := doReq(t, f.mux, "POST", "/api/v1/transcode/profiles", ProfileInput{
		Name:   "LegacyTest",
		Config: func() *ProfileConfig { c := DefaultProfileConfig(); return &c }(),
	})
	if profRes.Code != http.StatusCreated {
		t.Fatalf("POST profile %d: %s", profRes.Code, profRes.Body.String())
	}
	var legacyProf Profile
	_ = json.Unmarshal(profRes.Body.Bytes(), &legacyProf)

	err = f.st.Write(ctx, func(tx *sql.Tx) error {
		if err := database.PutSetting(tx, settingsGroup, settingsKey, `{"auto_transcode":false,"concurrent_jobs":2,"fileflows_tag_parity":false}`, domain.Actor{Kind: "test"}, time.Now().UTC()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE transcode_profiles SET config_json='{"target_codec":"av1","fileflows_tag_parity":false}' WHERE id=?`,
			legacyProf.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	stLegacyFalse := ReadSettings(ctx, f.st.Read())
	if len(stLegacyFalse.SkipTags) != 0 || stLegacyFalse.WriteTag != "" {
		t.Fatalf("expected legacy parity=false settings -> empty skip_tags/write_tag, got %+v", stLegacyFalse)
	}
	profsLegacy, _, err := f.svc.ListProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range profsLegacy {
		if p.ID == legacyProf.ID && (len(p.SkipTags) != 0 || p.WriteTag != "") {
			t.Fatalf("expected legacy parity=false profile -> empty skip_tags/write_tag, got %+v", p)
		}
	}

	// Explicit skip_tags/write_tag override legacy fileflows_tag_parity=false, and omitted fields default cleanly.
	err = f.st.Write(ctx, func(tx *sql.Tx) error {
		if err := database.PutSetting(tx, settingsGroup, settingsKey, `{"fileflows_tag_parity":false,"skip_tags":["KeepMe"],"write_tag":"CustomW"}`, domain.Actor{Kind: "test"}, time.Now().UTC()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE transcode_profiles SET config_json='{"target_codec":"av1"}' WHERE id=?`,
			legacyProf.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	stExplicit := ReadSettings(ctx, f.st.Read())
	if !slices.Equal(stExplicit.SkipTags, []string{"KeepMe"}) || stExplicit.WriteTag != "CustomW" {
		t.Fatalf("expected explicit tags to override legacy false, got %+v", stExplicit)
	}
	profsOmitted, _, err := f.svc.ListProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range profsOmitted {
		if p.ID == legacyProf.ID && (!slices.Equal(p.SkipTags, DefaultSkipTags()) || p.WriteTag != DefaultWriteTag) {
			t.Fatalf("expected omitted tag fields to default, got %+v", p)
		}
	}
}

func TestOverviewSQLAggregationCountsSavingsAndSpeed(t *testing.T) {
	f := newTestFix(t)
	ctx := context.Background()

	// Seed movieFile as done with error cleared, epFile stays unprobed (pending).
	lastErr := "prev failure"
	if err := upsertItem(ctx, f.st, &ItemRecord{
		FileID:      f.movieFile,
		State:       "done",
		InputBytes:  1000,
		OutputBytes: 400,
		SavedBytes:  600,
		AvgFPS:      240,
		AvgSpeed:    10.0,
		LastError:   &lastErr,
	}, domain.System); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.QueueFile(ctx, f.epFile, false); err != nil {
		t.Fatal(err)
	}

	ov, err := f.svc.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ov.Counts.Total != 2 || ov.Counts.Done != 1 || ov.Counts.Unprobed != 1 || ov.Counts.Pending != 1 || ov.Counts.Failed != 1 {
		t.Fatalf("unexpected overview counts: %+v", ov.Counts)
	}
	if ov.Savings.FilesDone != 1 || ov.Savings.InputBytes != 1000 || ov.Savings.OutputBytes != 400 || ov.Savings.SavedBytes != 600 || ov.Savings.Percent != 60.0 {
		t.Fatalf("unexpected overview savings: %+v", ov.Savings)
	}
	if ov.Speed.Samples != 1 || ov.Speed.FPS != 240.0 || ov.Speed.RealtimeX != 10.0 {
		t.Fatalf("unexpected overview speed: %+v", ov.Speed)
	}
	if len(ov.Jobs) != 1 || ov.Jobs[0].FileID != f.epFile {
		t.Fatalf("expected 1 active queued job for epFile, got %+v", ov.Jobs)
	}
}
