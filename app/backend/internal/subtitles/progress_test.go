package subtitles

import (
	"context"
	"sync"
	"testing"

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
