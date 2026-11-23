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

// Two separate Clients sharing the same Pool load-balance across GPUs via
// Runner.Load and Reserve instead of piling onto Vulkan0.
func TestClientsSharePoolLoadAcrossGPUs(t *testing.T) {
	p := NewPool(nil)
	t.Cleanup(p.Stop)
	r0, r1 := p.Runner("Vulkan0"), p.Runner("Vulkan1")
	o0, o1 := opts("a.gguf"), opts("a.gguf")
	o0.GPU, o0.Parallel = "Vulkan0", 4
	o1.GPU, o1.Parallel = "Vulkan1", 4
	targets := []Target{{Runner: r0, Opts: o0}, {Runner: r1, Opts: o1}}
	c1 := &Client{Name: "lab", Targets: targets}
	c2 := &Client{Name: "lab", Targets: targets}

	i1, _, done1, ok1 := c1.take(nil)
	if !ok1 || i1 != 0 {
		t.Fatalf("c1 first take = (%d, %v), want (0, true)", i1, ok1)
	}
	if r0.Load() != 1 || r1.Load() != 0 {
		t.Fatalf("loads after c1 take = (%d, %d), want (1, 0)", r0.Load(), r1.Load())
	}

	i2, _, done2, ok2 := c2.take(nil)
	if !ok2 || i2 != 1 {
		t.Fatalf("c2 take = (%d, %v), want (1, true) to balance off Vulkan0", i2, ok2)
	}
	if r0.Load() != 1 || r1.Load() != 1 {
		t.Fatalf("loads after c2 take = (%d, %d), want (1, 1)", r0.Load(), r1.Load())
	}

	// Releasing done1 twice must be idempotent.
	done1()
	done1()
	if r0.Load() != 0 || r1.Load() != 1 {
		t.Fatalf("loads after done1 = (%d, %d), want (0, 1)", r0.Load(), r1.Load())
	}

	i3, _, done3, ok3 := c2.take(nil)
	if !ok3 || i3 != 0 {
		t.Fatalf("c2 second take = (%d, %v), want (0, true)", i3, ok3)
	}
	done2()
	done3()
	if r0.Load() != 0 || r1.Load() != 0 {
		t.Fatalf("loads after releasing all = (%d, %d), want (0, 0)", r0.Load(), r1.Load())
	}

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for _, c := range []*Client{c1, c2, c1, c2} {
		wg.Add(1)
		go func(cl *Client) {
			defer wg.Done()
			_, err := cl.Translate(context.Background(), batch())
			errs <- err
		}(c)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if r0.Load() != 0 || r1.Load() != 0 {
		t.Fatalf("loads after concurrent Translate = (%d, %d), want (0, 0)", r0.Load(), r1.Load())
	}
	if r0.pid() == 0 || r1.pid() == 0 {
		t.Fatalf("expected both runners to be used: pids (%d, %d)", r0.pid(), r1.pid())
	}
}
