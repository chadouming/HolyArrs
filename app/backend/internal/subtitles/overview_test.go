package subtitles

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
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
