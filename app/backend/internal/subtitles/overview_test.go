package subtitles

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/container"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/faketranslator"
)

// subRow adds one subtitle row to a File.
func subRow(t *testing.T, f *fix, fileID int64, language, place, state, codec, rel string) {
	t.Helper()
	ts := database.FormatTime(now())
	err := f.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO subtitles (created_at, created_by, updated_at, updated_by, file_id, language, place, codec, relative_path, track_index, origin, state)
			VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), CASE ? WHEN 'built_in' THEN 0 END, 'found', ?)`,
			ts, sysActor, ts, sysActor, fileID, language, place, codec, rel, place, state)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// overviewSeed: the movie wants French and has nothing to read; episode 1
// wants French and has an English text track inside; episode 2 wants French
// and has only a picture track.
func overviewSeed(t *testing.T, f *fix) {
	t.Helper()
	subRow(t, f, f.movieFile, "fr", "wish", "wanted", "", "")
	subRow(t, f, f.ep1File, "fr", "wish", "wanted", "", "")
	subRow(t, f, f.ep1File, "en", "built_in", "present", "S_TEXT/UTF8", "")
	subRow(t, f, f.ep2File, "fr", "wish", "none_good_enough", "", "")
	subRow(t, f, f.ep2File, "en", "built_in", "present", "S_HDMV/PGS", "")
}

// The missing list says for each row whether the File has a subtitle that
// can be read, and can be narrowed to the Files that need speech to text or
// to those that have another language to translate.
func TestMissingListSaysWhatEachFileNeeds(t *testing.T) {
	f := newFix(t)
	overviewSeed(t, f)
	ctx, db := context.Background(), f.st.Read()
	all, n, err := MissingList(ctx, db, MissingFilter{}, 100, 0)
	if err != nil || n != 3 || len(all) != 3 {
		t.Fatalf("all: %d %v", n, err)
	}
	for _, r := range all {
		if r.HasText != (r.FileID == f.ep1File) {
			t.Fatalf("has_text of file %d is %v", r.FileID, r.HasText)
		}
	}
	speech, n, _ := MissingList(ctx, db, MissingFilter{Need: NeedSpeech}, 100, 0)
	if n != 2 || len(speech) != 2 || speech[0].FileID == f.ep1File || speech[1].FileID == f.ep1File {
		t.Fatalf("speech: %d %+v", n, speech)
	}
	text, n, _ := MissingList(ctx, db, MissingFilter{Need: NeedText}, 100, 0)
	if n != 1 || len(text) != 1 || text[0].FileID != f.ep1File {
		t.Fatalf("text: %d %+v", n, text)
	}
}

// A File whose transcript failed says so on its row.
func TestMissingListShowsAFailedTranscript(t *testing.T) {
	f := newFix(t)
	overviewSeed(t, f)
	s := &Service{Store: f.st}
	s.noteTranscript(context.Background(), f.movieFile, true, "no speech was found")
	rows, _, err := MissingList(context.Background(), f.st.Read(), MissingFilter{TitleID: f.movie}, 100, 0)
	if err != nil || len(rows) != 1 || rows[0].Speech != "final" || rows[0].SpeechReason != "no speech was found" {
		t.Fatalf("%+v %v", rows, err)
	}
}

// The SQL's idea of a subtitle that can be read is the code's.
func TestTextHereAgreesWithTheCode(t *testing.T) {
	for _, c := range []string{"S_TEXT/UTF8", "S_TEXT/ASCII", "S_TEXT/ASS", "S_TEXT/SSA", "S_ASS", "S_SSA", "S_TEXT/WEBVTT", "S_HDMV/PGS", "S_VOBSUB", "S_DVBSUB", "tx3g", ""} {
		if container.TextCodec(c) != (c != "" && strings.Contains(textHere, "'"+strings.ToUpper(c)+"'")) {
			t.Errorf("codec %q: the SQL and TextCodec disagree", c)
		}
	}
	for _, e := range []string{"srt", "vtt", "ass", "ssa", "sub", "idx", "sup"} {
		if (textFormat("a."+e) != "") != strings.Contains(textHere, "'%."+e+"'") {
			t.Errorf("ending %q: the SQL and textFormat disagree", e)
		}
	}
}

// route: GET /api/v1/subtitles/overview
func TestRoutes_Overview(t *testing.T) {
	f := newFix(t)
	overviewSeed(t, f)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	s.noteTranscript(context.Background(), f.ep2File, true, "no speech was found")
	// One job running on the movie, one waiting, and one of another part.
	ts := database.FormatTime(now())
	err := f.st.Write(context.Background(), func(tx *sql.Tx) error {
		for _, j := range [][]any{
			{KindTranscribe, `{"type":"file","id":` + itoa(f.movieFile) + `}`, "running", 0.4},
			{KindTranslate, `{"type":"file","id":` + itoa(f.ep1File) + `}`, "queued", 0.0},
			{"library_scan", nil, "running", 0.2},
		} {
			if _, err := tx.Exec(`INSERT INTO jobs (created_at, created_by, updated_at, updated_by, kind, subject, params, state, progress, run_after, asked_by)
				VALUES (?, ?, ?, ?, ?, ?, '{"language":"fr"}', ?, ?, ?, ?)`, ts, sysActor, ts, sysActor, j[0], j[1], j[2], j[3], ts, sysActor); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	o, err := s.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if o.Missing != 3 || o.Files != 3 || o.NeedSpeech != 2 || o.CanTranslate != 1 || o.SpeechHeld != 1 {
		t.Fatalf("counts %+v", o)
	}
	if o.Library.Wanted < o.Library.Missing || o.Library.FilesTotal < o.Library.FilesMissing {
		t.Fatalf("library floor violated: %+v", o.Library)
	}
	if o.Stats.Speech.Unit != "x" || o.Stats.Speech.Samples != 0 || o.Stats.Speech.AvgSpeed != 0 || o.Stats.Speech.LiveSpeed != 0 ||
		o.Stats.Translate.Unit != "lines/s" || o.Stats.Translate.Samples != 0 || o.Stats.Translate.AvgSpeed != 0 || o.Stats.Translate.LiveSpeed != 0 {
		t.Fatalf("empty stats defaults = %+v", o.Stats)
	}
	if len(o.Jobs) != 2 || o.Jobs[0].Kind != KindTranscribe || o.Jobs[0].State != "running" || o.Jobs[0].Progress != 0.4 || o.Jobs[0].Name != "The Lantern" || o.Jobs[0].TitleID != f.movie {
		t.Fatalf("jobs %+v", o.Jobs)
	}
	if o.Jobs[1].Name != "Quiet Harbour" || o.Jobs[1].Season == nil || *o.Jobs[1].Season != 1 || *o.Jobs[1].Episode != 2 || o.Jobs[1].Language != "fr" {
		t.Fatalf("waiting job %+v", o.Jobs[1])
	}
	if o.Running[KindTranscribe] != 1 || o.Queued[KindTranslate] != 1 || len(o.Running)+len(o.Queued) != 2 {
		t.Fatalf("running %v queued %v", o.Running, o.Queued)
	}
	m := http.NewServeMux()
	NewHandlers(f.st, s.Vault, s).Routes(m)
	code, body := call(m, "GET", "/api/v1/subtitles/overview", "")
	if code != 200 || !strings.Contains(body, `"need_speech":2`) || !strings.Contains(body, `"translator_ready":true`) || !strings.Contains(body, `"jobs":[`) {
		t.Fatalf("overview: %d %s", code, body)
	}
	code, body = call(m, "GET", "/api/v1/subtitles/missing?need=speech", "")
	if code != 200 || !strings.Contains(body, `"total":2`) {
		t.Fatalf("missing by need: %d %s", code, body)
	}
	if code, body := call(m, "GET", "/api/v1/subtitles/missing?need=nonsense", ""); code != 200 || !strings.Contains(body, `"total":3`) {
		t.Fatalf("a need that is not known: %d %s", code, body)
	}
}

// Overview computes library coverage, speech/translate speeds (historical +
// live), failed/cancelled counts, and the integrity summary.
func TestOverview_LibraryStatsFailedAndIntegrity(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	ctx := context.Background()

	// Configure default wishes for English and French.
	set := ReadSettings(ctx, f.st.Read())
	set.Languages = []Want{{Language: "en"}, {Language: "fr"}}
	if err := SaveSettings(ctx, f.st, set, domain.System); err != nil {
		t.Fatal(err)
	}

	// Write beside subtitle files for movie (en only) and ep1 (en + fr), plus one on ep2 (unlooked).
	writeSub := func(videoRel, suffix string) {
		t.Helper()
		base := strings.TrimSuffix(videoRel, filepath.Ext(videoRel))
		p := filepath.Join(f.lib, filepath.FromSlash(base+suffix))
		if err := os.WriteFile(p, []byte("1\n00:00:01,000 --> 00:00:02,000\nhello\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeSub("The Lantern (2010)/The Lantern (2010) 1080p BluRay.mkv", ".en.srt")
	writeSub("Quiet Harbour (2019)/Season 01/Quiet Harbour - S01E02.mkv", ".en.srt")
	writeSub("Quiet Harbour (2019)/Season 01/Quiet Harbour - S01E02.mkv", ".fr.srt")

	if _, err := s.Look(ctx, f.movieFile, domain.System); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Look(ctx, f.ep1File, domain.System); err != nil {
		t.Fatal(err)
	}
	// ep2 remains unlooked, with one unchecked beside row.
	subRow(t, f, f.ep2File, "en", "beside", "present", "", "Quiet Harbour (2019)/Season 01/Quiet Harbour - S01E03.en.srt")

	// Set integrity states on the three looked beside rows.
	ts := database.FormatTime(now())
	var liveTranscribeID, liveTranslateID int64
	err := f.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE subtitles SET integrity_state = 'ok', integrity_checked_at = ? WHERE file_id = ? AND place = 'beside' AND language = 'en'`, ts, f.movieFile); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE subtitles SET integrity_state = 'bad', integrity_issue = 'truncated', integrity_checked_at = ? WHERE file_id = ? AND place = 'beside' AND language = 'en'`, ts, f.ep1File); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE subtitles SET integrity_state = 'accepted', integrity_checked_at = ? WHERE file_id = ? AND place = 'beside' AND language = 'fr'`, ts, f.ep1File); err != nil {
			return err
		}

		insJob := func(kind, state, resJSON string) int64 {
			var resArg any
			if resJSON != "" {
				resArg = resJSON
			}
			r, err := tx.Exec(`INSERT INTO jobs (created_at, created_by, updated_at, updated_by, kind, params, state, result, run_after, ended_at, asked_by)
				VALUES (?, ?, ?, ?, ?, '{}', ?, ?, ?, ?, ?)`, ts, sysActor, ts, sysActor, kind, state, resArg, ts, ts, sysActor)
			if err != nil {
				t.Fatal(err)
			}
			id, _ := r.LastInsertId()
			return id
		}
		// Historical completed jobs with result payloads.
		insJob(KindTranscribe, "done", `{"audio_seconds":120,"elapsed_seconds":30,"lines":45}`)
		insJob(KindTranscribe, "done", `{"audio_seconds":60,"elapsed_seconds":10,"lines":15}`)
		insJob(KindTranslate, "done", `{"translated":100,"elapsed_seconds":20}`)
		// Live running jobs for live speed totals.
		liveTranscribeID = insJob(KindTranscribe, "running", "")
		liveTranslateID = insJob(KindTranslate, "running", "")
		// Failed and cancelled subtitle jobs, plus one non-subtitle failed job.
		insJob(KindSearch, "failed", "")
		insJob(KindTranscribe, "failed", "")
		insJob(KindTranslate, "cancelled", "")
		insJob("library_scan", "failed", "")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s.setLiveStat(liveTranscribeID, KindTranscribe, 2.0, "x")
	s.setLiveStat(liveTranslateID, KindTranslate, 3.0, "lines/s")

	o, err := s.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if o.Library.Wanted != 4 || o.Library.Have != 3 || o.Library.Missing != 1 || o.Library.Percent != 75.0 ||
		o.Library.FilesTotal != 3 || o.Library.FilesDone != 1 || o.Library.FilesMissing != 1 || o.Library.UnlookedFiles != 1 {
		t.Fatalf("library progress = %+v", o.Library)
	}
	if o.Stats.Speech.AvgSpeed != 4.5 || o.Stats.Speech.LiveSpeed != 2.0 || o.Stats.Speech.Unit != "x" ||
		o.Stats.Speech.Samples != 2 || o.Stats.Speech.TotalLines != 60 || o.Stats.Speech.TotalAudioSeconds != 180.0 {
		t.Fatalf("speech stats = %+v", o.Stats.Speech)
	}
	if o.Stats.Translate.AvgSpeed != 5.0 || o.Stats.Translate.LiveSpeed != 3.0 || o.Stats.Translate.Unit != "lines/s" ||
		o.Stats.Translate.Samples != 1 || o.Stats.Translate.TotalLines != 100 {
		t.Fatalf("translate stats = %+v", o.Stats.Translate)
	}
	if o.FailedCount != 2 || o.CancelledCount != 1 {
		t.Fatalf("failed=%d cancelled=%d", o.FailedCount, o.CancelledCount)
	}
	if o.Integrity.OK != 1 || o.Integrity.Bad != 1 || o.Integrity.Accepted != 1 || o.Integrity.Unchecked != 1 || o.Integrity.LastCheckedAt == nil {
		t.Fatalf("integrity = %+v", o.Integrity)
	}

	m := http.NewServeMux()
	NewHandlers(f.st, s.Vault, s).Routes(m)
	code, body := call(m, "GET", "/api/v1/subtitles/overview", "")
	if code != http.StatusOK {
		t.Fatalf("GET overview: %d %s", code, body)
	}
	var got Overview
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal overview: %v", err)
	}
	if got.Library.Percent != 75.0 || got.FailedCount != 2 || got.CancelledCount != 1 || got.Integrity.Bad != 1 ||
		got.Stats.Speech.AvgSpeed != 4.5 || got.Stats.Speech.LiveSpeed != 2.0 || got.Stats.Speech.Samples != 2 ||
		got.Stats.Speech.TotalLines != 60 || got.Stats.Speech.TotalAudioSeconds != 180.0 ||
		got.Stats.Translate.AvgSpeed != 5.0 || got.Stats.Translate.LiveSpeed != 3.0 || got.Stats.Translate.Samples != 1 ||
		got.Stats.Translate.TotalLines != 100 {
		t.Fatalf("decoded overview mismatch: %+v", got)
	}
}

// Concurrent running jobs sum their speeds into LiveSpeed while AvgSpeed stays the historical average.
func TestOverview_LiveSpeedSumsConcurrentJobs(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	ctx := context.Background()

	ts := database.FormatTime(now())
	if err := f.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO jobs (created_at, created_by, updated_at, updated_by, kind, params, state, result, run_after, ended_at, asked_by)
			VALUES (?, ?, ?, ?, ?, '{}', 'done', '{"audio_seconds":60,"elapsed_seconds":10,"lines":20}', ?, ?, ?)`,
			ts, sysActor, ts, sysActor, KindTranscribe, ts, ts, sysActor)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	for id := int64(1); id <= 4; id++ {
		s.setLiveStat(id, KindTranscribe, 16.0, "x")
	}
	o, err := s.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if o.Stats.Speech.LiveSpeed != 64.0 || o.Stats.Speech.AvgSpeed != 6.0 {
		t.Fatalf("4x16.0 live speed = %+v", o.Stats.Speech)
	}

	for id := int64(1); id <= 4; id++ {
		s.clearLiveStat(id)
	}
	s.setLiveStat(10, KindTranscribe, 15.5, "x")
	s.setLiveStat(11, KindTranscribe, 16.5, "x")
	o, err = s.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if o.Stats.Speech.LiveSpeed != 32.0 || o.Stats.Speech.AvgSpeed != 6.0 {
		t.Fatalf("15.5+16.5 live speed = %+v", o.Stats.Speech)
	}
}
