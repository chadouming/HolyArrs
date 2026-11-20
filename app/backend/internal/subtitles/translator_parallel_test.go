package subtitles

import (
	"context"
	"testing"

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
	if len(c.Targets) != 2 || c.Targets[0].Opts.GPU != "Vulkan0" || c.Targets[1].Opts.GPU != "Vulkan1" || c.Targets[0].Runner == c.Targets[1].Runner {
		t.Fatalf("targets %+v", c.Targets)
	}
	for _, tg := range c.Targets {
		if tg.Opts.Parallel != builtin.GPUParallel || tg.Opts.Context != builtin.GPUContext {
			t.Fatalf("card options %+v", tg.Opts)
		}
	}
	if c.Slots() != 2*builtin.GPUParallel || slotsOf(c) != c.Slots() {
		t.Fatalf("%d slots", c.Slots())
	}
	// One named card: one runner, still several calls at once.
	in.GPU = "Vulkan1"
	if _, err := s.SaveTranslator(ctx, in, domain.System); err != nil {
		t.Fatal(err)
	}
	if c := builtInClient(t, s); len(c.Targets) != 1 || c.Targets[0].Opts.GPU != "Vulkan1" || c.Slots() != builtin.GPUParallel {
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
