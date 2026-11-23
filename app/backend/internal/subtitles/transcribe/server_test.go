package transcribe

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withServer gives o a speech server made from the fake, and the folder the
// fake keeps its counts in.
func withServer(t *testing.T, o *Options) string {
	t.Helper()
	state := t.TempDir()
	t.Setenv("FAKE_STATE", state)
	o.Programs.Server = filepath.Join(fakeDir, "whisper-server")
	o.Server = NewServer(nil)
	t.Cleanup(o.Server.Stop)
	return state
}

func count(t *testing.T, state, name string) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(state, name))
	return strings.TrimSpace(string(b))
}

// With a speech server the model is loaded once: the three language checks
// and the transcript are four calls to one process, and a second file is
// four more calls to the same one.
func TestServerKeepsTheModelLoadedAcrossCallsAndFiles(t *testing.T) {
	o := setup(t, "en,en,en", "eng")
	state := withServer(t, &o)
	for i := 0; i < 2; i++ {
		res, err := Run(context.Background(), "video.mkv", Track{Index: 0, Language: "eng", Seconds: 1200}, "en", o)
		if err != nil {
			t.Fatal(err)
		}
		if res.Language != "en" || len(res.Cues) != 6 || res.Cues[1].StartMS != 3000 || res.Cues[1].EndMS != 5500 || res.Cues[0].Text != "Line 1 of the fake transcript." {
			t.Fatalf("result %+v", res)
		}
	}
	if count(t, state, "starts") != "1" || count(t, state, "checks") != "4" || count(t, state, "transcripts") != "2" {
		t.Fatalf("starts %s, checks %s, transcripts %s", count(t, state, "starts"), count(t, state, "checks"), count(t, state, "transcripts"))
	}
	empty(t, o.TempDir)
}

// The server is asked for what the one-shot program was: the language, voice
// detection, a time for every word, the names once, and a five-way search.
// Fast asks for a single candidate.
func TestServerRequestSaysWhatTheProgramWasTold(t *testing.T) {
	for _, fast := range []bool{false, true} {
		o := setup(t, "fr,fr,fr", "fre")
		state := withServer(t, &o)
		o.Prompt, o.Fast = "Aoi Kisaragi, Wind Blade", fast
		ff := filepath.Join(t.TempDir(), "fields.json")
		dlf := filepath.Join(t.TempDir(), "dl-fields.json")
		t.Setenv("FAKE_FIELDS_FILE", ff)
		t.Setenv("FAKE_DL_FIELDS_FILE", dlf)
		if _, err := Run(context.Background(), "video.mkv", Track{Index: 0, Language: "fre", Seconds: 600}, "fr", o); err != nil {
			t.Fatal(err)
		}
		var f, dl map[string]string
		b, _ := os.ReadFile(ff)
		_ = json.Unmarshal(b, &f)
		dlb, _ := os.ReadFile(dlf)
		_ = json.Unmarshal(dlb, &dl)
		beam := map[bool]string{false: "5", true: "1"}[fast]
		tempInc := map[bool]string{false: "0.4", true: "0.0"}[fast]
		if f["language"] != "fr" || f["response_format"] != "verbose_json" || f["vad"] != "true" || f["token_timestamps"] != "true" ||
			f["prompt"] != o.Prompt || f["beam_size"] != beam || f["best_of"] != beam || f["no_language_probabilities"] != "true" ||
			f["temperature_inc"] != tempInc {
			t.Fatalf("fast %v: fields %v", fast, f)
		}
		if dl["vad"] != "true" || dl["detect_language"] != "true" {
			t.Fatalf("detect_language fields %v", dl)
		}
		var args []string
		ab, _ := os.ReadFile(filepath.Join(state, "server-args"))
		_ = json.Unmarshal(ab, &args)
		if valueAfter(args, "-vmsd") != "20" {
			t.Fatalf("server args missing -vmsd 20: %v", args)
		}
	}
}

// The server's log places the words in the video and tells the progress, as
// the one-shot program's did.
func TestServerPlacesWordsAndTellsProgress(t *testing.T) {
	o := setup(t, "en,en,en", "eng")
	withServer(t, &o)
	t.Setenv("FAKE_LONG", "straddle")
	t.Setenv("FAKE_VAD", "10.00,10.50,0.00,0.50;196.00,240.00,0.60,44.60")
	t.Setenv("FAKE_TOKEN_BASE", "1000")
	var got []float64
	o.Progress = func(p float64) { got = append(got, p) }
	res, err := Run(context.Background(), "video.mkv", Track{Seconds: 1200}, "en", o)
	if err != nil || len(res.Cues) < 4 {
		t.Fatalf("%d cues %v", len(res.Cues), err)
	}
	if res.Cues[0].StartMS != 196400 {
		t.Fatalf("the first line starts at %d ms, want 196400", res.Cues[0].StartMS)
	}
	if len(got) < 4 || got[len(got)-1] < 0.9 {
		t.Fatalf("progress %v", got)
	}
}

// A second transcript never reads the log lines of the one before it.
func TestServerLogOfOneFileIsNotReadForTheNext(t *testing.T) {
	o := setup(t, "en,en,en", "eng")
	withServer(t, &o)
	t.Setenv("FAKE_LONG", "straddle")
	t.Setenv("FAKE_VAD", "10.00,10.50,0.00,0.50;196.00,240.00,0.60,44.60")
	t.Setenv("FAKE_TOKEN_BASE", "1000")
	for i := 0; i < 2; i++ {
		res, err := Run(context.Background(), "video.mkv", Track{Seconds: 1200}, "en", o)
		if err != nil || res.Cues[0].StartMS != 196400 {
			t.Fatalf("run %d: %v %+v", i, err, res.Cues)
		}
	}
}

// The fake reads its settings when it starts, so each case has its own server.
func TestServerEmptyAnswerIsNoSpeech(t *testing.T) {
	t.Setenv("FAKE_EMPTY", "1")
	o := setup(t, "en,en,en", "eng")
	withServer(t, &o)
	if _, err := Run(context.Background(), "video.mkv", Track{Seconds: 600}, "en", o); err != ErrNoSpeech {
		t.Fatalf("empty answer: %v", err)
	}
	empty(t, o.TempDir)
}

// A failed transcript says so, passes the server's last lines to the log,
// and leaves nothing behind.
func TestServerFailureIsPlainAndLogged(t *testing.T) {
	t.Setenv("FAKE_FAIL", "1")
	o := setup(t, "en,en,en", "eng")
	withServer(t, &o)
	var logged []string
	o.Log = func(l string) { logged = append(logged, l) }
	_, err := Run(context.Background(), "video.mkv", Track{Seconds: 600}, "en", o)
	if err == nil || !strings.Contains(err.Error(), "speech server failed") || !strings.Contains(strings.Join(logged, "\n"), "the model broke") {
		t.Fatalf("failure: %v, log %v", err, logged)
	}
	empty(t, o.TempDir)
}

// An idle server is stopped, which gives the card's memory back, and the
// next file starts a new one.
func TestServerStopsWhenIdleAndStartsAgain(t *testing.T) {
	old := ServerIdleStop
	ServerIdleStop = 150 * time.Millisecond
	t.Cleanup(func() { ServerIdleStop = old })
	o := setup(t, "en,en,en", "eng")
	state := withServer(t, &o)
	if _, err := Run(context.Background(), "video.mkv", Track{Seconds: 600}, "en", o); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for o.Server.Running() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if o.Server.Running() {
		t.Fatal("the idle server was not stopped")
	}
	if _, err := Run(context.Background(), "video.mkv", Track{Seconds: 600}, "en", o); err != nil {
		t.Fatal(err)
	}
	if count(t, state, "starts") != "2" {
		t.Fatalf("starts %s, want 2", count(t, state, "starts"))
	}
}

// The server is started for the card the options name, with the driver's
// cache folder, and started again when they change.
func TestServerStartsForItsCardAndRestartsWhenItChanges(t *testing.T) {
	o := setup(t, "en,en,en", "eng")
	state := withServer(t, &o)
	o.Device, o.CacheDir = 1, t.TempDir()
	if _, err := Run(context.Background(), "video.mkv", Track{Seconds: 600}, "en", o); err != nil {
		t.Fatal(err)
	}
	var args []string
	b, _ := os.ReadFile(filepath.Join(state, "server-args"))
	_ = json.Unmarshal(b, &args)
	if valueAfter(args, "-dev") != "1" || valueAfter(args, "-m") != o.Model || valueAfter(args, "-vm") != o.VAD || valueAfter(args, "--host") != "127.0.0.1" || count(t, state, "server-cache") != o.CacheDir {
		t.Fatalf("args %v cache %q", args, count(t, state, "server-cache"))
	}
	o.Device = -1
	if _, err := Run(context.Background(), "video.mkv", Track{Seconds: 600}, "en", o); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(state, "server-args"))
	_ = json.Unmarshal(b, &args)
	ng := false
	for _, a := range args {
		ng = ng || a == "-ng"
	}
	if !ng || count(t, state, "starts") != "2" {
		t.Fatalf("on the CPU: args %v, starts %s", args, count(t, state, "starts"))
	}
}

func TestServerEarlyExitLanguageDetect(t *testing.T) {
	// Two agreeing samples exit early without running the third sample.
	o := setup(t, "en,en,fr", "eng")
	state := withServer(t, &o)
	res, err := Run(context.Background(), "video.mkv", Track{Seconds: 600}, "", o)
	if err != nil || res.Language != "en" || count(t, state, "checks") != "2" {
		t.Fatalf("agreeing early exit: res=%+v err=%v checks=%s", res, err, count(t, state, "checks"))
	}

	// Disagreement on the first two runs the third sample.
	o2 := setup(t, "en,fr,en", "eng")
	state2 := withServer(t, &o2)
	res2, err := Run(context.Background(), "video.mkv", Track{Seconds: 600}, "", o2)
	if err != nil || res2.Language != "en" || count(t, state2, "checks") != "3" {
		t.Fatalf("disagreeing 3rd check: res=%+v err=%v checks=%s", res2, err, count(t, state2, "checks"))
	}
}

func TestServerRunChunkedParallel(t *testing.T) {
	o := setup(t, "en,en,en", "eng")
	state := withServer(t, &o)
	s1 := NewServer(nil)
	t.Cleanup(s1.Stop)
	o.Device = 0
	o.ExtraSlots = []Slot{{Device: 1, Server: s1}}
	var progUpdates []float64
	o.Progress = func(p float64) { progUpdates = append(progUpdates, p) }

	// Build a 200s WAV with a quiet valley near 94s.
	dir := t.TempDir()
	samples := make([]int16, wavSampleRate*200)
	for i := range samples {
		if i%2 == 0 {
			samples[i] = 8000
		} else {
			samples[i] = -8000
		}
	}
	for i := int(93.5 * wavSampleRate); i < int(94.5*wavSampleRate); i++ {
		samples[i] = 0
	}
	wavPath := filepath.Join(dir, "long.wav")
	writeTestWAV(t, wavPath, samples)
	info, ok := parseWAV(wavPath)
	if !ok {
		t.Fatal("parseWAV failed")
	}

	cues, err := runChunked(context.Background(), o, dir, wavPath, info, "en", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 12 {
		t.Fatalf("got %d cues, want 12 (2 chunks * 6)", len(cues))
	}
	if cues[0].StartMS != 0 || cues[6].StartMS < 90000 || cues[6].StartMS > 96000 {
		t.Fatalf("chunk offsets wrong: cue0=%+v cue6=%+v", cues[0], cues[6])
	}
	for i := 1; i < len(cues); i++ {
		if cues[i].EndMS <= cues[i].StartMS || cues[i].StartMS < cues[i-1].EndMS {
			t.Fatalf("non-monotonic cues at %d: prev=%+v cur=%+v", i, cues[i-1], cues[i])
		}
	}
	if count(t, state, "transcripts") != "2" {
		t.Fatalf("transcripts %s, want 2", count(t, state, "transcripts"))
	}
	if len(progUpdates) == 0 || progUpdates[len(progUpdates)-1] < 0.99 {
		t.Fatalf("progress updates %v", progUpdates)
	}
	for i := 1; i < len(progUpdates); i++ {
		if progUpdates[i] <= progUpdates[i-1] {
			t.Fatalf("non-monotonic progress %v", progUpdates)
		}
	}
}

func TestServerRunChunkedExtraSlotYields(t *testing.T) {
	o := setup(t, "en,en,en", "eng")
	state := withServer(t, &o)
	s1 := NewServer(nil)
	t.Cleanup(s1.Stop)
	o.Device = 0

	released := make(chan struct{}, 1)
	o.ExtraSlots = []Slot{{
		Device:      1,
		Server:      s1,
		ShouldYield: func() bool { return true },
		Release:     func() { select { case released <- struct{}{}: default: } },
	}}

	dir := t.TempDir()
	samples := make([]int16, wavSampleRate*200)
	for i := range samples {
		if i%2 == 0 {
			samples[i] = 8000
		} else {
			samples[i] = -8000
		}
	}
	for i := int(93.5 * wavSampleRate); i < int(94.5*wavSampleRate); i++ {
		samples[i] = 0
	}
	wavPath := filepath.Join(dir, "long.wav")
	writeTestWAV(t, wavPath, samples)
	info, ok := parseWAV(wavPath)
	if !ok {
		t.Fatal("parseWAV failed")
	}

	cues, err := runChunked(context.Background(), o, dir, wavPath, info, "en", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 12 {
		t.Fatalf("got %d cues, want 12", len(cues))
	}
	select {
	case <-released:
	default:
		t.Fatal("extra slot Release was not called when yielding")
	}
	if count(t, state, "transcripts") != "2" {
		t.Fatalf("transcripts %s, want 2", count(t, state, "transcripts"))
	}
}

func TestServerNoFlashAttnAndDeviceLostFallback(t *testing.T) {
	// Explicit NoFlashAttn passes -nfa.
	o := setup(t, "en,en,en", "eng")
	state := withServer(t, &o)
	o.Device = 0
	o.NoFlashAttn = true
	if _, err := Run(context.Background(), "video.mkv", Track{Seconds: 600}, "en", o); err != nil {
		t.Fatal(err)
	}
	var args []string
	b, _ := os.ReadFile(filepath.Join(state, "server-args"))
	_ = json.Unmarshal(b, &args)
	hasNFA := false
	for _, a := range args {
		hasNFA = hasNFA || a == "-nfa"
	}
	if !hasNFA {
		t.Fatalf("expected -nfa in server-args: %v", args)
	}

	// Env HOLYARRS_WHISPER_NO_FLASH_ATTN=1 passes -nfa.
	o2 := setup(t, "en,en,en", "eng")
	state2 := withServer(t, &o2)
	o2.Device = 0
	t.Setenv("HOLYARRS_WHISPER_NO_FLASH_ATTN", "1")
	if _, err := Run(context.Background(), "video.mkv", Track{Seconds: 600}, "en", o2); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(state2, "server-args"))
	_ = json.Unmarshal(b, &args)
	hasNFA = false
	for _, a := range args {
		hasNFA = hasNFA || a == "-nfa"
	}
	if !hasNFA {
		t.Fatalf("expected -nfa from env in server-args: %v", args)
	}

	// Automatic GPU fallback on ErrorDeviceLost retries once with -nfa and remembers it.
	t.Setenv("HOLYARRS_WHISPER_NO_FLASH_ATTN", "")
	t.Setenv("FAKE_DEVICE_LOST", "1")
	o3 := setup(t, "en,en,en", "eng")
	state3 := withServer(t, &o3)
	o3.Device = 0
	if _, err := Run(context.Background(), "video.mkv", Track{Seconds: 600}, "en", o3); err != nil {
		t.Fatalf("expected device-lost fallback to succeed, got: %v", err)
	}
	if count(t, state3, "starts") != "2" {
		t.Fatalf("expected 2 server starts (initial + -nfa retry), got %s", count(t, state3, "starts"))
	}
	b, _ = os.ReadFile(filepath.Join(state3, "server-args"))
	_ = json.Unmarshal(b, &args)
	hasNFA = false
	for _, a := range args {
		hasNFA = hasNFA || a == "-nfa"
	}
	if !hasNFA {
		t.Fatalf("retried server lacked -nfa: %v", args)
	}
}

func TestLinesShortAndSingleWordAfterSilenceUsesTimeMap(t *testing.T) {
	var m timeMap
	m.add("whisper_full_with_state: vad_segment_info: orig_start: 10.00, orig_end: 10.50, vad_start: 0.00, vad_end: 0.50")
	m.add("whisper_full_with_state: vad_segment_info: orig_start: 196.00, orig_end: 205.00, vad_start: 0.60, vad_end: 9.60")

	// Single-word segment after long silence.
	one := segmentOf(0, 197000, " Hello.", [3]any{" Hello.", 1000, 1500})
	cues := one.lines(m)
	if len(cues) != 1 || cues[0].StartMS != 196400 || cues[0].EndMS != 196900 || cues[0].Text != "Hello." {
		t.Fatalf("1-word after silence: %+v", cues)
	}

	// Short multi-word segment whose raw g.Offsets span > MaxLineMS due to preceding silence.
	shortSeg := segmentOf(0, 198000, " See you soon.", [3]any{" See", 1000, 1400}, [3]any{" you", 1400, 1700}, [3]any{" soon.", 1700, 2200})
	cues2 := shortSeg.lines(m)
	if len(cues2) != 1 || cues2[0].StartMS != 196400 || cues2[0].EndMS != 197600 || cues2[0].Text != "See you soon." {
		t.Fatalf("short after silence: %+v", cues2)
	}
}

func TestTimeMapResetsOnRetry(t *testing.T) {
	var m timeMap
	m.add("whisper_full_with_state: vad_segment_info: orig_start: 5.00, orig_end: 10.00, vad_start: 0.00, vad_end: 5.00")
	m.add("whisper_full_with_state: vad_segment_info: orig_start: 20.00, orig_end: 25.00, vad_start: 5.00, vad_end: 10.00")
	if len(m) != 2 {
		t.Fatalf("got %d pieces, want 2", len(m))
	}
	// Retry starts from vad_start: 0.00 again, discarding stale pieces.
	m.add("whisper_full_with_state: vad_segment_info: orig_start: 7.00, orig_end: 11.00, vad_start: 0.00, vad_end: 4.00")
	if len(m) != 1 || m[0].origStart != 7000 || m[0].vadEnd != 4000 {
		t.Fatalf("timeMap did not reset cleanly: %+v", m)
	}
}

func TestChunkCountSizing(t *testing.T) {
	cases := []struct {
		seconds float64
		slots   int
		want    int
	}{
		{seconds: 200, slots: 2, want: 2},
		{seconds: 600, slots: 2, want: 4},
		{seconds: 1440, slots: 2, want: 8},
		{seconds: 7200, slots: 2, want: 24},
		{seconds: 7200, slots: 6, want: 24},
	}
	for _, tc := range cases {
		if got := chunkCount(tc.seconds, tc.slots); got != tc.want {
			t.Fatalf("chunkCount(%.0f, %d) = %d, want %d", tc.seconds, tc.slots, got, tc.want)
		}
	}
}
