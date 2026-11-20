package builtin

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate"
)

// Several slots and a model told not to think reach the runner: the context
// is per slot, so the runner gets it times the slots.
func TestParallelAndNoThinkReachTheRunner(t *testing.T) {
	file := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_ARGS_FILE", file)
	r := newRunner(t)
	o := opts("a.gguf")
	o.GPU, o.Parallel, o.Context, o.NoThink = "Vulkan0", 8, 4096, true
	if _, _, err := r.Ensure(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	a := argsOf(t, file)
	for _, want := range []string{" --parallel 8 ", " --ctx-size 32768 ", " --reasoning-budget 0 "} {
		if !strings.Contains(a, want) {
			t.Fatalf("args lack %q: %s", want, a)
		}
	}
	r.Used()
	// One slot and a model that may think: neither is said.
	o.Parallel, o.NoThink = 0, false
	if _, _, err := r.Ensure(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if a := argsOf(t, file); !strings.Contains(a, " --parallel 1 ") || !strings.Contains(a, " --ctx-size 4096 ") || strings.Contains(a, "--reasoning-budget") {
		t.Fatalf("plain args: %s", a)
	}
}

// The pool keeps one runner for each graphics card, and stops them all.
func TestPoolKeepsOneRunnerPerDevice(t *testing.T) {
	p := NewPool(nil)
	t.Cleanup(p.Stop)
	a, b := p.Runner("Vulkan0"), p.Runner("Vulkan1")
	if a == b || p.Runner("Vulkan0") != a || p.Runner("") == a {
		t.Fatal("runners are not one per device")
	}
	for _, r := range []*Runner{a, b} {
		if _, _, err := r.Ensure(context.Background(), opts("a.gguf")); err != nil {
			t.Fatal(err)
		}
	}
	pa, pb := a.pid(), b.pid()
	if pa == 0 || pb == 0 || pa == pb || !p.Running() {
		t.Fatalf("pids %d %d running %v", pa, pb, p.Running())
	}
	p.Stop()
	gone(t, pa)
	gone(t, pb)
	if p.Running() {
		t.Fatal("still running after Stop")
	}
}

// With a runner for each card, calls made at the same time are spread over
// them, and the client says how many calls it can take at once.
func TestClientSpreadsCallsOverTargets(t *testing.T) {
	p := NewPool(nil)
	t.Cleanup(p.Stop)
	o0, o1 := opts("a.gguf"), opts("a.gguf")
	o0.GPU, o0.Parallel, o1.GPU, o1.Parallel = "Vulkan0", 8, "Vulkan1", 8
	c := &Client{Name: "lab", Targets: []Target{{Runner: p.Runner("Vulkan0"), Opts: o0}, {Runner: p.Runner("Vulkan1"), Opts: o1}}}
	if c.Slots() != 16 {
		t.Fatalf("%d slots, want 16", c.Slots())
	}
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := c.Translate(context.Background(), batch())
			if err == nil && (len(got) != 2 || got[0].Text != "T:Good morning.") {
				err = &translate.Error{Kind: translate.KindUnreadable, Msg: "wrong lines"}
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if p.Runner("Vulkan0").pid() == 0 || p.Runner("Vulkan1").pid() == 0 {
		t.Fatal("one of the two cards was never used")
	}
	// The old single-runner form still counts as one target.
	one := &Client{Runner: newRunner(t), Opts: opts("a.gguf"), Name: "lab"}
	if one.Slots() != 1 {
		t.Fatalf("single runner: %d slots", one.Slots())
	}
}

// A card that cannot start its runner does not draw the calls to itself: they
// go to the card that works, and only when no card works is the call refused.
func TestClientPassesOverACardThatFails(t *testing.T) {
	if FreeMemory() == 0 {
		t.Skip("free memory is not known on this system")
	}
	p := NewPool(nil)
	t.Cleanup(p.Stop)
	bad, good := opts("a.gguf"), opts("a.gguf")
	bad.GPU, bad.Parallel, bad.NeedBytes = "Vulkan0", 8, math.MaxUint64/2
	good.GPU, good.Parallel = "Vulkan1", 8
	c := &Client{Name: "lab", Targets: []Target{{Runner: p.Runner("Vulkan0"), Opts: bad}, {Runner: p.Runner("Vulkan1"), Opts: good}}}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.Translate(context.Background(), batch())
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a call failed though one card works: %v", err)
		}
	}
	if p.Runner("Vulkan0").Running() {
		t.Fatal("the card without memory was started")
	}
	// No card works: refused, so the run ends at once.
	good.NeedBytes = math.MaxUint64 / 2
	c = &Client{Name: "lab", Targets: []Target{{Runner: p.Runner("Vulkan0"), Opts: bad}, {Runner: p.Runner("Vulkan1"), Opts: good}}}
	if _, err := c.Translate(context.Background(), batch()); translate.KindOf(err) != translate.KindRefused {
		t.Fatalf("no card works: %v", err)
	}
}
