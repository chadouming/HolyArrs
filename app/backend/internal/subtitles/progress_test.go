package subtitles

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/faketranslator"
)

// told collects what a job would be told about its progress.
func told(ctx context.Context) (context.Context, func() []float64) {
	var mu sync.Mutex
	var got []float64
	ctx = withProgress(ctx, func(p float64) {
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
	})
	return ctx, func() []float64 {
		mu.Lock()
		defer mu.Unlock()
		return append([]float64(nil), got...)
	}
}

// A translation tells its job how many of the lines are done.
func TestTranslateTellsItsProgress(t *testing.T) {
	f := newFix(t)
	track := labMovie(t, f)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	wishAll(t, f, Want{Language: "pt-br", TranslateFrom: "en"})
	ctx, got := told(context.Background())
	if _, err := s.Translate(ctx, f.movieFile, TranslateRequest{Language: "pt-BR", Source: SourceBuiltIn, Track: track, ByHand: true}, domain.System); err != nil {
		t.Fatal(err)
	}
	ps := got()
	if len(ps) == 0 || ps[len(ps)-1] <= 0 || ps[len(ps)-1] > 1 {
		t.Fatalf("progress %v", ps)
	}
}

// A transcript tells its job how far the speech program is.
func TestTranscribeTellsItsProgress(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	withTranscriber(t, f, s, "en,en,en", "eng")
	wishAll(t, f, Want{Language: "en"})
	ctx, got := told(context.Background())
	if _, err := s.Transcribe(ctx, f.movieFile, true, domain.System); err != nil {
		t.Fatal(err)
	}
	ps := got()
	if len(ps) < 3 || ps[len(ps)-1] < 0.8 || ps[len(ps)-1] > 1 {
		t.Fatalf("progress %v", ps)
	}
}

// Telling progress with nobody listening does nothing.
func TestProgressWithNoListener(t *testing.T) {
	tellProgress(context.Background(), 0.5)
}

func TestJobProgressThrottlingAndImmediateTriggers(t *testing.T) {
	f := newFix(t)
	origInterval := progressMinInterval
	origNow := progressNow
	t.Cleanup(func() {
		progressMinInterval = origInterval
		progressNow = origNow
	})

	var clockMu sync.Mutex
	curTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	progressMinInterval = 2 * time.Second
	progressNow = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return curTime
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		curTime = curTime.Add(d)
		clockMu.Unlock()
	}

	type update struct {
		p    float64
		note string
	}
	var updatesMu sync.Mutex
	var updates []update
	done := make(chan struct{})

	var q *jobs.Queue
	q = jobs.New(f.st, jobs.Options{
		Workers:      1,
		PollInterval: 5 * time.Millisecond,
		OnChange: func(j jobs.Job) {
			if j.Kind != KindTranscribe {
				return
			}
			if j.State == "running" && j.Progress > 0 {
				note := ""
				if j.ProgressNote != nil {
					note = *j.ProgressNote
				}
				updatesMu.Lock()
				updates = append(updates, update{p: j.Progress, note: note})
				updatesMu.Unlock()
			}
			if j.State == "done" {
				close(done)
			}
		},
	})
	q.Handle(KindTranscribe, func(ctx context.Context, r *jobs.Run) error {
		pctx := jobProgress(ctx, r)
		// First update writes immediately (last < 0).
		tellProgressNote(pctx, 0.05, "extracting")
		// Rapid percentage updates within 2s are throttled.
		advance(500 * time.Millisecond)
		tellProgressNote(pctx, 0.06, "extracting")
		advance(500 * time.Millisecond)
		tellProgressNote(pctx, 0.07, "")
		// Note/phase change writes immediately even inside 2s window.
		advance(200 * time.Millisecond)
		tellProgressNote(pctx, 0.08, "transcribing")
		// Duplicate whole-percent without phase change is dropped even after 2s.
		advance(3 * time.Second)
		tellProgressNote(pctx, 0.089, "transcribing")
		// Percent increase after >= 2s writes.
		tellProgressNote(pctx, 0.25, "transcribing")
		// Completion (p >= 1) writes immediately even inside 2s window.
		advance(100 * time.Millisecond)
		tellProgressNote(pctx, 1.0, "transcribing")
		return nil
	})
	if err := q.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Stop)

	if _, _, err := q.Enqueue(context.Background(), KindTranscribe, &domain.Subject{Type: "file", ID: f.movieFile}, nil, "", domain.System); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for job completion")
	}

	updatesMu.Lock()
	defer updatesMu.Unlock()
	want := []update{
		{p: 0.05, note: "extracting"},
		{p: 0.08, note: "transcribing"},
		{p: 0.25, note: "transcribing"},
		{p: 1.0, note: "transcribing"},
	}
	if len(updates) != len(want) {
		t.Fatalf("got %d progress writes %+v, want %d %+v", len(updates), updates, len(want), want)
	}
	for i := range want {
		if updates[i] != want[i] {
			t.Fatalf("update[%d] = %+v, want %+v (all=%+v)", i, updates[i], want[i], updates)
		}
	}
}
