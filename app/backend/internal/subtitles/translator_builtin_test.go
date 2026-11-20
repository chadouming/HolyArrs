package subtitles

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/container"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/builtin"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/faketranslator"
)

var (
	fakeRunnerOnce sync.Once
	fakeRunnerPath string
)

// fakeRunner builds the stand-in for llama-server once and returns its path.
func fakeRunner(t *testing.T) string {
	t.Helper()
	fakeRunnerOnce.Do(func() {
		dir, err := os.MkdirTemp("", "fakerunner")
		if err != nil {
			t.Fatal(err)
		}
		fakeRunnerPath = filepath.Join(dir, "fakerunner")
		if out, err := exec.Command("go", "build", "-o", fakeRunnerPath, "./translate/builtin/fakerunner").CombinedOutput(); err != nil {
			t.Fatalf("building fakerunner: %v\n%s", err, out)
		}
	})
	return fakeRunnerPath
}

// withBuiltIn gives s a runner (the stand-in) and a models folder holding lab.gguf.
func withBuiltIn(t *testing.T, s *Service) string {
	t.Helper()
	t.Setenv(builtin.EnvPath, fakeRunner(t))
	s.ModelsDir = t.TempDir()
	s.Runners = builtin.NewPool(nil)
	t.Cleanup(s.Runners.Stop)
	if err := os.WriteFile(filepath.Join(s.ModelsDir, "lab.gguf"), []byte("GGUF model bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(s.ModelsDir, "lab.gguf")
}

func builtInInput() TranslatorInput {
	return TranslatorInput{Kind: TranslatorBuiltIn, BuiltInModel: "file:lab.gguf"}
}

func TestBuiltInNeedsRunnerAndModel(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	ctx := context.Background()
	// No runner in this image: saved, but never ready.
	s.ModelsDir = t.TempDir()
	tr, err := s.SaveTranslator(ctx, builtInInput(), domain.System)
	if err != nil || tr.Kind != TranslatorBuiltIn || tr.Ready() || tr.BuiltInAvailable {
		t.Fatalf("no runner: %+v %v", tr, err)
	}
	withBuiltIn(t, s)
	tr = s.Translator(ctx)
	if !tr.Ready() || !tr.BuiltInAvailable || tr.Name() != "lab" || tr.BuiltInModel != "file:lab.gguf" {
		t.Fatalf("runner and model: %+v ready %v name %q", tr, tr.Ready(), tr.Name())
	}
}

func TestBuiltInTranslatesAFile(t *testing.T) {
	f := newFix(t)
	track := labMovie(t, f)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	withBuiltIn(t, s)
	ctx := context.Background()
	if _, err := s.SaveTranslator(ctx, builtInInput(), domain.System); err != nil {
		t.Fatal(err)
	}
	wishAll(t, f, Want{Language: "fr", TranslateFrom: "en"})
	res, err := s.Translate(ctx, f.movieFile, TranslateRequest{Language: "fr", Source: SourceBuiltIn, Track: track, ByHand: true}, domain.System)
	if err != nil || res.Skipped != "" || res.Partial {
		t.Fatalf("%+v %v", res, err)
	}
	src, _ := container.ExtractText(ctx, f.abs(movieRel), track)
	got := readCues(t, f.abs(res.Path))
	if len(got) != len(src) || got[0].Text != "T:"+src[0].Text {
		t.Fatalf("%d cues (source %d), first %q", len(got), len(src), got[0].Text)
	}
	rows, _ := ListFor(ctx, f.st.Read(), f.movieFile)
	named := false
	for _, r := range rows {
		if r.Place == "beside" && r.State == "present" && r.SiteName != nil && *r.SiteName == "lab" {
			named = true
		}
	}
	if !named {
		t.Fatalf("the saved subtitle is not named after the model: %+v", rows)
	}
}

func TestBuiltInModelGoneIsNotReady(t *testing.T) {
	f := newFix(t)
	track := labMovie(t, f)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	model := withBuiltIn(t, s)
	ctx := context.Background()
	if _, err := s.SaveTranslator(ctx, builtInInput(), domain.System); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(model); err != nil {
		t.Fatal(err)
	}
	if s.Translator(ctx).Ready() {
		t.Fatal("ready without its model file")
	}
	wishAll(t, f, Want{Language: "fr", TranslateFrom: "en"})
	_, err := s.Translate(ctx, f.movieFile, TranslateRequest{Language: "fr", Source: SourceBuiltIn, Track: track, ByHand: true}, domain.System)
	if !errors.Is(err, ErrNoModel) {
		t.Fatalf("err %v", err)
	}
	if res := s.TestTranslator(ctx); res.OK || res.Message == "" {
		t.Fatalf("test %+v", res)
	}
}

func TestBuiltInModelTruncated(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	model := withBuiltIn(t, s)
	ctx := context.Background()
	if _, err := s.SaveTranslator(ctx, builtInInput(), domain.System); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(model, 0); err != nil {
		t.Fatal(err)
	}
	if s.Translator(ctx).Ready() {
		t.Fatal("ready with an empty model file")
	}
}

func TestOwnFileIDsStayInsideModelsDir(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	withBuiltIn(t, s)
	ctx := context.Background()
	outside := filepath.Join(filepath.Dir(s.ModelsDir), "secret.gguf")
	_ = os.WriteFile(outside, []byte("x"), 0o644)
	if err := os.MkdirAll(filepath.Join(s.ModelsDir, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(s.ModelsDir, "a", "b.gguf"), []byte("x"), 0o644)
	_ = os.Symlink(outside, filepath.Join(s.ModelsDir, "link.gguf"))
	for _, id := range []string{"file:../secret.gguf", "file:../../holyarrs.db", "file:a/b.gguf", `file:a\b.gguf`, "file:x.txt", "file:", "file:.gguf", "lab.gguf", ""} {
		if _, err := s.SaveTranslator(ctx, TranslatorInput{Kind: TranslatorBuiltIn, BuiltInModel: id}, domain.System); err == nil {
			t.Fatalf("%q was accepted", id)
		}
		if _, err := s.modelFor(id); err == nil {
			t.Fatalf("%q resolved", id)
		}
	}
	// A link is a name inside the folder, but what it points at is not a model here.
	if _, err := s.modelFor("file:link.gguf"); err == nil {
		t.Fatal("a link out of the models folder resolved")
	}
}

func TestSwitchingToBuiltInKeepsTheOtherKindsFields(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	withBuiltIn(t, s)
	ctx := context.Background()
	before := s.Translator(ctx)
	if _, err := s.SaveTranslator(ctx, builtInInput(), domain.System); err != nil {
		t.Fatal(err)
	}
	mid := s.Translator(ctx)
	if mid.URL != before.URL || mid.Model != before.Model || !mid.HasKey {
		t.Fatalf("built in dropped the other kind's fields: %+v", mid)
	}
	back, err := s.SaveTranslator(ctx, TranslatorInput{Kind: TranslatorOpenAI, URL: mid.URL, Model: mid.Model, BuiltInModel: mid.BuiltInModel}, domain.System)
	if err != nil || !back.HasKey || !back.Ready() || back.BuiltInModel != "file:lab.gguf" {
		t.Fatalf("back to the AI service: %+v %v", back, err)
	}
}

func TestBuiltInSetsItsOwnChunk(t *testing.T) {
	if got := chunkFor(Translator{Kind: TranslatorBuiltIn}); got != builtin.Chunk {
		t.Fatalf("built in starts with %d lines, want %d", got, builtin.Chunk)
	}
	if got := chunkFor(Translator{Kind: TranslatorOpenAI}); got != 0 {
		t.Fatalf("other kinds keep the default, got %d", got)
	}
}

func TestThreadsBounds(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	withBuiltIn(t, s)
	ctx := context.Background()
	for _, n := range []int{-1, runtime.NumCPU() + 1} {
		in := builtInInput()
		in.Threads = n
		if _, err := s.SaveTranslator(ctx, in, domain.System); err == nil {
			t.Fatalf("%d threads accepted", n)
		}
	}
	in := builtInInput()
	in.Threads = 1
	if tr, err := s.SaveTranslator(ctx, in, domain.System); err != nil || tr.Threads != 1 {
		t.Fatalf("1 thread: %+v %v", tr, err)
	}
	if tr, err := s.SaveTranslator(ctx, builtInInput(), domain.System); err != nil || tr.Threads != 0 {
		t.Fatalf("0 keeps the default: %+v %v", tr, err)
	}
}

// The graphics card is a setting of the built-in kind: kept as chosen, refused
// when it is not a device id, and handed to the runner with a small memory need.
func TestGPUSettingIsKeptAndChecked(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	withBuiltIn(t, s)
	ctx := context.Background()
	in := builtInInput()
	in.GPU = "Vulkan1"
	tr, err := s.SaveTranslator(ctx, in, domain.System)
	if err != nil || tr.GPU != "Vulkan1" {
		t.Fatalf("%+v %v", tr, err)
	}
	c, _, err := s.translatorClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	o := c.(*builtin.Client).Targets[0].Opts
	if o.GPU != "Vulkan1" || o.NeedBytes > 1<<30 || o.CacheDir != filepath.Join(s.ModelsDir, ".cache") {
		t.Fatalf("options %+v", o)
	}
	for _, bad := range []string{"Vulkan1 --model /etc/passwd", "--device", "../x", "vulkan"} {
		in.GPU = bad
		if _, err := s.SaveTranslator(ctx, in, domain.System); err == nil {
			t.Fatalf("%q accepted as a graphics card", bad)
		}
	}
	in.GPU = ""
	if tr, err := s.SaveTranslator(ctx, in, domain.System); err != nil || tr.GPU != "" {
		t.Fatalf("back to the CPU: %+v %v", tr, err)
	}
	c, _, _ = s.translatorClient(ctx)
	if o := c.(*builtin.Client).Targets[0].Opts; o.GPU != "" || o.NeedBytes <= 1<<30 {
		t.Fatalf("CPU options %+v", o)
	}
	// The cards the runner sees are offered to the page.
	if devs := s.Devices(ctx); len(devs) != 2 || devs[0].ID != "Vulkan0" {
		t.Fatalf("devices %+v", devs)
	}
}
