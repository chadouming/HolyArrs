package subtitles

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/transcribe"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/faketranslator"
)

var sttServerOnce sync.Once

// withSpeechServer adds the stand-in for whisper.cpp's server, so transcripts
// go through it.
func withSpeechServer(t *testing.T, s *Service) string {
	t.Helper()
	sttServerOnce.Do(func() {
		if b, err := exec.Command("go", "build", "-o", filepath.Join(sttFakesDir, "whisper-server"), "./transcribe/fakes/whisper-server").CombinedOutput(); err != nil {
			t.Fatalf("building the server stand-in: %v\n%s", err, b)
		}
	})
	t.Setenv(transcribe.EnvServer, filepath.Join(sttFakesDir, "whisper-server"))
	state := t.TempDir()
	t.Setenv("FAKE_STATE", state)
	t.Cleanup(s.StopRunners)
	return state
}

// The settings for how many transcripts run at once on a card and for the
// quicker search are kept, and a number out of range is brought into it.
func TestSpeechSettingsAreKept(t *testing.T) {
	f := newFix(t)
	ctx := context.Background()
	set := ReadSettings(ctx, f.st.Read())
	if set.TranscribeAtOnce != DefaultTranscribeAtOnce || set.TranscribeFast {
		t.Fatalf("defaults %+v", set)
	}
	set.TranscribeAtOnce, set.TranscribeFast = 3, true
	if err := SaveSettings(ctx, f.st, set, domain.System); err != nil {
		t.Fatal(err)
	}
	if got := ReadSettings(ctx, f.st.Read()); got.TranscribeAtOnce != 3 || !got.TranscribeFast {
		t.Fatalf("read back %+v", got)
	}
	for n, want := range map[int]int{0: 1, MaxTranscribeAtOnce + 5: MaxTranscribeAtOnce} {
		set.TranscribeAtOnce = n
		if err := SaveSettings(ctx, f.st, set, domain.System); err != nil {
			t.Fatal(err)
		}
		if got := ReadSettings(ctx, f.st.Read()).TranscribeAtOnce; got != want {
			t.Fatalf("%d was kept as %d, want %d", n, got, want)
		}
	}
}

// Transcripts are spread over the cards, so many on each, and one more waits
// for a place. A place given back is taken by the one that waited.
func TestSpeechPlacesSpreadOverTheCards(t *testing.T) {
	var p speechPlaces
	ctx := context.Background()
	cards := []int{0, 1}
	var held []func()
	seen := map[int]int{}
	servers := map[*transcribe.Server]bool{}
	for i := 0; i < 4; i++ {
		dev, srv, release, err := p.take(ctx, cards, 2)
		if err != nil {
			t.Fatal(err)
		}
		seen[dev]++
		servers[srv] = true
		held = append(held, release)
	}
	if seen[0] != 2 || seen[1] != 2 || len(servers) != 4 {
		t.Fatalf("cards %v, %d servers", seen, len(servers))
	}
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	if _, _, _, err := p.take(short, cards, 2); err == nil {
		t.Fatal("a fifth transcript got a place")
	}
	cancel()
	got := make(chan int, 1)
	go func() {
		dev, _, release, err := p.take(ctx, cards, 2)
		if err == nil {
			release()
			got <- dev
		}
	}()
	held[1]() // the place on card 1
	select {
	case dev := <-got:
		if dev != 1 {
			t.Fatalf("the freed place is on card 1, got %d", dev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the waiting transcript never got the freed place")
	}
	// With no card there is one place, on the CPU.
	var q speechPlaces
	dev, _, release, err := q.take(ctx, nil, 3)
	if err != nil || dev != -1 {
		t.Fatalf("no card: %d %v", dev, err)
	}
	short, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, _, _, err := q.take(short, nil, 3); err == nil {
		t.Fatal("two transcripts at once on the CPU")
	}
	release()
	for _, r := range held {
		r()
	}
	p.stop()
	q.stop()
}

// With the server in the image a file's language checks and transcript use
// one loaded model, and so does the next file; the quicker search is asked
// for when its setting is on.
func TestTranscribeGoesThroughTheSpeechServer(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	withTranscriber(t, f, s, "en,en,en", "eng")
	state := withSpeechServer(t, s)
	fields := filepath.Join(t.TempDir(), "fields.json")
	t.Setenv("FAKE_FIELDS_FILE", fields)
	ctx := context.Background()
	wishAll(t, f, Want{Language: "en"})
	set := ReadSettings(ctx, f.st.Read())
	set.TranscribeFast = true
	if err := SaveSettings(ctx, f.st, set, domain.System); err != nil {
		t.Fatal(err)
	}
	res, err := s.Transcribe(ctx, f.movieFile, true, domain.System)
	if err != nil || res.Lines != 6 || res.Language != "en" {
		t.Fatalf("%+v %v", res, err)
	}
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(state, name))
		return strings.TrimSpace(string(b))
	}
	if read("starts") != "1" || read("checks") != "2" || read("transcripts") != "1" {
		t.Fatalf("starts %s checks %s transcripts %s", read("starts"), read("checks"), read("transcripts"))
	}
	b, _ := os.ReadFile(fields)
	if !strings.Contains(string(b), `"beam_size":"1"`) {
		t.Fatalf("the quicker search was not asked for: %s", b)
	}
}

func TestTakeExtraSingleActiveJobBorrowsAllIdleSlots(t *testing.T) {
	var p speechPlaces
	defer p.stop()
	ctx := context.Background()
	cards := []int{0, 1}
	done := p.enter()
	defer done()

	_, _, release, err := p.take(ctx, cards, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	extra, releaseExtra := p.takeExtra(cards, 3, len(cards)*3-1)
	defer releaseExtra()
	if len(extra) != 5 {
		t.Fatalf("borrowed %d extra slots, want 5", len(extra))
	}
}

func TestTakeExtraFullConcurrencyPreventsStarvation(t *testing.T) {
	var p speechPlaces
	defer p.stop()
	ctx := context.Background()
	cards := []int{0, 1}
	dones := make([]func(), 6)
	for i := range dones {
		dones[i] = p.enter()
		defer dones[i]()
	}

	var releases []func()
	for i := 0; i < 6; i++ {
		short, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		_, _, release, err := p.take(short, cards, 3)
		cancel()
		if err != nil {
			t.Fatalf("job %d failed to acquire primary slot: %v", i, err)
		}
		releases = append(releases, release)
		if i == 0 {
			extra, releaseExtra := p.takeExtra(cards, 3, len(cards)*3-1)
			if len(extra) != 0 {
				releaseExtra()
				t.Fatalf("borrowed %d extra slots under full concurrency, want 0", len(extra))
			}
			releaseExtra()
		}
	}
	for _, r := range releases {
		r()
	}
}

func TestTakeExtraFairShareAcrossTwoActiveJobs(t *testing.T) {
	var p speechPlaces
	defer p.stop()
	ctx := context.Background()
	cards := []int{0, 1}
	done1 := p.enter()
	defer done1()
	done2 := p.enter()
	defer done2()

	_, _, rel1, err := p.take(ctx, cards, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer rel1()
	extra1, relExtra1 := p.takeExtra(cards, 3, len(cards)*3-1)
	defer relExtra1()

	_, _, rel2, err := p.take(ctx, cards, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer rel2()
	extra2, relExtra2 := p.takeExtra(cards, 3, len(cards)*3-1)
	defer relExtra2()

	if len(extra1) != 2 || len(extra2) != 2 {
		t.Fatalf("fair share extra slots = (%d, %d), want (2, 2)", len(extra1), len(extra2))
	}
}

func TestTakeExtraDynamicYieldUnblocksWaitingJob(t *testing.T) {
	var p speechPlaces
	defer p.stop()
	ctx := context.Background()
	cards := []int{0, 1}

	done1 := p.enter()
	defer done1()
	_, _, rel1, err := p.take(ctx, cards, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer rel1()
	extra, relExtra := p.takeExtra(cards, 3, len(cards)*3-1)
	defer relExtra()
	if len(extra) != 5 {
		t.Fatalf("borrowed %d extra slots, want 5", len(extra))
	}
	if extra[0].ShouldYield() {
		t.Fatal("ShouldYield reported true before any second job arrived")
	}

	done2 := p.enter()
	defer done2()
	acquired := make(chan error, 1)
	go func() {
		_, _, rel2, err := p.take(ctx, cards, 3)
		if err == nil {
			rel2()
		}
		acquired <- err
	}()

	deadline := time.Now().Add(2 * time.Second)
	for !extra[0].ShouldYield() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !extra[0].ShouldYield() {
		t.Fatal("ShouldYield stayed false while job 2 was waiting")
	}
	extra[0].Release()

	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("job 2 take failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("job 2 remained blocked after extra[0].Release()")
	}
}
