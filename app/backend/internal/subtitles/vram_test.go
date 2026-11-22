package subtitles

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/builtin"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/faketranslator"
)

func TestSpeechCapAndTightVRAM(t *testing.T) {
	if got := speechCapForCard(0, 1600<<20); got != MaxTranscribeAtOnce {
		t.Fatalf("unknown memory cap = %d, want %d", got, MaxTranscribeAtOnce)
	}
	if got := speechCapForCard(4096, 1100<<20); got != 1 {
		t.Fatalf("4 GB cap = %d, want 1", got)
	}
	if got := speechCapForCard(6144, 1600<<20); got != 2 {
		t.Fatalf("6 GB Q8_0 cap = %d, want 2", got)
	}
	if got := speechCapForCard(6144, 2500<<20); got != 1 {
		t.Fatalf("6 GB FP16 cap = %d, want 1", got)
	}
	if got := speechCapForCard(8192, 1600<<20); got != 2 {
		t.Fatalf("8 GB cap = %d, want 2", got)
	}
	if got := speechCapForCard(12216, 2500<<20); got != MaxTranscribeAtOnce {
		t.Fatalf("12 GB cap = %d, want %d", got, MaxTranscribeAtOnce)
	}

	devs := []builtin.Device{
		{ID: "Vulkan0", MemoryMB: 12216},
		{ID: "Vulkan1", MemoryMB: 4096},
	}
	if got := effectiveTranscribeAtOnce(3, []int{0}, devs, 1600<<20); got != 3 {
		t.Fatalf("12 GB effective = %d, want 3", got)
	}
	if got := effectiveTranscribeAtOnce(3, []int{0, 1}, devs, 1600<<20); got != 1 {
		t.Fatalf("mixed 12+4 GB effective = %d, want 1", got)
	}
	if got := effectiveTranscribeAtOnce(0, []int{0}, devs, 1600<<20); got != 1 {
		t.Fatalf("wanted 0 effective = %d, want 1", got)
	}

	for _, c := range []struct {
		mb   int
		want bool
	}{
		{0, false},
		{4096, true},
		{6144, true},
		{8192, true},
		{8500, true},
		{12216, false},
	} {
		if got := tightVRAM(c.mb); got != c.want {
			t.Fatalf("tightVRAM(%d) = %v, want %v", c.mb, got, c.want)
		}
	}
}

func TestTranscribeModelSettingValidationAndFallback(t *testing.T) {
	f := newFix(t)
	ctx := context.Background()

	// Reject unknown or non-speech model IDs.
	set := DefaultSettings()
	set.TranscribeModel = "hy-mt2-1.8b-q8"
	if err := SaveSettings(ctx, f.st, set, domain.System); err == nil {
		t.Fatal("accepted a translation model as transcribe_model")
	}
	set.TranscribeModel = "nonexistent-model"
	if err := SaveSettings(ctx, f.st, set, domain.System); err == nil {
		t.Fatal("accepted an unknown transcribe_model")
	}

	// Valid speech model persists and shows as chosen.
	set.TranscribeModel = "whisper-large-v3-turbo-q5_0"
	if err := SaveSettings(ctx, f.st, set, domain.System); err != nil {
		t.Fatal(err)
	}
	if got := ReadSettings(ctx, f.st.Read()).TranscribeModel; got != "whisper-large-v3-turbo-q5_0" {
		t.Fatalf("read back %q", got)
	}

	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	s.ModelsDir = t.TempDir()
	if m, ok := s.speechModel(ctx); !ok || m.ID != "whisper-large-v3-turbo-q5_0" {
		t.Fatalf("speechModel = %+v, %v", m, ok)
	}

	var q5Chosen bool
	for _, mv := range s.Models(ctx) {
		if mv.ID == "whisper-large-v3-turbo-q5_0" {
			q5Chosen = mv.Chosen
		}
	}
	if !q5Chosen {
		t.Fatal("selected speech model not marked Chosen in Models()")
	}

	// When unset, backward-compat picks an already-downloaded FP16 model if default Q8_0 is absent.
	set.TranscribeModel = ""
	if err := SaveSettings(ctx, f.st, set, domain.System); err != nil {
		t.Fatal(err)
	}
	fp16, _ := builtin.ByID("whisper-large-v3-turbo")
	fp16File := filepath.Join(s.ModelsDir, fp16.ID+".gguf")
	fh, err := os.Create(fp16File)
	if err != nil {
		t.Fatal(err)
	}
	if err := fh.Truncate(fp16.Size); err != nil {
		fh.Close()
		t.Fatal(err)
	}
	fh.Close()

	if m, ok := s.speechModel(ctx); !ok || m.ID != "whisper-large-v3-turbo" {
		t.Fatalf("backward-compat speechModel = %+v, %v; want whisper-large-v3-turbo", m, ok)
	}

	// Once default Q8_0 is also on disk, it becomes the default again.
	q8, _ := builtin.ByID("whisper-large-v3-turbo-q8_0")
	q8File := filepath.Join(s.ModelsDir, q8.ID+".gguf")
	fh2, err := os.Create(q8File)
	if err != nil {
		t.Fatal(err)
	}
	if err := fh2.Truncate(q8.Size); err != nil {
		fh2.Close()
		t.Fatal(err)
	}
	fh2.Close()

	if m, ok := s.speechModel(ctx); !ok || m.ID != "whisper-large-v3-turbo-q8_0" {
		t.Fatalf("default speechModel = %+v, %v; want whisper-large-v3-turbo-q8_0", m, ok)
	}
}

func TestSpeechPlacesTakeExtraNonBlocking(t *testing.T) {
	var p speechPlaces
	ctx := context.Background()
	cards := []int{0}
	_, _, rel0, err := p.take(ctx, cards, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer rel0()

	extras, relExtra := p.takeExtra(cards, 3, 2)
	if len(extras) != 2 || extras[0].Device != 0 || extras[1].Device != 0 {
		t.Fatalf("got extras %+v", extras)
	}

	// All 3 slots are busy: takeExtra must return immediately with 0 slots, never block.
	done := make(chan int, 1)
	go func() {
		more, rel := p.takeExtra(cards, 3, 2)
		rel()
		done <- len(more)
	}()
	select {
	case n := <-done:
		if n != 0 {
			t.Fatalf("got %d extra slots when full, want 0", n)
		}
	case <-time.After(time.Second):
		t.Fatal("takeExtra blocked when all slots were busy")
	}

	relExtra()
	extras2, relExtra2 := p.takeExtra(cards, 3, 1)
	if len(extras2) != 1 {
		t.Fatalf("after release got %d extra slots, want 1", len(extras2))
	}
	relExtra2()
	p.stop()
}
