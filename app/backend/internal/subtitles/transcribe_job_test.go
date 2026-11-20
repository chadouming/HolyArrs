package subtitles

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/sites/fakesite"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/transcribe"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/builtin"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/faketranslator"
)

var (
	sttFakesOnce sync.Once
	sttFakesDir  string
)

// withTranscriber gives s the stand-ins for whisper-cli, ffmpeg and ffprobe,
// a speech model and a voice-detection model in its models folder, and a
// movie file with no subtitle at all. langs are what the language checks
// answer, tracks the audio tracks' tags.
func withTranscriber(t *testing.T, f *fix, s *Service, langs, tracks string) {
	t.Helper()
	sttFakesOnce.Do(func() {
		dir, err := os.MkdirTemp("", "stt-fakes")
		if err != nil {
			t.Fatal(err)
		}
		sttFakesDir = dir
		for out, pkg := range map[string]string{"whisper-cli": "./transcribe/fakes/whisper", "ffmpeg": "./transcribe/fakes/ffmpeg", "ffprobe": "./transcribe/fakes/ffmpeg"} {
			if b, err := exec.Command("go", "build", "-o", filepath.Join(dir, out), pkg).CombinedOutput(); err != nil {
				t.Fatalf("building %s: %v\n%s", pkg, err, b)
			}
		}
	})
	t.Setenv(transcribe.EnvWhisper, filepath.Join(sttFakesDir, "whisper-cli"))
	t.Setenv(transcribe.EnvFFmpeg, filepath.Join(sttFakesDir, "ffmpeg"))
	t.Setenv(transcribe.EnvFFprobe, filepath.Join(sttFakesDir, "ffprobe"))
	t.Setenv("FAKE_LANGS", langs)
	t.Setenv("FAKE_TRACKS", tracks)
	t.Setenv("FAKE_STATE", t.TempDir())
	t.Setenv("FAKE_SECONDS", "60") // the stand-in's six lines cover a short file
	if s.ModelsDir == "" {
		withBuiltIn(t, s)
	}
	old := builtin.Catalogue
	builtin.Catalogue = append([]builtin.Model{
		{ID: "lab-speech", Name: "Lab Speech", Kind: builtin.KindSpeech, Size: 12},
		{ID: "lab-vad", Name: "Lab VAD", Kind: builtin.KindVAD, Size: 9},
	}, old...)
	t.Cleanup(func() { builtin.Catalogue = old })
	for name, body := range map[string]string{"lab-speech.gguf": "speech model", "lab-vad.gguf": "vad model"} {
		if err := os.WriteFile(filepath.Join(s.ModelsDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(filepath.Join("container", "testdata", "lab-none.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(f.abs(movieRel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.abs(movieRel), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func transcribeOn(t *testing.T, f *fix) {
	t.Helper()
	set := ReadSettings(context.Background(), f.st.Read())
	set.TranscribeMissing = true
	if err := SaveSettings(context.Background(), f.st, set, domain.System); err != nil {
		t.Fatal(err)
	}
}

func besideRows(t *testing.T, f *fix, fileID int64) []Subtitle {
	t.Helper()
	rows, _ := ListFor(context.Background(), f.st.Read(), fileID)
	var out []Subtitle
	for _, r := range rows {
		if r.Place == "beside" && r.State == "present" {
			out = append(out, r)
		}
	}
	return out
}

func TestTranscribeSavesASubtitleMarkedTranscribed(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	withTranscriber(t, f, s, "en,en,en", "eng")
	ctx := context.Background()
	wishAll(t, f, Want{Language: "en"})
	if tr := s.Transcriber(ctx); !tr.Available || !tr.Ready || tr.Model != "Lab Speech" {
		t.Fatalf("transcriber %+v", tr)
	}
	res, err := s.Transcribe(ctx, f.movieFile, true, domain.System)
	if err != nil || res.Skipped != "" || res.Language != "en" || res.Lines != 6 || res.Path != "The Lantern (2010)/The Lantern (2010) 1080p BluRay.en.srt" {
		t.Fatalf("%+v %v", res, err)
	}
	cues := readCues(t, f.abs(res.Path))
	if len(cues) != 6 || cues[0].Text != "Line 1 of the fake transcript." {
		t.Fatalf("cues %+v", cues)
	}
	rows := besideRows(t, f, f.movieFile)
	if len(rows) != 1 || !rows[0].Transcribed || rows[0].Made == nil || rows[0].Language != "en" || rows[0].SiteName == nil || *rows[0].SiteName != "Lab Speech" || rows[0].Origin != "fetched" {
		t.Fatalf("row %+v", rows)
	}
	if len(eventsOf(t, f.st, EventTranscribed)) != 1 {
		t.Fatal("no transcribed event")
	}
	// Now the file has a text subtitle: a second transcript is refused.
	if _, err := s.Transcribe(ctx, f.movieFile, true, domain.System); !errors.Is(err, ErrHasText) {
		t.Fatalf("second transcript: %v", err)
	}
}

func TestTranscribeRefusesAFileWithATextSubtitle(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	withTranscriber(t, f, s, "en,en,en", "eng")
	labMovie(t, f) // an English text track inside the video
	wishAll(t, f, Want{Language: "fr"})
	if _, err := s.Transcribe(context.Background(), f.movieFile, true, domain.System); !errors.Is(err, ErrHasText) {
		t.Fatalf("err %v", err)
	}
}

func TestTranscribeLabelsTheSpokenLanguageNotTheTag(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	withTranscriber(t, f, s, "en,en,en", "fra,eng")
	wishAll(t, f, Want{Language: "fr"}, Want{Language: "en"})
	res, err := s.Transcribe(context.Background(), f.movieFile, true, domain.System)
	// French is wanted first, so the track tagged French is taken; what is spoken on it is English.
	if err != nil || res.Language != "en" || !strings.HasSuffix(res.Path, ".en.srt") {
		t.Fatalf("%+v %v", res, err)
	}
	if rows := besideRows(t, f, f.movieFile); len(rows) != 1 || rows[0].Language != "en" {
		t.Fatalf("rows %+v", rows)
	}
}

func runTranscribeJob(t *testing.T, s *Service, fileID int64) error {
	t.Helper()
	return s.handleTranscribe(context.Background(), &jobs.Run{Job: jobs.Job{Subject: &domain.Subject{Type: "file", ID: fileID}, Params: map[string]any{}}})
}

func queuedKinds(q *[]queued, kind string) int {
	n := 0
	for _, x := range *q {
		if x.kind == kind {
			n++
		}
	}
	return n
}

func TestUnclearLanguageSavesNothingAndIsNotRetried(t *testing.T) {
	f := newFix(t)
	s, q := withTranslator(t, f, &faketranslator.OpenAI{})
	withTranscriber(t, f, s, "en,fr,de", "-")
	transcribeOn(t, f)
	ctx := context.Background()
	wishAll(t, f, Want{Language: "en"})
	if err := runTranscribeJob(t, s, f.movieFile); err != nil {
		t.Fatalf("a final outcome is not a failed job: %v", err)
	}
	if rows := besideRows(t, f, f.movieFile); len(rows) != 0 {
		t.Fatalf("something was saved: %+v", rows)
	}
	if _, err := s.Search(ctx, f.movieFile, SearchOptions{}, domain.System); err != nil {
		t.Fatal(err)
	}
	if n := queuedKinds(q, KindTranscribe); n != 0 {
		t.Fatalf("queued again %d times after an outcome that will not change", n)
	}
	// By hand it may be asked again.
	if err := s.QueueTranscribe(ctx, f.movieFile, true); err != nil || queuedKinds(q, KindTranscribe) != 1 {
		t.Fatalf("by hand: %v, %d queued", err, queuedKinds(q, KindTranscribe))
	}
}

func TestEmptyTranscriptSavesNothingAndIsNotRetried(t *testing.T) {
	f := newFix(t)
	s, q := withTranslator(t, f, &faketranslator.OpenAI{})
	withTranscriber(t, f, s, "en,en,en", "eng")
	transcribeOn(t, f)
	t.Setenv("FAKE_EMPTY", "1")
	wishAll(t, f, Want{Language: "en"})
	if err := runTranscribeJob(t, s, f.movieFile); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Search(context.Background(), f.movieFile, SearchOptions{}, domain.System); err != nil {
		t.Fatal(err)
	}
	if len(besideRows(t, f, f.movieFile)) != 0 || queuedKinds(q, KindTranscribe) != 0 {
		t.Fatalf("saved %d, queued %d", len(besideRows(t, f, f.movieFile)), queuedKinds(q, KindTranscribe))
	}
}

func TestCrashIsRetriedAfterADayThreeTimes(t *testing.T) {
	f := newFix(t)
	s, q := withTranslator(t, f, &faketranslator.OpenAI{})
	withTranscriber(t, f, s, "en,en,en", "eng")
	transcribeOn(t, f)
	t.Setenv("FAKE_EXIT", "1")
	ctx := context.Background()
	wishAll(t, f, Want{Language: "en"})
	clock := time.Now()
	old := now
	now = func() time.Time { return clock }
	defer func() { now = old }()
	for try := 1; try <= transcribeTries; try++ {
		if err := runTranscribeJob(t, s, f.movieFile); err == nil {
			t.Fatalf("try %d: a crash should fail the job", try)
		}
		// Held for a day: a search now does not queue.
		before := queuedKinds(q, KindTranscribe)
		if _, err := s.Search(ctx, f.movieFile, SearchOptions{}, domain.System); err != nil {
			t.Fatal(err)
		}
		if queuedKinds(q, KindTranscribe) != before {
			t.Fatalf("try %d: queued again inside the hold", try)
		}
		clock = clock.Add(25 * time.Hour)
		if _, err := s.Search(ctx, f.movieFile, SearchOptions{}, domain.System); err != nil {
			t.Fatal(err)
		}
		want := before + 1
		if try == transcribeTries {
			want = before // three tries are all it gets
		}
		if got := queuedKinds(q, KindTranscribe); got != want {
			t.Fatalf("try %d: %d queued after a day, want %d", try, got, want)
		}
	}
}

func TestSearchQueuesATranscriptWhenSitesGiveNothing(t *testing.T) {
	f := newFix(t)
	s, q := withTranslator(t, f, &faketranslator.OpenAI{})
	withTranscriber(t, f, s, "en,en,en", "eng")
	ctx := context.Background()
	wishAll(t, f, Want{Language: "en"}, Want{Language: "fr", TranslateFrom: "en"})
	// Switch off: nothing.
	if _, err := s.Search(ctx, f.movieFile, SearchOptions{}, domain.System); err != nil || queuedKinds(q, KindTranscribe) != 0 {
		t.Fatalf("switch off: %v, %d queued", err, queuedKinds(q, KindTranscribe))
	}
	transcribeOn(t, f)
	// No site is set up at all: the transcript does not wait for one.
	res, err := s.Search(ctx, f.movieFile, SearchOptions{}, domain.System)
	if err != nil || queuedKinds(q, KindTranscribe) != 1 {
		t.Fatalf("switch on: %v, %d queued, outcomes %+v", err, queuedKinds(q, KindTranscribe), res.Outcomes)
	}
	said := false
	for _, o := range res.Outcomes {
		said = said || o.Result == "transcribing"
	}
	if !said {
		t.Fatalf("no outcome says transcribing: %+v", res.Outcomes)
	}
	// Models gone: not ready, not queued.
	_ = os.Remove(filepath.Join(s.ModelsDir, "lab-vad.gguf"))
	*q = nil
	if _, err := s.Search(ctx, f.movieFile, SearchOptions{}, domain.System); err != nil || queuedKinds(q, KindTranscribe) != 0 {
		t.Fatalf("not ready: %v, %d queued", err, queuedKinds(q, KindTranscribe))
	}
	if s.Transcriber(ctx).Ready {
		t.Fatal("ready without its voice-detection model")
	}
}

func TestTranslationFromATranscriptFollowsTranslateFrom(t *testing.T) {
	for _, c := range []struct {
		from string
		want int
	}{{FromAny, 1}, {"ja", 1}, {"en", 0}, {"", 0}} {
		f := newFix(t)
		s, q := withTranslator(t, f, &faketranslator.OpenAI{})
		withTranscriber(t, f, s, "ja,ja,ja", "jpn")
		ctx := context.Background()
		wishAll(t, f, Want{Language: "fr", TranslateFrom: c.from})
		res, err := s.Transcribe(ctx, f.movieFile, true, domain.System)
		if err != nil || res.Language != "ja" {
			t.Fatalf("from %q: %+v %v", c.from, res, err)
		}
		// The transcript is kept whatever the translator may do with it.
		if rows := besideRows(t, f, f.movieFile); len(rows) != 1 || rows[0].Language != "ja" {
			t.Fatalf("from %q: rows %+v", c.from, rows)
		}
		if _, err := s.Search(ctx, f.movieFile, SearchOptions{}, domain.System); err != nil {
			t.Fatal(err)
		}
		if got := queuedKinds(q, KindTranslate); got != c.want {
			t.Fatalf("from %q: %d translations queued, want %d", c.from, got, c.want)
		}
	}
}

func TestSpeechModelsAreNotTranslationModels(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	withTranscriber(t, f, s, "en,en,en", "eng")
	ctx := context.Background()
	if _, err := s.SaveTranslator(ctx, TranslatorInput{Kind: TranslatorBuiltIn, BuiltInModel: "lab-speech"}, domain.System); err == nil {
		t.Fatal("a speech model was accepted as the translator's model")
	}
	kinds := map[string]string{}
	for _, v := range s.Models(ctx) {
		kinds[v.ID] = v.Kind
	}
	if kinds["lab-speech"] != builtin.KindSpeech || kinds["lab-vad"] != builtin.KindVAD || kinds["file:lab.gguf"] != "" {
		t.Fatalf("kinds %v", kinds)
	}
	for _, id := range []string{"whisper-large-v3-turbo", "silero-vad-6.2.0"} {
		if m, ok := builtin.ByID(id); !ok || m.Kind == "" || len(m.SHA256) != 64 || !strings.HasPrefix(m.URL, "https://huggingface.co/") {
			t.Fatalf("catalogue entry %s: %+v %v", id, m, ok)
		}
	}
}

func TestMigrationAddsTranscribedAndTheHoldTable(t *testing.T) {
	f := newFix(t)
	ts := database.FormatTime(time.Now())
	subPinExec(t, f.st, `INSERT INTO subtitles (created_at, created_by, updated_at, updated_by, file_id, language, place, relative_path, origin, state, made, transcribed)
		VALUES (?, ?, ?, ?, ?, 'en', 'beside', 'x.en.srt', 'fetched', 'present', 'ai_translated', 1)`, ts, sysActor, ts, sysActor, f.movieFile)
	subPinExec(t, f.st, `INSERT INTO subtitle_transcripts (file_id, state, reason, tries, updated_at) VALUES (?, 'final', 'no speech was found', 1, ?)`, f.movieFile, ts)
	var n int
	if err := f.st.Read().QueryRow("SELECT count(*) FROM subtitles WHERE transcribed = 0").Scan(&n); err != nil {
		t.Fatal(err)
	}
}

// A transcript is a stand-in: when a site has a subtitle a person made, the
// search saves that one and the transcript goes. Making the transcript itself
// asked the site for nothing.
func TestPersonsSubtitleReplacesATranscript(t *testing.T) {
	f := newFix(t)
	ctx := context.Background()
	osf := &fakesite.OpenSubs{Key: mkKey, User: mkUser, Pass: mkPass, Quota: 10, Subs: []fakesite.Sub{
		{ID: "21", Kind: "movie", Language: "en", Title: "The Lantern", Year: 2010, ByID: true, Trusted: true,
			Release: "The.Lantern.2010.1080p.BluRay.x264-GRP", Text: fakesite.Cue("A line a person wrote.", 1000)},
	}}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		osf.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	withTranscriber(t, f, s, "en,en,en", "eng")
	wishAll(t, f, Want{Language: "en"})
	if res, err := s.Transcribe(ctx, f.movieFile, true, domain.System); err != nil || res.Language != "en" {
		t.Fatalf("%+v %v", res, err)
	}
	if calls != 0 {
		t.Fatalf("making a transcript called a site %d times", calls)
	}
	in := SiteInput{Kind: "opensubtitles", URL: srv.URL + "/api/v1", AllowLocalNetwork: true, Limits: &SiteLimits{GapMS: 1, PerWindow: 1000, WindowSeconds: 3600}}
	in.APIKey, in.Username, in.Password = mkKey, mkUser, mkPass
	if _, err := AddSite(ctx, f.st, s.Vault, in, domain.System); err != nil {
		t.Fatal(err)
	}
	// The weekly look for something better than a machine-made subtitle.
	res, err := s.Search(ctx, f.movieFile, SearchOptions{Better: true, BetterDays: 1}, domain.System)
	if err != nil {
		t.Fatal(err)
	}
	rows := besideRows(t, f, f.movieFile)
	if len(rows) != 1 || rows[0].Transcribed || rows[0].Made != nil {
		t.Fatalf("after the search: %+v, outcomes %+v", rows, res.Outcomes)
	}
	if cues := readCues(t, f.abs(*rows[0].RelativePath)); len(cues) != 1 || cues[0].Text != "A line a person wrote." {
		t.Fatalf("the saved file is not the person's: %+v", cues)
	}
}

// A transcript in a language nobody wants has no search of its own, but the
// weekly look for something better must still move on past it, or its File
// would be looked at on every run.
func TestWeeklyLookMovesPastATranscriptNobodyWants(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	withTranscriber(t, f, s, "en,en,en", "eng")
	ctx := context.Background()
	wishAll(t, f, Want{Language: "fr"}) // French wanted; English is what is spoken
	if res, err := s.Transcribe(ctx, f.movieFile, true, domain.System); err != nil || res.Language != "en" {
		t.Fatalf("%+v %v", res, err)
	}
	old := database.FormatTime(time.Now().AddDate(0, 0, -30))
	subPinExec(t, f.st, "UPDATE subtitles SET saved_at = ?, last_tried_at = NULL WHERE file_id = ? AND made IS NOT NULL", old, f.movieFile)
	due, err := stopgapsDue(ctx, f.st.Read(), 10)
	if err != nil || len(due) != 1 || due[0] != f.movieFile {
		t.Fatalf("before the look: %v %v", due, err)
	}
	if _, err := s.Search(ctx, f.movieFile, SearchOptions{Better: true, BetterDays: 1}, domain.System); err != nil {
		t.Fatal(err)
	}
	if due, _ := stopgapsDue(ctx, f.st.Read(), 10); len(due) != 0 {
		t.Fatalf("still due right after it was looked at: %v", due)
	}
}
