package subtitles

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/transcribe"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/builtin"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/faketranslator"
)

func TestSpeechCapAndTightVRAM(t *testing.T) {
	if got := speechCapForCard(0, 1600<<20); got != DefaultTranscribeAtOnce {
		t.Fatalf("unknown memory cap = %d, want %d", got, DefaultTranscribeAtOnce)
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
	if got := speechCapForCard(8192, 1600<<20); got != 3 {
		t.Fatalf("8 GB Q8_0 cap = %d, want 3", got)
	}
	if got := speechCapForCard(8192, 2500<<20); got != 2 {
		t.Fatalf("8 GB FP16 cap = %d, want 2", got)
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

func TestVRAMBudgetHelpers(t *testing.T) {
	if got := usableVRAMMB(0); got != 0 {
		t.Fatalf("usableVRAMMB(0) = %d, want 0", got)
	}
	if got := usableVRAMMB(4096); got != 3072 {
		t.Fatalf("usableVRAMMB(4096) = %d, want 3072", got)
	}
	if got := usableVRAMMB(12216); got != 10995 {
		t.Fatalf("usableVRAMMB(12216) = %d, want 10995", got)
	}
	if got := speechSlotVRAMMB(2500 << 20); got != 3000 {
		t.Fatalf("speechSlotVRAMMB(2500M) = %d, want 3000", got)
	}
	if got := speechSlotVRAMMB(1600 << 20); got != 2100 {
		t.Fatalf("speechSlotVRAMMB(1600M) = %d, want 2100", got)
	}
	if got := speechSlotVRAMMB(500 << 20); got != 2000 {
		t.Fatalf("speechSlotVRAMMB(500M) = %d, want 2000", got)
	}
	if got := translatorEstVRAMMB(3<<30, 4); got != 3328+4*160 {
		t.Fatalf("translatorEstVRAMMB(3G,4) = %d, want %d", got, 3328+4*160)
	}
	if got := translatorEstVRAMMB(3<<30, 8); got != 3328+8*160 {
		t.Fatalf("translatorEstVRAMMB(3G,8) = %d, want %d", got, 3328+8*160)
	}
	if got := translatorEstVRAMMB(7<<30, 2); got != 7424+2*280 {
		t.Fatalf("translatorEstVRAMMB(7G,2) = %d, want %d", got, 7424+2*280)
	}

	// 12 GB B580 sharing: llama (3800) + transcode (1200) -> ~5995 available -> 1 whisper FP16 slot per card.
	usable12 := usableVRAMMB(12216)
	if got := speechCapForAvailable(usable12-3800-1200, 2500<<20); got != 1 {
		t.Fatalf("12 GB shared speech cap = %d, want 1", got)
	}
	// 12 GB B580 dedicated -> 3 whisper slots.
	if got := speechCapForAvailable(usable12, 2500<<20); got != 3 {
		t.Fatalf("12 GB dedicated speech cap = %d, want 3", got)
	}
	// 12 GB B580 sharing llama with transcode (1200) -> 9795 -> llama Parallel=12.
	if got := builtin.GPUParallelFor(usable12-1200, 3<<30); got != 12 {
		t.Fatalf("12 GB + transcode llama parallel = %d, want 12", got)
	}
	// 12 GB B580 sharing llama with transcode (1200) + q8_0 whisper (2100) -> 7695 -> llama Parallel=10.
	if got := builtin.GPUParallelFor(usable12-1200-2100, 3<<30); got != 10 {
		t.Fatalf("12 GB shared llama parallel = %d, want 10", got)
	}
	// 12 GB B580 sharing llama with transcode (1200) + 2x q8_0 whisper (4200) -> 5595 -> llama Parallel=8.
	if got := builtin.GPUParallelFor(usable12-1200-2*2100, 3<<30); got != 8 {
		t.Fatalf("12 GB + 2x Q8 + transcode llama parallel = %d, want 8", got)
	}
	// 12 GB B580 sharing llama with transcode (1200) + 2x FP16 whisper (6000) -> 3795 -> llama Parallel=6.
	if got := builtin.GPUParallelFor(usable12-1200-2*3000, 3<<30); got != 6 {
		t.Fatalf("12 GB + 2x FP16 + transcode llama parallel = %d, want 6", got)
	}
	// 8 GB, 6 GB, and 4 GB sharing with transcode -> llama scales to 8, 6, and 1, whisper scales to 1.
	if got := builtin.GPUParallelFor(usableVRAMMB(8192)-1200, 3<<30); got != 8 {
		t.Fatalf("8 GB shared llama parallel = %d, want 8", got)
	}
	if got := builtin.GPUParallelFor(usableVRAMMB(6144)-1200, 3<<30); got != 6 {
		t.Fatalf("6 GB shared llama parallel = %d, want 6", got)
	}
	if got := builtin.GPUParallelFor(usableVRAMMB(4096)-1200, 3<<30); got != 1 {
		t.Fatalf("4 GB shared llama parallel = %d, want 1", got)
	}
	if got := speechCapForAvailable(usableVRAMMB(8192)-1200-4000, 1600<<20); got != 1 {
		t.Fatalf("8 GB shared speech cap = %d, want 1", got)
	}

	// needsVRAMRoom triggers on 12 GB cards when workloads exceed usable VRAM.
	if needsVRAMRoom(12216, 4000, 6000) {
		t.Fatal("10000 <= 10995 should not require eviction")
	}
	if !needsVRAMRoom(12216, 4608, 1200+2*3000) {
		t.Fatal("11808 > 10995 must require eviction on 12 GB card")
	}
	if !needsVRAMRoom(8192, 2000, 0) {
		t.Fatal("8192 <= 8500 tightVRAM must remain true")
	}
}

func TestCrossEngineVRAMBudgetAndYield(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	withBuiltIn(t, s)
	withTranscriber(t, f, s, "en,en,en", "eng")
	withSpeechServer(t, s)
	ctx := context.Background()

	in := builtInInput()
	in.GPU = "Vulkan0"
	if _, err := s.SaveTranslator(ctx, in, domain.System); err != nil {
		t.Fatal(err)
	}

	// Start 2 speech servers on card 0 and 1 idle llama runner on Vulkan0.
	progs, _ := transcribe.Find()
	sp, _ := s.speechModel(ctx)
	spPath, err := s.modelFor(sp.ID)
	if err != nil {
		t.Fatal(err)
	}
	vad, _ := builtin.ByKind(builtin.KindVAD)
	vadPath, err := s.modelFor(vad.ID)
	if err != nil {
		t.Fatal(err)
	}
	cards := []int{0}
	_, srv0, rel0, err := s.speech.take(ctx, cards, 2)
	if err != nil {
		t.Fatal(err)
	}
	_, srv1, rel1, err := s.speech.take(ctx, cards, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, srv := range []*transcribe.Server{srv0, srv1} {
		if _, err := transcribe.Run(ctx, "video.mkv", transcribe.Track{Index: 0, Language: "eng", Seconds: 600}, "en", transcribe.Options{
			Programs: progs, Model: spPath.Path, VAD: vadPath.Path, Device: 0, Threads: 2, Server: srv,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.speech.activeOnCard(0); got != 2 {
		t.Fatalf("activeOnCard(0) = %d, want 2", got)
	}
	if got := s.speech.runningOnCard(0); got != 2 {
		t.Fatalf("runningOnCard(0) = %d, want 2", got)
	}
	rel0()
	rel1()
	if got := s.speech.activeOnCard(0); got != 0 {
		t.Fatalf("after release activeOnCard(0) = %d, want 0", got)
	}

	bin, ok := builtin.Find()
	if !ok {
		t.Fatal("fake builtin runner not found")
	}
	r0 := s.Runners.Runner("Vulkan0")
	if _, _, err := r0.Ensure(ctx, builtin.Options{
		Binary: bin, ModelPath: filepath.Join(s.ModelsDir, "lab.gguf"), GPU: "Vulkan0", Threads: 2, Context: builtin.GPUContext, Parallel: 4,
	}); err != nil {
		t.Fatal(err)
	}
	r0.Used()
	if !s.Runners.RunningOn("Vulkan0") || s.Runners.ActiveOn("Vulkan0") {
		t.Fatal("expected Vulkan0 runner resident and idle")
	}

	// 2 idle whispers (2*3000=6000) + llama (3072) + reserve (2500) = 11572 > 10995:
	// YieldGPUForVRAM stops 1 excess whisper, leaving 1 whisper (3000) + llama (3072) warm (8572 <= 10995).
	s.YieldGPUForVRAM(ctx, 0, 2500)
	if got := s.speech.runningOnCard(0); got != 1 {
		t.Fatalf("after YieldGPUForVRAM(0, 2500) runningOnCard(0) = %d, want 1", got)
	}
	if !s.Runners.RunningOn("Vulkan0") {
		t.Fatal("expected llama runner to stay warm when 1 whisper + llama + 2500 <= usable")
	}

	// Small reserve (1500) fits alongside 1 whisper (3000) + llama (3072) = 7572 <= 10995: leaves both warm.
	s.YieldGPUForVRAM(ctx, 0, 1500)
	if s.speech.runningOnCard(0) != 1 || !s.Runners.RunningOn("Vulkan0") {
		t.Fatal("expected YieldGPUForVRAM to leave models warm when budget fits")
	}

	// Large reserve (8500): evicts both idle whisper and idle llama.
	s.YieldGPUForVRAM(ctx, 0, 8500)
	if s.speech.runningOnCard(0) != 0 || s.Runners.RunningOn("Vulkan0") {
		t.Fatalf("expected large reserve to evict idle models, speech=%d llama=%v",
			s.speech.runningOnCard(0), s.Runners.RunningOn("Vulkan0"))
	}

	// With ExternalGPUVRAMMB=1200 and active FP16/fallback speech slots (3000 MiB each) on card 0:
	// 1x speech: 10995 - 1200 - 3000 = 6795 -> Parallel=10.
	// 2x speech: 10995 - 1200 - 6000 = 3795 -> Parallel=6.
	s.ExternalGPUVRAMMB = func(_ context.Context, card int) int {
		if card == 0 {
			return 1200
		}
		return 0
	}
	_, _, relBusy, err := s.speech.take(ctx, cards, 2)
	if err != nil {
		t.Fatal(err)
	}
	if c := builtInClient(t, s); len(c.Targets) != 1 || c.Targets[0].Opts.Parallel != 10 {
		relBusy()
		t.Fatalf("shared 12 GB translator Parallel with 1x 3000 MiB speech = %d, want 10", c.Targets[0].Opts.Parallel)
	}
	_, _, relBusy2, err := s.speech.take(ctx, cards, 2)
	if err != nil {
		relBusy()
		t.Fatal(err)
	}
	if c := builtInClient(t, s); len(c.Targets) != 1 || c.Targets[0].Opts.Parallel != 6 {
		relBusy2()
		relBusy()
		t.Fatalf("shared 12 GB translator Parallel with 2x 3000 MiB speech = %d, want 6", c.Targets[0].Opts.Parallel)
	}
	// With Q8_0 speech model (1600 MiB -> 2100 MiB slot) + transcode (1200 MiB):
	// 2x speech: 10995 - 1200 - 4200 = 5595 -> Parallel=8.
	// 1x speech: 10995 - 1200 - 2100 = 7695 -> Parallel=10.
	oldCatNeed := builtin.Catalogue[0].NeedBytes
	t.Cleanup(func() { builtin.Catalogue[0].NeedBytes = oldCatNeed })
	builtin.Catalogue[0].NeedBytes = 1600 << 20
	if c := builtInClient(t, s); len(c.Targets) != 1 || c.Targets[0].Opts.Parallel != 8 {
		relBusy2()
		relBusy()
		t.Fatalf("shared 12 GB translator Parallel with 2x Q8_0 speech = %d, want 8", c.Targets[0].Opts.Parallel)
	}
	relBusy2()
	if c := builtInClient(t, s); len(c.Targets) != 1 || c.Targets[0].Opts.Parallel != 10 {
		relBusy()
		t.Fatalf("shared 12 GB translator Parallel with 1x Q8_0 speech = %d, want 10", c.Targets[0].Opts.Parallel)
	}
	relBusy()

	// translatorClient evicts idle speech servers on 12 GB card when 2 idle speech + ext + 8-slot llama > usable.
	_, s0, relA, err := s.speech.take(ctx, cards, 2)
	if err != nil {
		t.Fatal(err)
	}
	_, s1, relB, err := s.speech.take(ctx, cards, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, srv := range []*transcribe.Server{s0, s1} {
		if _, err := transcribe.Run(ctx, "video.mkv", transcribe.Track{Index: 0, Language: "eng", Seconds: 600}, "en", transcribe.Options{
			Programs: progs, Model: spPath.Path, VAD: vadPath.Path, Device: 0, Threads: 2, Server: srv,
		}); err != nil {
			t.Fatal(err)
		}
	}
	relA()
	relB()
	if got := s.speech.runningOnCard(0); got != 2 {
		t.Fatalf("before translatorClient runningOnCard(0) = %d, want 2", got)
	}
	// ext (4000) + 2*2100 (speech=4200) + translatorEstVRAMMB(0, 8)=3280 -> 11480 > 10995 -> evicts idle speech servers
	// (4 slots would only estimate 2640 -> 10840 <= 10995 and fail to evict).
	s.ExternalGPUVRAMMB = func(_ context.Context, card int) int {
		if card == 0 {
			return 4000
		}
		return 0
	}
	_ = builtInClient(t, s)
	if got := s.speech.runningOnCard(0); got != 0 {
		t.Fatalf("after translatorClient eviction runningOnCard(0) = %d, want 0", got)
	}

	// Ensure stopIdleExcess trims warm speech servers when capacity shrinks from 2 to 1.
	_, es0, relE0, err := s.speech.take(ctx, cards, 2)
	if err != nil {
		t.Fatal(err)
	}
	_, es1, relE1, err := s.speech.take(ctx, cards, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, srv := range []*transcribe.Server{es0, es1} {
		if _, err := transcribe.Run(ctx, "video.mkv", transcribe.Track{Index: 0, Language: "eng", Seconds: 600}, "en", transcribe.Options{
			Programs: progs, Model: spPath.Path, VAD: vadPath.Path, Device: 0, Threads: 2, Server: srv,
		}); err != nil {
			t.Fatal(err)
		}
	}
	relE0()
	relE1()
	s.speech.stopIdleExcess(0, 1)
	if got := s.speech.runningOnCard(0); got != 1 {
		t.Fatalf("after stopIdleExcess(0, 1) runningOnCard(0) = %d, want 1", got)
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

func TestActiveTranslatorEstVRAMMB(t *testing.T) {
	var nilSvc *Service
	if got := nilSvc.activeTranslatorEstVRAMMB(context.Background()); got != 3072 {
		t.Fatalf("nil service VRAM = %d, want 3072", got)
	}

	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	withBuiltIn(t, s)
	ctx := context.Background()

	// Unknown / custom file model falls back to 3072 MB.
	if _, err := s.SaveTranslator(ctx, builtInInput(), domain.System); err != nil {
		t.Fatal(err)
	}
	if got := s.activeTranslatorEstVRAMMB(ctx); got != 3072 {
		t.Fatalf("custom model VRAM = %d, want 3072", got)
	}

	// hy-mt2-1.8b-q8 (3 GiB) reports 3072 MB.
	if _, err := s.SaveTranslator(ctx, TranslatorInput{Kind: TranslatorBuiltIn, BuiltInModel: "hy-mt2-1.8b-q8"}, domain.System); err != nil {
		t.Fatal(err)
	}
	if got := s.activeTranslatorEstVRAMMB(ctx); got != 3072 {
		t.Fatalf("hy-mt2-1.8b-q8 VRAM = %d, want 3072", got)
	}

	// gemma-4-e4b-q4 (7 GiB) reports 7168 MB.
	if _, err := s.SaveTranslator(ctx, TranslatorInput{Kind: TranslatorBuiltIn, BuiltInModel: "gemma-4-e4b-q4"}, domain.System); err != nil {
		t.Fatal(err)
	}
	if got := s.activeTranslatorEstVRAMMB(ctx); got != 7168 {
		t.Fatalf("gemma-4-e4b-q4 VRAM = %d, want 7168", got)
	}

	// Small built-in model clamps up to 2400 MB floor.
	old := builtin.Catalogue
	builtin.Catalogue = append([]builtin.Model{{ID: "tiny-tr", NeedBytes: 1 << 30}}, old...)
	t.Cleanup(func() { builtin.Catalogue = old })
	if _, err := s.SaveTranslator(ctx, TranslatorInput{Kind: TranslatorBuiltIn, BuiltInModel: "tiny-tr"}, domain.System); err != nil {
		t.Fatal(err)
	}
	if got := s.activeTranslatorEstVRAMMB(ctx); got != 2400 {
		t.Fatalf("tiny model VRAM = %d, want 2400", got)
	}
}

func TestDeviceIDAndMemoryLookup(t *testing.T) {
	ovDevs := []builtin.Device{
		{ID: "GPU0", MemoryMB: 12216},
		{ID: "GPU1", MemoryMB: 4096},
	}
	if got := cardDeviceID(ovDevs, 0); got != "GPU0" {
		t.Fatalf("cardDeviceID(ov, 0) = %q, want GPU0", got)
	}
	if got := cardDeviceID(ovDevs, 1); got != "GPU1" {
		t.Fatalf("cardDeviceID(ov, 1) = %q, want GPU1", got)
	}
	if got := cardDeviceID(ovDevs, 2); got != "Vulkan2" {
		t.Fatalf("cardDeviceID(ov, 2) = %q, want Vulkan2", got)
	}
	if got := deviceMemoryMB(ovDevs, "GPU1"); got != 4096 {
		t.Fatalf("deviceMemoryMB(ov, GPU1) = %d, want 4096", got)
	}
	if got := deviceMemoryMB(ovDevs, "Vulkan0"); got != 12216 {
		t.Fatalf("legacy Vulkan0 lookup against GPU0 = %d, want 12216", got)
	}
	if got := deviceMemoryMB(ovDevs, "Vulkan2"); got != 0 {
		t.Fatalf("deviceMemoryMB(ov, Vulkan2) = %d, want 0", got)
	}

	vkDevs := []builtin.Device{
		{ID: "Vulkan0", MemoryMB: 8192},
		{ID: "Vulkan1", MemoryMB: 6144},
	}
	if got := cardDeviceID(vkDevs, 0); got != "Vulkan0" {
		t.Fatalf("cardDeviceID(vk, 0) = %q, want Vulkan0", got)
	}
	if got := cardDeviceID(vkDevs, 1); got != "Vulkan1" {
		t.Fatalf("cardDeviceID(vk, 1) = %q, want Vulkan1", got)
	}
	if got := deviceMemoryMB(vkDevs, "Vulkan1"); got != 6144 {
		t.Fatalf("deviceMemoryMB(vk, Vulkan1) = %d, want 6144", got)
	}
}
