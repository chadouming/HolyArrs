package subtitles

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/builtin"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/faketranslator"
)

func builtInClient(t *testing.T, s *Service) *builtin.Client {
	t.Helper()
	c, _, err := s.translatorClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return c.(*builtin.Client)
}

// "All cards" runs one runner on every graphics card the runner lists, each
// taking several calls at once; one card is one runner.
func TestAllCardsUsesEveryDevice(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	withBuiltIn(t, s)
	ctx := context.Background()
	in := builtInInput()
	in.GPU = GPUAll
	if tr, err := s.SaveTranslator(ctx, in, domain.System); err != nil || tr.GPU != GPUAll {
		t.Fatalf("%+v %v", tr, err)
	}
	c := builtInClient(t, s)
	wantParallel := builtin.GPUParallelFor(usableVRAMMB(12216), 0)
	if len(c.Targets) != 2 || c.Targets[0].Opts.GPU != "Vulkan0" || c.Targets[1].Opts.GPU != "Vulkan1" || c.Targets[0].Runner == c.Targets[1].Runner {
		t.Fatalf("targets %+v", c.Targets)
	}
	for _, tg := range c.Targets {
		if tg.Opts.Parallel != wantParallel || tg.Opts.Context != builtin.GPUContext {
			t.Fatalf("card options %+v (want Parallel=%d)", tg.Opts, wantParallel)
		}
	}
	if c.Slots() != 2*wantParallel || slotsOf(c) != c.Slots() {
		t.Fatalf("%d slots (want %d)", c.Slots(), 2*wantParallel)
	}
	// One named card: one runner, still several calls at once.
	in.GPU = "Vulkan1"
	if _, err := s.SaveTranslator(ctx, in, domain.System); err != nil {
		t.Fatal(err)
	}
	if c := builtInClient(t, s); len(c.Targets) != 1 || c.Targets[0].Opts.GPU != "Vulkan1" || c.Slots() != wantParallel {
		t.Fatalf("one card: %+v", c.Targets)
	}
	// The CPU takes one call at a time.
	in.GPU = ""
	if _, err := s.SaveTranslator(ctx, in, domain.System); err != nil {
		t.Fatal(err)
	}
	if c := builtInClient(t, s); len(c.Targets) != 1 || c.Targets[0].Opts.GPU != "" || c.Slots() != 1 || c.Targets[0].Opts.Context != builtin.DefaultContext {
		t.Fatalf("CPU: %+v", c.Targets)
	}
}

// "All cards" on a machine with no card falls back to the CPU instead of failing.
func TestAllCardsWithNoCardUsesTheCPU(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	withBuiltIn(t, s)
	t.Setenv("FAKE_NO_DEVICES", "1")
	in := builtInInput()
	in.GPU = GPUAll
	if _, err := s.SaveTranslator(context.Background(), in, domain.System); err != nil {
		t.Fatal(err)
	}
	if c := builtInClient(t, s); len(c.Targets) != 1 || c.Targets[0].Opts.GPU != "" || c.Slots() != 1 {
		t.Fatalf("no card: %+v", c.Targets)
	}
}

// Only the built-in kind is cut into parts: other translators are called one
// chunk at a time, as before.
func TestOnlyBuiltInRunsInParts(t *testing.T) {
	if n := slotsOf(&translate.OpenAI{}); n != 1 {
		t.Fatalf("an AI service runs %d calls at once", n)
	}
	if n := slotsOf(&translate.Lingarr{}); n != 1 {
		t.Fatalf("Lingarr runs %d calls at once", n)
	}
}

// A model that reasons before answering is told not to, in the call and at the runner.
func TestNoThinkFormReachesTheRunner(t *testing.T) {
	m, ok := builtin.ByID("gemma-4-e4b-q4")
	if !ok || m.Form != builtin.FormNoThink {
		t.Fatalf("Gemma 4 E4B in the catalogue: %+v %v", m, ok)
	}
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	_, lab := withCatalogue(t, s)
	builtin.Catalogue[0].Form = builtin.FormNoThink
	ctx := context.Background()
	if err := s.downloadModel(ctx, lab.ID, noProgress); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveTranslator(ctx, TranslatorInput{Kind: TranslatorBuiltIn, BuiltInModel: lab.ID}, domain.System); err != nil {
		t.Fatal(err)
	}
	c := builtInClient(t, s)
	if c.Form != builtin.FormNoThink || !c.Targets[0].Opts.NoThink {
		t.Fatalf("client form %q, options %+v", c.Form, c.Targets[0].Opts)
	}
}

func TestShouldPublishTranslateSpeed(t *testing.T) {
	cases := []struct {
		name       string
		chunksDone int
		waveChunks int
		done       int
		totalLines int
		elapsed    float64
		want       bool
	}{
		{"single_slot_too_early", 1, 1, 20, 400, 0.4, false},
		{"single_slot_ready", 1, 1, 20, 400, 0.6, true},
		{"8_slot_wave1_first_chunk", 1, 8, 20, 400, 4.5, false},
		{"8_slot_wave1_seventh_chunk", 7, 8, 140, 400, 4.8, false},
		{"8_slot_wave1_complete", 8, 8, 160, 400, 5.0, true},
		{"short_file_completes_before_wave", 3, 8, 60, 60, 1.2, true},
		{"zero_done_suppressed", 1, 1, 0, 400, 1.0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldPublishTranslateSpeed(tc.chunksDone, tc.waveChunks, tc.done, tc.totalLines, tc.elapsed)
			if got != tc.want {
				t.Fatalf("shouldPublishTranslateSpeed(%d, %d, %d, %d, %.1f) = %v, want %v",
					tc.chunksDone, tc.waveChunks, tc.done, tc.totalLines, tc.elapsed, got, tc.want)
			}
		})
	}
}

func TestTranslatePoolWorkers(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	withBuiltIn(t, s)
	ctx := context.Background()

	if got := TranslatePoolWorkers(ctx, f.st); got != 4 {
		t.Fatalf("TranslatePoolWorkers(with 2 GPUs) = %d, want 4", got)
	}
	t.Setenv("FAKE_NO_DEVICES", "1")
	if got := TranslatePoolWorkers(ctx, f.st); got != 1 {
		t.Fatalf("TranslatePoolWorkers(no devices) = %d, want 1", got)
	}
}

func TestAcquireSerialTranslate(t *testing.T) {
	s := &Service{}
	var active, peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := s.acquireSerialTranslate(context.Background())
			if err != nil {
				t.Errorf("acquireSerialTranslate: %v", err)
				return
			}
			cur := active.Add(1)
			for {
				m := peak.Load()
				if cur <= m || peak.CompareAndSwap(m, cur) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			active.Add(-1)
			rel()
			rel()
		}()
	}
	wg.Wait()
	if peak.Load() != 1 {
		t.Fatalf("peak concurrent serial translations = %d, want 1", peak.Load())
	}

	// Respects context cancellation while slot is held.
	rel, err := s.acquireSerialTranslate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	cctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.acquireSerialTranslate(cctx); err == nil {
		t.Fatal("expected context deadline error when serial slot is held")
	}
}
