package builtin

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

var fake string // the built fakerunner

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakerunner")
	if err != nil {
		panic(err)
	}
	fake = filepath.Join(dir, "fakerunner")
	if out, err := exec.Command("go", "build", "-o", fake, "./fakerunner").CombinedOutput(); err != nil {
		panic(fmt.Sprintf("building fakerunner: %v\n%s", err, out))
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func opts(model string) Options {
	return Options{Binary: fake, ModelPath: model, Threads: 1, Context: 512}
}

func newRunner(t *testing.T) *Runner {
	t.Helper()
	r := NewRunner(func(string) {})
	t.Cleanup(r.Stop)
	return r
}

func alive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }

func gone(t *testing.T, pid int) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if !alive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d is still there", pid)
}

func TestEnsureStartsOnLoopbackAndReuses(t *testing.T) {
	r := newRunner(t)
	base, key, err := r.Ensure(context.Background(), opts("a.gguf"))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(base)
	if u.Hostname() != "127.0.0.1" || len(key) < 32 {
		t.Fatalf("base %q key %q", base, key)
	}
	pid := r.pid()
	base2, key2, err := r.Ensure(context.Background(), opts("a.gguf"))
	if err != nil || base2 != base || key2 != key || r.pid() != pid {
		t.Fatalf("second Ensure: %q %q pid %d (was %d): %v", base2, key2, r.pid(), pid, err)
	}
}

func TestEnsureRestartsForAnotherModel(t *testing.T) {
	r := newRunner(t)
	_, key, err := r.Ensure(context.Background(), opts("a.gguf"))
	if err != nil {
		t.Fatal(err)
	}
	pid := r.pid()
	r.Used()
	_, key2, err := r.Ensure(context.Background(), opts("b.gguf"))
	if err != nil || key2 == key || r.pid() == pid {
		t.Fatalf("another model: key changed %v, pid %d (was %d): %v", key2 != key, r.pid(), pid, err)
	}
	gone(t, pid)
}

func TestIdleStop(t *testing.T) {
	old := IdleStop
	IdleStop = 50 * time.Millisecond
	defer func() { IdleStop = old }()
	r := newRunner(t)
	if _, _, err := r.Ensure(context.Background(), opts("a.gguf")); err != nil {
		t.Fatal(err)
	}
	pid := r.pid()
	time.Sleep(150 * time.Millisecond)
	if !r.Running() {
		t.Fatal("a call still in flight must keep the runner")
	}
	r.Used()
	gone(t, pid)
	// The process is gone a moment before the Runner has noted it.
	for i := 0; i < 200 && r.Running(); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if r.Running() {
		t.Fatal("still running after the idle stop")
	}
	if _, _, err := r.Ensure(context.Background(), opts("a.gguf")); err != nil || !r.Running() {
		t.Fatalf("start again: %v", err)
	}
}

func TestUsedPostponesIdleStop(t *testing.T) {
	old := IdleStop
	IdleStop = 300 * time.Millisecond
	defer func() { IdleStop = old }()
	r := newRunner(t)
	for i := 0; i < 4; i++ {
		if _, _, err := r.Ensure(context.Background(), opts("a.gguf")); err != nil {
			t.Fatal(err)
		}
		r.Used()
		time.Sleep(150 * time.Millisecond)
		if !r.Running() {
			t.Fatalf("stopped after call %d though each call restarts the wait", i)
		}
	}
}

func TestStartFailureCarriesLog(t *testing.T) {
	t.Setenv("FAKE_EXIT_AFTER", "0")
	t.Setenv("FAKE_LOG", "bad model")
	r := newRunner(t)
	start := time.Now()
	_, _, err := r.Ensure(context.Background(), opts("a.gguf"))
	if err == nil || !strings.Contains(err.Error(), "bad model") {
		t.Fatalf("err %v", err)
	}
	if time.Since(start) > 10*time.Second || r.Running() {
		t.Fatalf("took %s, running %v", time.Since(start), r.Running())
	}
	// The next call starts cleanly.
	t.Setenv("FAKE_EXIT_AFTER", "")
	if _, _, err := r.Ensure(context.Background(), opts("a.gguf")); err != nil {
		t.Fatalf("next call: %v", err)
	}
}

func TestNeverHealthyHitsStartLimit(t *testing.T) {
	old := StartLimit
	StartLimit = 200 * time.Millisecond
	defer func() { StartLimit = old }()
	t.Setenv("FAKE_NEVER_HEALTHY", "1")
	r := newRunner(t)
	_, _, err := r.Ensure(context.Background(), opts("a.gguf"))
	if err == nil {
		t.Fatal("no error")
	}
	if r.Running() || alive(r.lastPid()) {
		t.Fatal("a process was left")
	}
}

func TestStopKillsTheProcess(t *testing.T) {
	r := newRunner(t)
	if _, _, err := r.Ensure(context.Background(), opts("a.gguf")); err != nil {
		t.Fatal(err)
	}
	pid := r.pid()
	r.Stop()
	gone(t, pid)

	// Also while Ensure still waits for the health check.
	t.Setenv("FAKE_NEVER_HEALTHY", "1")
	done := make(chan error, 1)
	go func() { _, _, err := r.Ensure(context.Background(), opts("a.gguf")); done <- err }()
	for i := 0; i < 200 && r.pid() == 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	pid = r.pid()
	if pid == 0 {
		t.Fatal("never started")
	}
	r.Stop()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Ensure succeeded though stopped")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ensure did not return after Stop")
	}
	gone(t, pid)
}

func TestEnsureRefusesWithoutMemory(t *testing.T) {
	if FreeMemory() == 0 {
		t.Skip("free memory is not known on this system")
	}
	r := newRunner(t)
	o := opts("a.gguf")
	o.NeedBytes = math.MaxUint64 / 2
	_, _, err := r.Ensure(context.Background(), o)
	if !errors.Is(err, ErrNoMemory) || r.Running() || r.lastPid() != 0 {
		t.Fatalf("err %v running %v pid %d", err, r.Running(), r.lastPid())
	}
}

func TestFindUsesEnv(t *testing.T) {
	t.Setenv(EnvPath, fake)
	if p, ok := Find(); !ok || p != fake {
		t.Fatalf("%q %v", p, ok)
	}
	t.Setenv(EnvPath, filepath.Join(t.TempDir(), "nothing"))
	if _, ok := Find(); ok {
		t.Fatal("a missing file was found")
	}
	if DefaultThreads() < 1 {
		t.Fatal("threads")
	}
}

// More threads than a handful bring nothing on a machine that is also doing
// other work (measured in the trial), so the default stays small.
func TestDefaultThreadsIsCapped(t *testing.T) {
	if n := DefaultThreads(); n < 1 || n > MaxDefaultThreads || n > runtime.NumCPU() {
		t.Fatalf("%d default threads on %d cores", n, runtime.NumCPU())
	}
}

func argsOf(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return " " + strings.TrimSpace(string(b)) + " "
}

// The graphics card chosen in Settings reaches the runner; with none chosen
// the runner is told to stay on the CPU, whatever cards it can see.
func TestGPUOptionsReachTheRunner(t *testing.T) {
	file := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_ARGS_FILE", file)
	r := newRunner(t)
	o := opts("a.gguf")
	if _, _, err := r.Ensure(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if a := argsOf(t, file); !strings.Contains(a, " --gpu-layers 0 ") || strings.Contains(a, "--device") {
		t.Fatalf("CPU args: %s", a)
	}
	r.Used()
	o.GPU = "Vulkan1"
	o.CacheDir = t.TempDir()
	if _, _, err := r.Ensure(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	a := argsOf(t, file)
	if !strings.Contains(a, " --gpu-layers 999 ") || !strings.Contains(a, " --device Vulkan1 ") || !strings.Contains(a, " XDG_CACHE_HOME="+o.CacheDir+" ") {
		t.Fatalf("GPU args: %s", a)
	}
}

func TestDevicesParsesTheList(t *testing.T) {
	got, err := Devices(context.Background(), fake)
	if err != nil || len(got) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	want := Device{ID: "Vulkan1", Name: "Intel(R) Arc(tm) B580 Graphics (BMG G21)", MemoryMB: 12216, FreeMB: 10375}
	if got[1] != want || got[0].ID != "Vulkan0" {
		t.Fatalf("got %+v", got)
	}
	t.Setenv("FAKE_NO_DEVICES", "1")
	if got, err := Devices(context.Background(), fake); err != nil || len(got) != 0 {
		t.Fatalf("no cards: %+v %v", got, err)
	}
	if _, err := Devices(context.Background(), filepath.Join(t.TempDir(), "nothing")); err == nil {
		t.Fatal("a missing runner listed devices")
	}
}

func TestGPUFlashAttnAndQuantKVFlags(t *testing.T) {
	file := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_ARGS_FILE", file)
	r := newRunner(t)
	o := opts("a.gguf")
	o.GPU = "Vulkan0"
	o.FlashAttn = true
	o.QuantKV = true
	if _, _, err := r.Ensure(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	a := argsOf(t, file)
	for _, want := range []string{" --flash-attn on ", " --cache-type-k q8_0 ", " --cache-type-v q8_0 "} {
		if !strings.Contains(a, want) {
			t.Fatalf("args lack %q: %s", want, a)
		}
	}
	r.Used()

	t.Setenv("HOLYARRS_LLAMA_NO_FLASH_ATTN", "1")
	o.ModelPath = "b.gguf" // force restart
	if _, _, err := r.Ensure(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	a = argsOf(t, file)
	if strings.Contains(a, "--flash-attn") || strings.Contains(a, "--cache-type-k") {
		t.Fatalf("HOLYARRS_LLAMA_NO_FLASH_ATTN did not disable flags: %s", a)
	}
}

func TestGPUParallelFor(t *testing.T) {
	if got := GPUParallelFor(0, 3<<30); got != GPUParallel {
		t.Fatalf("unknown memory = %d, want %d", got, GPUParallel)
	}
	small := []struct {
		mb, want int
	}{{12216, 8}, {8192, 6}, {6144, 4}, {4096, 2}}
	for _, c := range small {
		if got := GPUParallelFor(c.mb, 3<<30); got != c.want {
			t.Fatalf("small model %d MB = %d, want %d", c.mb, got, c.want)
		}
	}
	large := []struct {
		mb, want int
	}{{12216, 6}, {8192, 4}, {6144, 2}, {4096, 1}}
	for _, c := range large {
		if got := GPUParallelFor(c.mb, 7<<30); got != c.want {
			t.Fatalf("large model %d MB = %d, want %d", c.mb, got, c.want)
		}
	}
}

func TestRunnerStopIfIdle(t *testing.T) {
	r := newRunner(t)
	if r.StopIfIdle() {
		t.Fatal("StopIfIdle returned true before runner started")
	}
	if _, _, err := r.Ensure(context.Background(), opts("a.gguf")); err != nil {
		t.Fatal(err)
	}
	pid := r.pid()
	if r.StopIfIdle() {
		t.Fatal("StopIfIdle stopped a busy runner")
	}
	if !r.Running() {
		t.Fatal("runner should still be running while busy")
	}
	r.Used()
	if !r.StopIfIdle() {
		t.Fatal("StopIfIdle did not stop an idle runner")
	}
	gone(t, pid)
}
