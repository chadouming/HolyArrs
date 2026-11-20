package transcribe

import (
	"context"
	"os"
	"strings"
	"testing"
)

// A run tells how far it is: the sound is read, the language is checked, and
// then Whisper's own percent as it prints it. It never goes backwards.
func TestRunTellsItsProgress(t *testing.T) {
	o := setup(t, "en,en,en", "eng")
	var got []float64
	o.Progress = func(p float64) { got = append(got, p) }
	var logged []string
	o.Log = func(l string) { logged = append(logged, l) }
	if _, err := Run(context.Background(), "video.mkv", Track{Index: 0, Language: "eng", Seconds: 600}, "eng", o); err != nil {
		t.Fatal(err)
	}
	if len(got) < 4 {
		t.Fatalf("progress told %v", got)
	}
	// The percent lines go to the listener only, not into the log.
	for _, l := range logged {
		if strings.Contains(l, "progress =") {
			t.Fatalf("a progress line was logged: %q", l)
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i] < got[i-1] {
			t.Fatalf("progress went backwards: %v", got)
		}
	}
	if got[0] <= 0 || got[len(got)-1] < 0.9 || got[len(got)-1] > 1 {
		t.Fatalf("progress %v", got)
	}
	// Whisper's 50% is somewhere in the middle of the run.
	mid := false
	for _, p := range got {
		mid = mid || (p > 0.4 && p < 0.7)
	}
	if !mid {
		t.Fatalf("Whisper's own percent was not told: %v", got)
	}
}

// Without a listener the program is not asked to print its progress.
func TestRunAsksForProgressOnlyWhenListened(t *testing.T) {
	o := setup(t, "en,en,en", "eng")
	args := t.TempDir() + "/args.json"
	t.Setenv("FAKE_ARGS_FILE", args)
	if _, err := Run(context.Background(), "video.mkv", Track{Index: 0, Language: "eng", Seconds: 600}, "eng", o); err != nil {
		t.Fatal(err)
	}
	for _, a := range readArgs(t, args) {
		if a == "-pp" {
			t.Fatal("-pp given with no listener")
		}
	}
}

// With a card, the speech program is told where the graphics driver may keep
// what it compiles, so the next file does not wait for it again. On the CPU
// there is nothing to keep.
func TestRunGivesTheDriverACacheFolderOnACard(t *testing.T) {
	for _, dev := range []int{0, -1} {
		o := setup(t, "en,en,en", "eng")
		o.Device, o.CacheDir = dev, t.TempDir()
		envFile := t.TempDir() + "/env"
		t.Setenv("FAKE_ENV_FILE", envFile)
		if _, err := Run(context.Background(), "video.mkv", Track{Index: 0, Language: "eng", Seconds: 600}, "eng", o); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(envFile)
		if want := map[int]string{0: o.CacheDir, -1: ""}[dev]; string(b) != want {
			t.Fatalf("device %d: XDG_CACHE_HOME %q, want %q", dev, b, want)
		}
	}
}
