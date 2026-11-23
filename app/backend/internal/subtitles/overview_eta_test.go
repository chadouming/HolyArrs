package subtitles

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/faketranslator"
)

func setFileRuntime(t *testing.T, f *fix, fileID int64, sec int) {
	t.Helper()
	err := f.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE files SET media_info = ? WHERE id = ?`, fmt.Sprintf(`{"runtime_seconds":%d}`, sec), fileID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func insertOverviewJob(t *testing.T, f *fix, kind, state string, fileID int64, progress float64, resJSON string) int64 {
	t.Helper()
	ts := database.FormatTime(now())
	var subjArg any
	if fileID > 0 {
		subjArg = fmt.Sprintf(`{"type":"file","id":%d}`, fileID)
	}
	var resArg any
	if resJSON != "" {
		resArg = resJSON
	}
	var id int64
	err := f.st.Write(context.Background(), func(tx *sql.Tx) error {
		r, err := tx.Exec(`INSERT INTO jobs (created_at, created_by, updated_at, updated_by, kind, subject, params, state, progress, result, run_after, ended_at, asked_by)
			VALUES (?, ?, ?, ?, ?, ?, '{"language":"fr"}', ?, ?, ?, ?, ?, ?)`,
			ts, sysActor, ts, sysActor, kind, subjArg, state, progress, resArg, ts, ts, sysActor)
		if err != nil {
			return err
		}
		id, err = r.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestComputeBacklogETA_ZeroCases(t *testing.T) {
	// No missing or queued work.
	o := Overview{
		Transcriber:       Transcriber{Ready: true},
		TranscribeMissing: true,
		TranslatorReady:   true,
		Stats: SubtitleStats{
			Speech:    SpeedStats{AvgSpeed: 10},
			Translate: SpeedStats{AvgSpeed: 20},
		},
	}
	if got := computeBacklogETA(o, nil, nil, 0, 0, 2); got != 0 {
		t.Fatalf("empty backlog got %d, want 0", got)
	}

	// Work exists, but no speed data (neither live nor historical).
	o.Missing = 2
	o.CanTranslate = 1
	o.Stats = SubtitleStats{}
	if got := computeBacklogETA(o, nil, map[int64]float64{1: 1800}, 0, 0, 2); got != 0 {
		t.Fatalf("no speed data got %d, want 0", got)
	}

	// Sub-second positive ETA rounds up to at least 1 second.
	o.Missing = 1
	o.CanTranslate = 1
	o.Stats.Translate = SpeedStats{AvgSpeed: 5000, Samples: 1, TotalLines: 500}
	if got := computeBacklogETA(o, nil, nil, 0, 0, 1); got != 1 {
		t.Fatalf("sub-second ETA got %d, want 1", got)
	}
}

func TestComputeBacklogETA_SpeechOnly(t *testing.T) {
	// Active job progress deduction + live speed scaling across running workers.
	// File 1: 1000s (running, 50% done -> 500s left), File 2: 2000s (queued), File 3: 0s -> falls back to avgAudioSec (1500s from samples).
	// Total remaining audio = 500 + 2000 + 1500 = 4000s.
	// 2 running speech jobs, 1 live stat reporting 10x -> scaled live speed = 10 * (2/1) = 20x.
	// ETA = 4000 / 20 = 200s.
	o := Overview{
		Transcriber:       Transcriber{Ready: true},
		TranscribeMissing: true,
		Running:           map[string]int{KindTranscribe: 2},
		Queued:            map[string]int{KindTranscribe: 1},
		Stats: SubtitleStats{
			Speech: SpeedStats{
				LiveSpeed:         10.0,
				Samples:           2,
				TotalAudioSeconds: 3000.0, // avg 1500s
			},
		},
	}
	active := []jobs.Job{
		{ID: 1, Kind: KindTranscribe, State: "running", Progress: 0.5, Subject: &domain.Subject{Type: "file", ID: 1}},
		{ID: 2, Kind: KindTranscribe, State: "running", Progress: 0.0, Subject: &domain.Subject{Type: "file", ID: 2}},
		{ID: 3, Kind: KindTranscribe, State: "queued", Progress: 0.0, Subject: &domain.Subject{Type: "file", ID: 3}},
	}
	runtimes := map[int64]float64{1: 1000, 2: 2000, 3: 0}
	if got := computeBacklogETA(o, active, runtimes, 1, 0, 4); got != 200 {
		t.Fatalf("speech live scaled ETA got %d, want 200", got)
	}

	// Idle historical fallback with worker capping and positive runtime average fallback.
	// 2 pending files (1200s and 2400s -> avg 1800s), plus 1 extra queued job not in map -> +1800s = 5400s.
	// maxSpeechWorkers = 6, capped to speechCount = 3 -> 3 workers * 5x = 15x.
	// ETA = 5400 / 15 = 360s.
	oIdle := Overview{
		Transcriber:       Transcriber{Ready: true},
		TranscribeMissing: true,
		Queued:            map[string]int{KindTranscribe: 3},
		Stats: SubtitleStats{
			Speech: SpeedStats{AvgSpeed: 5.0},
		},
	}
	if got := computeBacklogETA(oIdle, nil, map[int64]float64{10: 1200, 20: 2400}, 0, 0, 6); got != 360 {
		t.Fatalf("speech idle capped workers ETA got %d, want 360", got)
	}

	// Fallback 2400s when no positive runtimes and no historical samples.
	oDefault := Overview{
		Transcriber:       Transcriber{Ready: true},
		TranscribeMissing: true,
		Stats: SubtitleStats{
			Speech: SpeedStats{AvgSpeed: 10.0},
		},
	}
	if got := computeBacklogETA(oDefault, nil, map[int64]float64{1: 0}, 0, 0, 2); got != 240 {
		t.Fatalf("speech 2400s default ETA got %d, want 240", got)
	}
}

func TestComputeBacklogETA_TranslateOnly(t *testing.T) {
	// Historical average lines = 800 / 2 = 400 lines/sub.
	// 3 subs total, 1 running at 25% progress -> 2.75 subs -> 1100 lines.
	// Live speed = 10 lines/s with 1 live worker and 1 running job -> 1100 / 10 = 110s.
	o := Overview{
		TranslatorReady: true,
		MissingCounts:   MissingCounts{Missing: 3, CanTranslate: 3},
		Running:         map[string]int{KindTranslate: 1},
		Queued:          map[string]int{KindTranslate: 2},
		Stats: SubtitleStats{
			Translate: SpeedStats{
				LiveSpeed:  10.0,
				AvgSpeed:   5.0,
				Samples:    2,
				TotalLines: 800,
			},
		},
	}
	active := []jobs.Job{
		{ID: 1, Kind: KindTranslate, State: "running", Progress: 0.25},
	}
	if got := computeBacklogETA(o, active, nil, 0, 1, 1); got != 110 {
		t.Fatalf("translate live ETA got %d, want 110", got)
	}

	// Idle historical fallback with default 500 lines/sub and clamped out-of-range progress.
	oHist := Overview{
		TranslatorReady: true,
		MissingCounts:   MissingCounts{Missing: 2, CanTranslate: 2},
		Stats: SubtitleStats{
			Translate: SpeedStats{AvgSpeed: 25.0},
		},
	}
	if got := computeBacklogETA(oHist, nil, nil, 0, 0, 1); got != 40 {
		t.Fatalf("translate historical ETA got %d, want 40", got)
	}
}

func TestComputeBacklogETA_CombinedParallelAndPostSpeech(t *testing.T) {
	// 2 speech files (each 1000s -> 2000s total) at 10x = 200s speechETA.
	// Missing = 7, CanTranslate = 1 (direct), speechCount = 2 -> postSpeechTr = 7 - 1 - 2 = 4.
	// Total translate subs = 1 + 4 = 5. At 500 lines/sub = 2500 lines.
	// At 5 lines/s = 500s translateETA.
	// Concurrent pipelines -> max(200, 500) = 500s.
	o := Overview{
		Transcriber:       Transcriber{Ready: true},
		TranscribeMissing: true,
		TranslatorReady:   true,
		MissingCounts:     MissingCounts{Missing: 7, CanTranslate: 1},
		Stats: SubtitleStats{
			Speech:    SpeedStats{AvgSpeed: 10.0},
			Translate: SpeedStats{AvgSpeed: 5.0},
		},
	}
	runtimes := map[int64]float64{1: 1000, 2: 1000}
	if got := computeBacklogETA(o, nil, runtimes, 0, 0, 1); got != 500 {
		t.Fatalf("combined translate-dominant ETA got %d, want 500", got)
	}

	// Now make speech slower than translate: 2000s at 2x = 1000s speechETA > 500s translateETA.
	o.Stats.Speech.AvgSpeed = 2.0
	if got := computeBacklogETA(o, nil, runtimes, 0, 0, 1); got != 1000 {
		t.Fatalf("combined speech-dominant ETA got %d, want 1000", got)
	}
}

func TestComputeBacklogETA_ManualModeIgnoresUnqueuedSpeech(t *testing.T) {
	// TranscribeMissing = false, no queued/running speech jobs -> pending speech map ignored, speechETA = 0.
	o := Overview{
		Transcriber:       Transcriber{Ready: true},
		TranscribeMissing: false,
		TranslatorReady:   true,
		MissingCounts:     MissingCounts{Missing: 4, NeedSpeech: 2, CanTranslate: 1},
		Stats: SubtitleStats{
			Speech:    SpeedStats{AvgSpeed: 1.0},
			Translate: SpeedStats{AvgSpeed: 10.0},
		},
	}
	// Only direct translation counts (1 * 500 / 10 = 50s); postSpeechTr is 0 because autoSpeechEligible is false.
	if got := computeBacklogETA(o, nil, map[int64]float64{1: 3600, 2: 3600}, 0, 0, 1); got != 50 {
		t.Fatalf("manual mode without speech jobs got %d, want 50", got)
	}

	// With 1 queued speech job in manual mode, only that job's runtime counts (600s / 2x = 300s).
	o.Queued = map[string]int{KindTranscribe: 1}
	o.Stats.Speech.AvgSpeed = 2.0
	if got := computeBacklogETA(o, nil, map[int64]float64{1: 600}, 0, 0, 1); got != 300 {
		t.Fatalf("manual mode with 1 queued speech job got %d, want 300", got)
	}
}

func TestOverview_ETASpeechTranslateAndManualMode(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	withTranscriber(t, f, s, "en,en,en", "eng")
	ctx := context.Background()

	// Seed files:
	// - movieFile: wants en, fr; no text track (1 speech + 1 post-speech translation), runtime 1000s.
	// - ep1File: wants fr; has en built-in text track (1 direct translation), runtime 500s.
	// - ep2File: wants fr; no text track, held in subtitle_transcripts (ignored by auto speech), runtime 3000s.
	subRow(t, f, f.movieFile, "en", "wish", "wanted", "", "")
	subRow(t, f, f.movieFile, "fr", "wish", "wanted", "", "")
	subRow(t, f, f.ep1File, "fr", "wish", "wanted", "", "")
	subRow(t, f, f.ep1File, "en", "built_in", "present", "S_TEXT/UTF8", "")
	subRow(t, f, f.ep2File, "fr", "wish", "wanted", "", "")
	s.noteTranscript(ctx, f.ep2File, true, "no speech")

	setFileRuntime(t, f, f.movieFile, 1000)
	setFileRuntime(t, f, f.ep1File, 500)
	setFileRuntime(t, f, f.ep2File, 3000)

	// Historical speed samples:
	// Speech: 600s in 60s -> AvgSpeed = 10x.
	// Translate: 300 lines in 30s -> AvgSpeed = 10 lines/s, avgLines = 300.
	insertOverviewJob(t, f, KindTranscribe, "done", 0, 1, `{"audio_seconds":600,"elapsed_seconds":60,"lines":120}`)
	insertOverviewJob(t, f, KindTranslate, "done", 0, 1, `{"translated":300,"elapsed_seconds":30}`)

	// Running speech job on movieFile at 40% progress, with live speed 5x -> remaining audio = 600s, speechETA = 120s.
	// Missing = 4, CanTranslate = 1 (ep1File), speechCount = 1 (movieFile) -> postSpeechTr = 4 - 1 - 1 = 2 -> 3 total translations * 300 lines = 900 lines / 10 lps = 90s.
	// Max(120, 90) = 120s.
	transcribeOn(t, f)
	liveSp := insertOverviewJob(t, f, KindTranscribe, "running", f.movieFile, 0.4, "")
	s.setLiveStat(liveSp, KindTranscribe, 5.0, "x")

	o, err := s.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if o.ETASeconds != 120 {
		t.Fatalf("auto mode ETA got %d, want 120 (stats=%+v counts=%+v)", o.ETASeconds, o.Stats, o.MissingCounts)
	}

	// Verify JSON serialization includes eta_seconds.
	m := http.NewServeMux()
	NewHandlers(f.st, s.Vault, s).Routes(m)
	code, body := call(m, "GET", "/api/v1/subtitles/overview", "")
	if code != http.StatusOK {
		t.Fatalf("GET overview: %d %s", code, body)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatal(err)
	}
	if got, ok := payload["eta_seconds"].(float64); !ok || int64(got) != 120 {
		t.Fatalf("json eta_seconds = %v, want 120 (body=%s)", payload["eta_seconds"], body)
	}

	// Switch to manual mode (TranscribeMissing = false):
	// Held/running movieFile is still counted because it is actively running (600s / 5x = 120s).
	// Clear running speech job and add queued speech job on ep2File (3000s / 10x = 300s).
	set := ReadSettings(ctx, f.st.Read())
	set.TranscribeMissing = false
	if err := SaveSettings(ctx, f.st, set, domain.System); err != nil {
		t.Fatal(err)
	}
	s.clearLiveStat(liveSp)
	err = f.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM jobs WHERE id = ?`, liveSp)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// With no active speech job in manual mode, only direct translation (ep1File = 300 lines / 10 lps = 30s) counts.
	o, err = s.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if o.ETASeconds != 30 {
		t.Fatalf("manual mode idle ETA got %d, want 30", o.ETASeconds)
	}

	// Queue a manual speech job for ep2File (3000s) -> 3000 / 10 = 300s > 30s.
	insertOverviewJob(t, f, KindTranscribe, "queued", f.ep2File, 0, "")
	o, err = s.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if o.ETASeconds != 300 {
		t.Fatalf("manual mode queued speech ETA got %d, want 300", o.ETASeconds)
	}
}
