package subtitles

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/secrets"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/startup"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/sites/fakesite"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/faketranslator"
)

func seedQueueMovieFile(t *testing.T, f *fix, name string) int64 {
	t.Helper()
	ts := database.FormatTime(now())
	rel := fmt.Sprintf("%s/%s.mkv", name, name)
	abs := f.abs(rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("not a real video"), 0o644); err != nil {
		t.Fatal(err)
	}
	var fileID int64
	err := f.st.Write(context.Background(), func(tx *sql.Tx) error {
		var rules, lf int64
		if err := tx.QueryRow("SELECT min(id) FROM pick_rules").Scan(&rules); err != nil {
			return err
		}
		if err := tx.QueryRow("SELECT min(id) FROM library_folders").Scan(&lf); err != nil {
			return err
		}
		ins := func(q string, args ...any) int64 {
			res, err := tx.Exec(q, args...)
			if err != nil {
				t.Fatal(err)
			}
			id, _ := res.LastInsertId()
			return id
		}
		tid := ins(`INSERT INTO titles (created_at, created_by, updated_at, updated_by, kind, name, sort_name, year, source_ids, metadata_source, why_added, state)
			VALUES (?, ?, ?, ?, 'movie', ?, ?, 2020, '{}', 'tmdb', 'by_hand', 'active')`, ts, sysActor, ts, sysActor, name, name)
		ver := ins(`INSERT INTO versions (created_at, created_by, updated_at, updated_by, title_id, label, pick_rules_id, library_folder_id, folder_name, is_main)
			VALUES (?, ?, ?, ?, ?, 'HD', ?, ?, ?, 1)`, ts, sysActor, ts, sysActor, tid, rules, lf, name)
		it := ins(`INSERT INTO items (created_at, created_by, updated_at, updated_by, title_id, kind, numbers)
			VALUES (?, ?, ?, ?, ?, 'movie', '{}')`, ts, sysActor, ts, sysActor, tid)
		slot := ins(`INSERT INTO slots (created_at, created_by, updated_at, updated_by, item_id, version_id, state)
			VALUES (?, ?, ?, ?, ?, ?, 'in_library')`, ts, sysActor, ts, sysActor, it, ver)
		fileID = ins(`INSERT INTO files (created_at, created_by, updated_at, updated_by, slot_ids, version_id, relative_path, added_at, release_name, origin, placed_by)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'imported', 'copy')`, ts, sysActor, ts, sysActor, "["+itoa(slot)+"]", ver, rel, ts, name+".2020.1080p.BluRay.x264-GRP")
		_, err := tx.Exec("UPDATE slots SET file_id = ? WHERE id = ?", fileID, slot)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return fileID
}

func makeFileJob(fileID int64, kind string) jobs.Job {
	return jobs.Job{
		Kind:    kind,
		Subject: &domain.Subject{Type: "file", ID: fileID},
	}
}

func TestProtectedFrontOfLine_TwiceWorkingQueue(t *testing.T) {
	var runningSp, runningTr, queuedSp, queuedTr []jobs.Job
	// 4 running speech (file IDs 1..4) and 8 running translate (file IDs 101..108).
	for id := int64(1); id <= 4; id++ {
		runningSp = append(runningSp, makeFileJob(id, KindTranscribe))
	}
	for id := int64(101); id <= 108; id++ {
		runningTr = append(runningTr, makeFileJob(id, KindTranslate))
	}
	// 30 queued speech (file IDs 1001..1030).
	for id := int64(1001); id <= 1030; id++ {
		queuedSp = append(queuedSp, makeFileJob(id, KindTranscribe))
	}
	// 30 queued translate, where file 2001 has 3 language jobs in a row,
	// followed by 2002..2030.
	queuedTr = append(queuedTr, makeFileJob(2001, KindTranslate), makeFileJob(2001, KindTranslate), makeFileJob(2001, KindTranslate))
	for id := int64(2002); id <= 2030; id++ {
		queuedTr = append(queuedTr, makeFileJob(id, KindTranslate))
	}

	got := protectedFrontOfLine(runningSp, runningTr, queuedSp, queuedTr, 4, 8)
	// Expected: 4 running speech + 8 running translate + 8 queued speech (1001..1008) + 16 queued translate (2001..2016) = 36 protected.
	if len(got) != 36 {
		t.Fatalf("protected count = %d, want 36 (12 running + 24 in line)", len(got))
	}
	for id := int64(1); id <= 4; id++ {
		if !got[id] {
			t.Fatalf("running speech file %d not protected", id)
		}
	}
	for id := int64(101); id <= 108; id++ {
		if !got[id] {
			t.Fatalf("running translate file %d not protected", id)
		}
	}
	for id := int64(1001); id <= 1008; id++ {
		if !got[id] {
			t.Fatalf("queued speech front-of-line file %d not protected", id)
		}
	}
	for id := int64(1009); id <= 1030; id++ {
		if got[id] {
			t.Fatalf("queued speech file %d beyond 2x line should not be protected", id)
		}
	}
	for id := int64(2001); id <= 2016; id++ {
		if !got[id] {
			t.Fatalf("queued translate front-of-line file %d not protected", id)
		}
	}
	for id := int64(2017); id <= 2030; id++ {
		if got[id] {
			t.Fatalf("queued translate file %d beyond 2x line should not be protected", id)
		}
	}
}

func TestQueueAllPendingWork_AndWebsiteRetrySkipsProtected(t *testing.T) {
	f := newFix(t)
	ctx := context.Background()
	t.Setenv("HOLYARRS_FLAGS", startup.Lab)

	q := jobs.New(f.st, jobs.Options{Workers: 1, PollInterval: time.Hour})
	v, err := secrets.Open(ctx, f.st, make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	ft := &faketranslator.OpenAI{Key: fakeAIKey}
	trSrv := httptest.NewServer(ft)
	t.Cleanup(trSrv.Close)

	s := &Service{Store: f.st, Vault: v}
	s.Register(q)
	if _, err := s.SaveTranslator(ctx, TranslatorInput{Kind: TranslatorOpenAI, URL: trSrv.URL + "/v1", Model: "lab-model", Key: fakeAIKey}, domain.System); err != nil {
		t.Fatal(err)
	}
	withTranscriber(t, f, s, "en,en,en", "eng")
	transcribeOn(t, f)

	// Wish: English (for speech or site), Spanish translated from English.
	wishAll(t, f, Want{Language: "en"}, Want{Language: "es", TranslateFrom: "en"})

	// Seed 4 speech-pending movies (no text subtitle) and 4 translate-pending movies (beside .en.srt).
	var speechIDs, trIDs []int64
	for i := 1; i <= 4; i++ {
		speechIDs = append(speechIDs, seedQueueMovieFile(t, f, fmt.Sprintf("SpeechMovie%d", i)))
		fid := seedQueueMovieFile(t, f, fmt.Sprintf("TranslateMovie%d", i))
		trIDs = append(trIDs, fid)
		srtPath := f.abs(fmt.Sprintf("TranslateMovie%d/TranslateMovie%d.en.srt", i, i))
		if err := os.WriteFile(srtPath, []byte(fakesite.Cue("hello", 1000)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Look at all seeded files so wish rows exist in DB.
	allSeeded := append(append([]int64{}, speechIDs...), trIDs...)
	for _, id := range allSeeded {
		if _, err := s.Look(ctx, id, domain.System); err != nil {
			t.Fatal(err)
		}
	}
	// Mark fixture files as already looked and met so they do not interfere.
	for _, id := range []int64{f.movieFile, f.ep1File, f.ep2File} {
		_ = f.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec("UPDATE files SET state = 'replaced' WHERE id = ?", id)
			return err
		})
	}

	// Run handleMissing: it must queue ALL 4 speech + ALL 4 translate, then
	// dueFiles must skip the 2*1=2 front-of-line in each queue and queue
	// KindSearch only for the remaining files behind the line.
	run := &jobs.Run{Job: jobs.Job{ID: 1, Kind: KindMissing}}
	if err := s.handleMissing(ctx, run); err != nil {
		t.Fatal(err)
	}

	spQueued, _, err := jobs.ListQueued(ctx, f.st.Read(), []string{KindTranscribe}, 100, 0)
	if err != nil || len(spQueued) != 4 {
		t.Fatalf("queued transcribe = %d (%v), want 4", len(spQueued), err)
	}
	trQueued, _, err := jobs.ListQueued(ctx, f.st.Read(), []string{KindTranslate}, 100, 0)
	if err != nil || len(trQueued) != 4 {
		t.Fatalf("queued translate = %d (%v), want 4", len(trQueued), err)
	}

	protected := s.protectedWorkFileIDs(ctx)
	spWorkers := SpeechWorkers(ctx, s.Store)
	trWorkers := TranslatePoolWorkers(ctx, s.Store)
	wantProtected := min(2*spWorkers, 4) + min(2*trWorkers, 4)
	if len(protected) != wantProtected {
		t.Fatalf("protected len = %d, want %d (spW=%d trW=%d)", len(protected), wantProtected, spWorkers, trWorkers)
	}

	srQueued, _, err := jobs.ListQueued(ctx, f.st.Read(), []string{KindSearch}, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(srQueued) != len(allSeeded)-wantProtected {
		t.Fatalf("queued search = %d, want %d", len(srQueued), len(allSeeded)-wantProtected)
	}
	for _, j := range srQueued {
		fid := jobFileID(j)
		if protected[fid] {
			t.Fatalf("KindSearch was queued for protected file %d", fid)
		}
	}

	// Also verify POST /api/v1/subtitles/missing/search skips protected files.
	_ = f.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM jobs WHERE kind = ?", KindSearch)
		return err
	})
	m := http.NewServeMux()
	NewHandlers(f.st, v, s).Routes(m)
	code, body := call(m, "POST", "/api/v1/subtitles/missing/search", `{}`)
	if code != http.StatusAccepted {
		t.Fatalf("missing/search: %d %s", code, body)
	}
	var msResp struct {
		Queued  int     `json:"queued"`
		FileIDs []int64 `json:"file_ids"`
	}
	if err := json.Unmarshal([]byte(body), &msResp); err != nil {
		t.Fatal(err)
	}
	if msResp.Queued != len(allSeeded)-wantProtected {
		t.Fatalf("missing/search queued = %d, want %d", msResp.Queued, len(allSeeded)-wantProtected)
	}
	for _, fid := range msResp.FileIDs {
		if protected[fid] {
			t.Fatalf("missing/search queued protected file %d", fid)
		}
	}
}

func TestHandleSearch_SkipsProtectedUnlessByHandOrBetter(t *testing.T) {
	f := newFix(t)
	ctx := context.Background()
	t.Setenv("HOLYARRS_FLAGS", startup.Lab)
	site := fakesite.Lab()
	srv := httptest.NewServer(site)
	t.Cleanup(srv.Close)
	if _, err := AddSite(ctx, f.st, nil, SiteInput{
		Kind:              "fake",
		URL:               srv.URL,
		AllowLocalNetwork: true,
		Limits:            &SiteLimits{GapMS: 1, PerWindow: 1000, WindowSeconds: 3600},
	}, domain.System); err != nil {
		t.Fatal(err)
	}
	q := jobs.New(f.st, jobs.Options{Workers: 1, PollInterval: time.Hour})
	s := &Service{Store: f.st}
	s.Register(q)
	wishAll(t, f, Want{Language: "en"})

	// Place movieFile in the protected front-of-line by queuing KindTranscribe for it.
	if err := s.QueueTranscribe(ctx, f.movieFile, false); err != nil {
		t.Fatal(err)
	}

	// 1. Background KindSearch skips without contacting the site.
	bgRun := &jobs.Run{Job: jobs.Job{
		ID:      10,
		Kind:    KindSearch,
		Subject: &domain.Subject{Type: "file", ID: f.movieFile},
		Params:  map[string]any{},
	}}
	if err := s.handleSearch(ctx, bgRun); err != nil {
		t.Fatal(err)
	}
	if site.Searches != 0 {
		t.Fatalf("protected background search contacted site %d times", site.Searches)
	}

	// 2. Manual search (by_hand: true) still runs.
	handRun := &jobs.Run{Job: jobs.Job{
		ID:      11,
		Kind:    KindSearch,
		Subject: &domain.Subject{Type: "file", ID: f.movieFile},
		Params:  map[string]any{"by_hand": true},
	}}
	if err := s.handleSearch(ctx, handRun); err != nil {
		t.Fatal(err)
	}
	if site.Searches != 1 {
		t.Fatalf("by_hand search did not contact site: %d", site.Searches)
	}

	// 3. Better search (better: true) still runs even if another file is protected.
	if err := s.QueueTranscribe(ctx, f.ep1File, false); err != nil {
		t.Fatal(err)
	}
	betterRun := &jobs.Run{Job: jobs.Job{
		ID:      12,
		Kind:    KindSearch,
		Subject: &domain.Subject{Type: "file", ID: f.ep1File},
		Params:  map[string]any{"better": true, "better_days": float64(7)},
	}}
	if err := s.handleSearch(ctx, betterRun); err != nil {
		t.Fatal(err)
	}
	if site.Searches != 2 {
		t.Fatalf("better search did not contact site: %d", site.Searches)
	}
}

func TestWebsiteSaveDropsQueuedSpeechAndTranslate(t *testing.T) {
	f := newFix(t)
	ctx := context.Background()
	t.Setenv("HOLYARRS_FLAGS", startup.Lab)

	site := fakesite.Lab()
	srv := httptest.NewServer(site)
	t.Cleanup(srv.Close)
	if _, err := AddSite(ctx, f.st, nil, SiteInput{
		Kind:              "fake",
		URL:               srv.URL,
		AllowLocalNetwork: true,
		Limits:            &SiteLimits{GapMS: 1, PerWindow: 1000, WindowSeconds: 3600},
	}, domain.System); err != nil {
		t.Fatal(err)
	}

	q := jobs.New(f.st, jobs.Options{Workers: 1, PollInterval: time.Hour})
	s := &Service{Store: f.st}
	s.Register(q)
	wishAll(t, f, Want{Language: "en"})

	// Queue both a speech job and a translate job for movieFile.
	if err := s.QueueTranscribe(ctx, f.movieFile, false); err != nil {
		t.Fatal(err)
	}
	if err := s.QueueTranslate(ctx, f.movieFile, TranslateRequest{Language: "en", Forced: false}); err != nil {
		t.Fatal(err)
	}

	// Website search saves the human English subtitle for movieFile.
	res, err := s.Search(ctx, f.movieFile, SearchOptions{}, domain.System)
	if err != nil || len(res.Outcomes) != 1 || res.Outcomes[0].Result != "saved" {
		t.Fatalf("search outcomes = %+v, err = %v", res.Outcomes, err)
	}

	// Both queued KindTranscribe and KindTranslate must be dropped cleanly, leaving 0 cancelled/failed rows.
	sp, _, _ := jobs.ListQueued(ctx, f.st.Read(), []string{KindTranscribe}, 100, 0)
	tr, _, _ := jobs.ListQueued(ctx, f.st.Read(), []string{KindTranslate}, 100, 0)
	if len(sp) != 0 || len(tr) != 0 {
		t.Fatalf("expected queued jobs to be dropped: transcribe=%d translate=%d", len(sp), len(tr))
	}
	counts, err := jobs.CountByState(ctx, f.st.Read(), []string{KindTranscribe, KindTranslate}, "failed", "cancelled")
	if err != nil {
		t.Fatal(err)
	}
	if counts["failed"] != 0 || counts["cancelled"] != 0 {
		t.Fatalf("expected 0 failed/cancelled history rows, got %+v", counts)
	}
}

func TestSearchTranscribingSetsNextTryAtAndAdvancesDueFiles(t *testing.T) {
	f := newFix(t)
	ctx := context.Background()
	t.Setenv("HOLYARRS_FLAGS", startup.Lab)

	q := jobs.New(f.st, jobs.Options{Workers: 1, PollInterval: time.Hour})
	s := &Service{Store: f.st}
	s.Register(q)
	withTranscriber(t, f, s, "en,en,en", "eng")
	transcribeOn(t, f)
	wishAll(t, f, Want{Language: "en"})

	// Mark fixture files replaced so only our seeded files participate.
	for _, id := range []int64{f.movieFile, f.ep1File, f.ep2File} {
		_ = f.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec("UPDATE files SET state = 'replaced' WHERE id = ?", id)
			return err
		})
	}
	front1 := seedQueueMovieFile(t, f, "Front1")
	front2 := seedQueueMovieFile(t, f, "Front2")
	behind1 := seedQueueMovieFile(t, f, "Behind1")
	behind2 := seedQueueMovieFile(t, f, "Behind2")
	for _, id := range []int64{front1, front2, behind1, behind2} {
		if _, err := s.Look(ctx, id, domain.System); err != nil {
			t.Fatal(err)
		}
	}
	// Fill the 2*1 protected front-of-line in the speech queue.
	if err := s.QueueTranscribe(ctx, front1, false); err != nil {
		t.Fatal(err)
	}
	if err := s.QueueTranscribe(ctx, front2, false); err != nil {
		t.Fatal(err)
	}

	dueBefore, err := s.dueFiles(ctx, 1)
	if err != nil || len(dueBefore) != 1 || (dueBefore[0] != behind1 && dueBefore[0] != behind2) {
		t.Fatalf("dueBefore = %v (%v), want one of [%d, %d]", dueBefore, err, behind1, behind2)
	}
	firstDue := dueBefore[0]
	otherBehind := behind2
	if firstDue == behind2 {
		otherBehind = behind1
	}

	res, err := s.Search(ctx, firstDue, SearchOptions{}, domain.System)
	if err != nil || len(res.Outcomes) != 1 || res.Outcomes[0].Result != "transcribing" {
		t.Fatalf("Search(%d) = %+v, err = %v, want transcribing", firstDue, res.Outcomes, err)
	}

	var state, nextTry string
	var tries int
	if err := f.st.Read().QueryRowContext(ctx,
		"SELECT state, tries, COALESCE(next_try_at, '') FROM subtitles WHERE file_id = ? AND place = 'wish' AND language = 'en' AND forced = 0",
		firstDue).Scan(&state, &tries, &nextTry); err != nil {
		t.Fatal(err)
	}
	if state != "none_good_enough" || tries != 1 || nextTry == "" {
		t.Fatalf("wish row after transcribing: state=%q tries=%d next_try_at=%q", state, tries, nextTry)
	}
	parsedNext, err := time.Parse(database.TimeLayout, nextTry)
	if err != nil || !parsedNext.After(now()) {
		t.Fatalf("next_try_at %q is not in the future (now=%v, err=%v)", nextTry, now(), err)
	}

	dueAfter, err := s.dueFiles(ctx, 1)
	if err != nil || len(dueAfter) != 1 || dueAfter[0] != otherBehind {
		t.Fatalf("dueAfter = %v (%v), want [%d]", dueAfter, err, otherBehind)
	}
}

func TestSearchSaveUpdatesHavesForSubsequentTranslation(t *testing.T) {
	f := newFix(t)
	ctx := context.Background()
	t.Setenv("HOLYARRS_FLAGS", startup.Lab)

	site := &fakesite.Site{
		Echo: false,
		Subs: []fakesite.Sub{
			{
				ID:       "lantern-en",
				Kind:     "movie",
				Language: "en",
				Release:  "The.Lantern.2010.1080p.BluRay.x264-GRP",
				Title:    "The Lantern",
				Year:     2010,
				Trusted:  true,
				Format:   "srt",
				Text:     fakesite.Cue("Lantern subtitle line.", 1000),
			},
		},
	}
	srv := httptest.NewServer(site)
	t.Cleanup(srv.Close)
	if _, err := AddSite(ctx, f.st, nil, SiteInput{
		Kind:              "fake",
		URL:               srv.URL,
		AllowLocalNetwork: true,
		Limits:            &SiteLimits{GapMS: 1, PerWindow: 1000, WindowSeconds: 3600},
	}, domain.System); err != nil {
		t.Fatal(err)
	}

	q := jobs.New(f.st, jobs.Options{Workers: 1, PollInterval: time.Hour})
	v, err := secrets.Open(ctx, f.st, make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	ft := &faketranslator.OpenAI{Key: fakeAIKey}
	trSrv := httptest.NewServer(ft)
	t.Cleanup(trSrv.Close)

	s := &Service{Store: f.st, Vault: v}
	s.Register(q)
	if _, err := s.SaveTranslator(ctx, TranslatorInput{Kind: TranslatorOpenAI, URL: trSrv.URL + "/v1", Model: "lab-model", Key: fakeAIKey}, domain.System); err != nil {
		t.Fatal(err)
	}
	withTranscriber(t, f, s, "en,en,en", "eng")
	transcribeOn(t, f)

	wishAll(t, f, Want{Language: "en"}, Want{Language: "fr", TranslateFrom: "en"})

	// Pre-queue KindTranscribe for movieFile (as queueAllPendingWork does before Search finds en).
	if err := s.QueueTranscribe(ctx, f.movieFile, false); err != nil {
		t.Fatal(err)
	}

	res, err := s.Search(ctx, f.movieFile, SearchOptions{}, domain.System)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Outcomes) != 2 || res.Outcomes[0].Result != "saved" || res.Outcomes[1].Result != "translating" {
		t.Fatalf("outcomes = %+v, want [saved, translating]", res.Outcomes)
	}

	// KindTranscribe must have been dropped and NOT re-queued during the fr target.
	sp, _, err := jobs.ListQueued(ctx, f.st.Read(), []string{KindTranscribe}, 100, 0)
	if err != nil || len(sp) != 0 {
		t.Fatalf("queued transcribe = %d (%v), want 0", len(sp), err)
	}
	tr, _, err := jobs.ListQueued(ctx, f.st.Read(), []string{KindTranslate}, 100, 0)
	if err != nil || len(tr) != 1 {
		t.Fatalf("queued translate = %d (%v), want 1", len(tr), err)
	}
	if gotSrc, _ := tr[0].Params["source"].(string); gotSrc != SourceBeside {
		t.Fatalf("translate source = %q, want %q", gotSrc, SourceBeside)
	}
	if gotLang, _ := tr[0].Params["language"].(string); gotLang != "fr" {
		t.Fatalf("translate language = %q, want fr", gotLang)
	}
}

func TestHandleLook_QueuesPendingWorkEvenWhenNoStaleFiles(t *testing.T) {
	f := newFix(t)
	ctx := context.Background()
	t.Setenv("HOLYARRS_FLAGS", startup.Lab)

	q := jobs.New(f.st, jobs.Options{Workers: 1, PollInterval: time.Hour})
	s := &Service{Store: f.st}
	s.Register(q)
	withTranscriber(t, f, s, "en,en,en", "eng")
	wishAll(t, f, Want{Language: "en"})

	// Look all files while TranscribeMissing is still off, so staleFiles returns 0 files later.
	for _, id := range []int64{f.movieFile, f.ep1File, f.ep2File} {
		if _, err := s.Look(ctx, id, domain.System); err != nil {
			t.Fatal(err)
		}
	}
	spBefore, _, _ := jobs.ListQueued(ctx, f.st.Read(), []string{KindTranscribe}, 100, 0)
	if len(spBefore) != 0 {
		t.Fatalf("transcribe queued before enabling setting = %d, want 0", len(spBefore))
	}

	// Enable TranscribeMissing and run handleLook (0 stale files).
	transcribeOn(t, f)
	run := &jobs.Run{Job: jobs.Job{ID: 1, Kind: KindLook}}
	if err := s.handleLook(ctx, run); err != nil {
		t.Fatal(err)
	}

	spAfter, _, err := jobs.ListQueued(ctx, f.st.Read(), []string{KindTranscribe}, 100, 0)
	if err != nil || len(spAfter) != 3 {
		t.Fatalf("transcribe queued after handleLook with 0 stale files = %d (%v), want 3", len(spAfter), err)
	}
}
