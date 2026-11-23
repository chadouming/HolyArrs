package subtitles

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/builtin"
)

func writeSlowDeviceProbeScript(t *testing.T, dir string) (scriptPath, startedFile, releaseFile, countFile string) {
	t.Helper()
	scriptPath = filepath.Join(dir, "fake-llama-server.sh")
	startedFile = filepath.Join(dir, "started")
	releaseFile = filepath.Join(dir, "release")
	countFile = filepath.Join(dir, "count")

	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--list-devices" ]; then
  printf 'x' >> %q
  : > %q
  i=0
  while [ ! -f %q ] && [ "$i" -lt 500 ]; do
    sleep 0.01
    i=$((i + 1))
  done
  printf 'Available devices:\n'
  printf '  Vulkan0: Test Card New (12216 MiB, 11000 MiB free)\n'
  exit 0
fi
exit 1
`, countFile, startedFile, releaseFile)

	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return scriptPath, startedFile, releaseFile, countFile
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func TestDevices_StaleWhileRevalidate(t *testing.T) {
	dir := t.TempDir()
	scriptPath, startedFile, releaseFile, countFile := writeSlowDeviceProbeScript(t, dir)
	t.Setenv(builtin.EnvPath, scriptPath)

	pool := builtin.NewPool(nil)
	defer pool.Stop()

	s := &Service{
		Runners:   pool,
		devices:   []builtin.Device{{ID: "Vulkan0", Name: "Stale Card", MemoryMB: 8192, FreeMB: 4000}},
		devicesAt: time.Now().Add(-2 * devicesFor),
	}

	ctx := context.Background()
	start := time.Now()
	got := s.Devices(ctx)
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("expired warm cache blocked for %v", elapsed)
	}
	if len(got) != 1 || got[0].Name != "Stale Card" {
		t.Fatalf("got %+v, want Stale Card", got)
	}

	waitForFile(t, startedFile, 2*time.Second)

	// A second call while the background refresh is running must also return immediately
	// and must not spawn another probe.
	got2 := s.Devices(ctx)
	if len(got2) != 1 || got2[0].Name != "Stale Card" {
		t.Fatalf("got2 %+v, want Stale Card", got2)
	}

	if err := os.WriteFile(releaseFile, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cur := s.Devices(ctx)
		if len(cur) == 1 && cur[0].Name == "Test Card New" {
			cnt, err := os.ReadFile(countFile)
			if err != nil {
				t.Fatal(err)
			}
			if len(cnt) != 1 {
				t.Fatalf("expected 1 background probe, got %d", len(cnt))
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("devices did not refresh asynchronously")
}

func TestDevices_ColdStartSingleflight(t *testing.T) {
	dir := t.TempDir()
	scriptPath, startedFile, releaseFile, countFile := writeSlowDeviceProbeScript(t, dir)
	t.Setenv(builtin.EnvPath, scriptPath)

	pool := builtin.NewPool(nil)
	defer pool.Stop()

	s := &Service{Runners: pool}

	const callers = 3
	results := make([][]builtin.Device, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = s.Devices(context.Background())
		}()
	}

	waitForFile(t, startedFile, 2*time.Second)
	time.Sleep(30 * time.Millisecond)

	if err := os.WriteFile(releaseFile, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	wg.Wait()

	for i, res := range results {
		if len(res) != 1 || res[0].Name != "Test Card New" {
			t.Fatalf("caller %d got %+v, want Test Card New", i, res)
		}
	}
	cnt, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(cnt) != 1 {
		t.Fatalf("expected 1 cold probe across %d concurrent callers, got %d", callers, len(cnt))
	}
}
