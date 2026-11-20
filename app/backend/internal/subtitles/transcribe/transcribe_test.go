package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var progs Programs

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "stt-fakes")
	if err != nil {
		panic(err)
	}
	progs = Programs{Whisper: filepath.Join(dir, "whisper-cli"), FFmpeg: filepath.Join(dir, "ffmpeg"), FFprobe: filepath.Join(dir, "ffprobe")}
	for out, pkg := range map[string]string{progs.Whisper: "./fakes/whisper", progs.FFmpeg: "./fakes/ffmpeg", progs.FFprobe: "./fakes/ffmpeg"} {
		if b, err := exec.Command("go", "build", "-o", out, pkg).CombinedOutput(); err != nil {
			panic(fmt.Sprintf("building %s: %v\n%s", pkg, err, b))
		}
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// setup gives a test its own temp folder and language-check counter.
func setup(t *testing.T, langs, tracks string) Options {
	t.Helper()
	t.Setenv("FAKE_LANGS", langs)
	t.Setenv("FAKE_TRACKS", tracks)
	t.Setenv("FAKE_STATE", t.TempDir())
	return Options{Programs: progs, Model: "model.bin", VAD: "vad.bin", Device: -1, Threads: 2, TempDir: t.TempDir()}
}

func empty(t *testing.T, dir string) {
	t.Helper()
	if es, _ := os.ReadDir(dir); len(es) != 0 {
		t.Fatalf("%d things left in the temp folder, first %s", len(es), es[0].Name())
	}
}

func TestFindNeedsAllThreePrograms(t *testing.T) {
	t.Setenv(EnvWhisper, progs.Whisper)
	t.Setenv(EnvFFmpeg, progs.FFmpeg)
	t.Setenv(EnvFFprobe, progs.FFprobe)
	if p, ok := Find(); !ok || p != progs {
		t.Fatalf("%+v %v", p, ok)
	}
	t.Setenv(EnvFFprobe, filepath.Join(t.TempDir(), "nothing"))
	if _, ok := Find(); ok {
		t.Fatal("found without ffprobe")
	}
}

func TestPickPrefersAWantedLanguageThenOriginalThenFirst(t *testing.T) {
	tr := func(langs ...string) []Track {
		out := make([]Track, len(langs))
		for i, l := range langs {
			out[i] = Track{Index: i, Language: l}
		}
		return out
	}
	if got, ok := Pick(tr("fr", "en"), []string{"en", "fr"}, "en"); !ok || got.Index != 1 {
		t.Fatalf("wanted order: %+v", got)
	}
	if got, _ := Pick(tr("en", "ja"), []string{"de"}, "ja"); got.Index != 1 {
		t.Fatalf("original: %+v", got)
	}
	if got, _ := Pick(tr("", "xx"), []string{"en"}, "ja"); got.Index != 0 {
		t.Fatalf("first: %+v", got)
	}
	if _, ok := Pick(nil, []string{"en"}, "en"); ok {
		t.Fatal("picked from no tracks")
	}
}

func TestTracksReadsTagsAndLength(t *testing.T) {
	o := setup(t, "en", "fra,-,eng")
	t.Setenv("FAKE_SECONDS", "3187.5")
	got, err := Tracks(context.Background(), o.Programs, "video.mkv")
	if err != nil || len(got) != 3 || got[0].Language != "fra" || got[1].Language != "" || got[2].Index != 2 || got[2].Seconds != 3187.5 {
		t.Fatalf("%+v %v", got, err)
	}
	t.Setenv("FAKE_TRACKS", "")
	if _, err := Tracks(context.Background(), o.Programs, "video.mkv"); !errors.Is(err, ErrNoAudio) || !Final(err) {
		t.Fatalf("no audio: %v", err)
	}
}

func TestRunTranscribesAndRemovesItsTempFile(t *testing.T) {
	o := setup(t, "en,en,en", "eng")
	res, err := Run(context.Background(), "video.mkv", Track{Index: 0, Language: "eng", Seconds: 1200}, "en", o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Language != "en" || len(res.Cues) != 6 || res.Cues[1].StartMS != 3000 || res.Cues[1].EndMS != 5500 || res.Cues[0].Text != "Line 1 of the fake transcript." {
		t.Fatalf("result %+v", res)
	}
	empty(t, o.TempDir)
}

func TestRunRemovesTempFileOnFailureAndOnCancel(t *testing.T) {
	o := setup(t, "en,en,en", "eng")
	t.Setenv("FAKE_EXIT", "1")
	_, err := Run(context.Background(), "video.mkv", Track{Seconds: 1200}, "en", o)
	if err == nil || !strings.Contains(err.Error(), "the model broke") || Final(err) {
		t.Fatalf("crash: %v final %v", err, err != nil && Final(err))
	}
	empty(t, o.TempDir)
	t.Setenv("FAKE_EXIT", "")
	t.Setenv("FAKE_SLEEP_MS", "400")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := Run(ctx, "video.mkv", Track{Seconds: 1200}, "en", o); err == nil || Final(err) {
		t.Fatalf("cancel: %v", err)
	}
	empty(t, o.TempDir)
}

func TestExtractionFailureIsPlainAndLeavesNothing(t *testing.T) {
	o := setup(t, "en,en,en", "eng")
	t.Setenv("FAKE_FFMPEG_EXIT", "1")
	_, err := Run(context.Background(), "video.mkv", Track{Seconds: 1200}, "en", o)
	if err == nil || !strings.Contains(err.Error(), "could not read the audio") || Final(err) {
		t.Fatalf("err %v", err)
	}
	empty(t, o.TempDir)
}

func TestLanguageNeedsTwoSamplesOrTheTag(t *testing.T) {
	cases := []struct {
		langs, hint, want string
		unclear           bool
	}{
		{"en,en,fr", "", "en", false},
		{"en,fr,de", "fr", "fr", false},
		{"en,fr,de", "", "", true},
		{"en,fr,de", "ja", "", true},
	}
	for _, c := range cases {
		o := setup(t, c.langs, "eng")
		res, err := Run(context.Background(), "video.mkv", Track{Seconds: 1200}, c.hint, o)
		if c.unclear {
			if !errors.Is(err, ErrLanguage) || !Final(err) {
				t.Fatalf("%s hint %q: %v", c.langs, c.hint, err)
			}
			empty(t, o.TempDir)
			continue
		}
		if err != nil || res.Language != c.want {
			t.Fatalf("%s hint %q: %q %v", c.langs, c.hint, res.Language, err)
		}
	}
}

func TestWrongTagNeverLabelsTheTranscript(t *testing.T) {
	o := setup(t, "en,en,en", "fra")
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_ARGS_FILE", args)
	res, err := Run(context.Background(), "video.mkv", Track{Language: "fra", Seconds: 1200}, "fr", o)
	if err != nil || res.Language != "en" {
		t.Fatalf("%q %v", res.Language, err)
	}
	if a := readArgs(t, args); valueAfter(a, "-l") != "en" {
		t.Fatalf("transcribed as %q", valueAfter(a, "-l"))
	}
}

func readArgs(t *testing.T, file string) []string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var a []string
	if err := json.Unmarshal(b, &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func valueAfter(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestEmptyOutputIsNoSpeech(t *testing.T) {
	o := setup(t, "en,en,en", "eng")
	t.Setenv("FAKE_EMPTY", "1")
	_, err := Run(context.Background(), "video.mkv", Track{Seconds: 1200}, "en", o)
	if !errors.Is(err, ErrNoSpeech) || !Final(err) {
		t.Fatalf("err %v", err)
	}
	empty(t, o.TempDir)
}

func TestPromptIsOneArgumentAndCut(t *testing.T) {
	o := setup(t, "en,en,en", "eng")
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_ARGS_FILE", args)
	o.Prompt = "--model /etc/passwd\n\"Ser Aldric\", Vale of Thorns\t" + strings.Repeat("x", 5000)
	if _, err := Run(context.Background(), "video.mkv", Track{Seconds: 1200}, "en", o); err != nil {
		t.Fatal(err)
	}
	a := readArgs(t, args)
	p := valueAfter(a, "--prompt")
	if !strings.HasPrefix(p, "--model /etc/passwd \"Ser Aldric\", Vale of Thorns ") || strings.ContainsAny(p, "\n\t\r") || len(p) > MaxPrompt {
		t.Fatalf("prompt (%d chars) %q", len(p), p[:min(len(p), 80)])
	}
	if valueAfter(a, "-m") != "model.bin" || valueAfter(a, "-vm") != "vad.bin" {
		t.Fatalf("the prompt moved other arguments: %v", a[:min(len(a), 14)])
	}
	// No prompt: the option is not given at all.
	o.Prompt = "  "
	if _, err := Run(context.Background(), "video.mkv", Track{Seconds: 1200}, "en", o); err != nil {
		t.Fatal(err)
	}
	for _, x := range readArgs(t, args) {
		if x == "--prompt" {
			t.Fatal("an empty prompt was passed")
		}
	}
}

func TestDeviceChoice(t *testing.T) {
	o := setup(t, "en,en,en", "eng")
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_ARGS_FILE", args)
	if _, err := Run(context.Background(), "video.mkv", Track{Seconds: 1200}, "en", o); err != nil {
		t.Fatal(err)
	}
	if a := strings.Join(readArgs(t, args), " "); !strings.Contains(a, " -ng") || strings.Contains(a, "-dev") {
		t.Fatalf("CPU args: %s", a)
	}
	o.Device = 1
	if _, err := Run(context.Background(), "video.mkv", Track{Seconds: 1200}, "en", o); err != nil {
		t.Fatal(err)
	}
	if a := readArgs(t, args); valueAfter(a, "-dev") != "1" || strings.Contains(strings.Join(a, " "), " -ng") {
		t.Fatalf("card args: %v", a)
	}
}

func TestTimeLimit(t *testing.T) {
	o := setup(t, "en,en,en", "eng")
	old := limitFor
	limitFor = func(float64) time.Duration { return 150 * time.Millisecond }
	defer func() { limitFor = old }()
	t.Setenv("FAKE_SLEEP_MS", "600")
	start := time.Now()
	_, err := Run(context.Background(), "video.mkv", Track{Seconds: 1200}, "en", o)
	if err == nil || Final(err) || time.Since(start) > 5*time.Second {
		t.Fatalf("err %v after %s", err, time.Since(start))
	}
	empty(t, o.TempDir)
}

// A long stretch Whisper gives as one segment is cut into lines by the times
// of its own words, which run on another clock than the segment's.
func TestLongSegmentIsCutByItsWordTimes(t *testing.T) {
	o := setup(t, "en,en,en", "eng")
	t.Setenv("FAKE_LONG", "1")
	res, err := Run(context.Background(), "video.mkv", Track{Seconds: 1200}, "en", o)
	if err != nil || len(res.Cues) < 4 {
		t.Fatalf("%d cues %v", len(res.Cues), err)
	}
	var words []string
	for i, c := range res.Cues {
		if c.EndMS-c.StartMS > MaxLineMS || c.StartMS < 10000 || c.EndMS > 34000 || (i > 0 && c.StartMS < res.Cues[i-1].EndMS) {
			t.Fatalf("cue %d runs %d to %d", i, c.StartMS, c.EndMS)
		}
		words = append(words, strings.Fields(c.Text)...)
	}
	if len(words) != 12 || words[0] != "word1" || words[11] != "word12" {
		t.Fatalf("words %v", words)
	}
	// The first line ends with the sentence, on the sixth word: twelve seconds in.
	if res.Cues[0].StartMS != 10000 || !strings.HasSuffix(res.Cues[len(res.Cues)-1].Text, "word12") {
		t.Fatalf("first cue %+v", res.Cues[0])
	}
	sixth := -1
	for i, c := range res.Cues {
		if strings.HasSuffix(c.Text, "word6.") {
			sixth = i
			if c.EndMS != 22000 {
				t.Fatalf("the sentence ends at %d, want 22000", c.EndMS)
			}
		}
	}
	if sixth < 0 {
		t.Fatalf("no line ends with the sentence: %+v", res.Cues)
	}
}

// A segment that runs across a stretch with no speech keeps its lines where
// the speech is, at the pace it was spoken, not spread over the silence.
func TestSegmentAcrossSilenceIsNotSpreadOverIt(t *testing.T) {
	o := setup(t, "en,en,en", "eng")
	t.Setenv("FAKE_LONG", "straddle")
	res, err := Run(context.Background(), "video.mkv", Track{Seconds: 1200}, "en", o)
	if err != nil || len(res.Cues) < 4 {
		t.Fatalf("%d cues %v", len(res.Cues), err)
	}
	for i, c := range res.Cues {
		if c.EndMS-c.StartMS > MaxLineMS || c.StartMS < 10000 || c.EndMS > 34000 {
			t.Fatalf("cue %d runs %d to %d: spread over the silence", i, c.StartMS, c.EndMS)
		}
	}
}

// A warning ffprobe prints beside its answer does not spoil the answer.
func TestTracksIgnoresWarningsOnStderr(t *testing.T) {
	o := setup(t, "en", "eng,fra")
	t.Setenv("FAKE_PROBE_WARN", "1")
	got, err := Tracks(context.Background(), o.Programs, "video.mkv")
	if err != nil || len(got) != 2 || got[1].Language != "fra" {
		t.Fatalf("%+v %v", got, err)
	}
}

func segmentOf(from, to int64, text string, toks ...[3]any) segment {
	var g segment
	g.Offsets.From, g.Offsets.To, g.Text = from, to, text
	for _, tk := range toks {
		var x struct {
			Text    string `json:"text"`
			Offsets struct {
				From int64 `json:"from"`
				To   int64 `json:"to"`
			} `json:"offsets"`
		}
		x.Text = tk[0].(string)
		x.Offsets.From, x.Offsets.To = int64(tk[1].(int)), int64(tk[2].(int))
		g.Tokens = append(g.Tokens, x)
	}
	return g
}

// Words Whisper gives no length still make their lines: nothing said is dropped.
func TestLinesNeverDropWords(t *testing.T) {
	g := segmentOf(1000, 12000, " Nobody answered the door. Somebody surely heard it. Then everything went quiet.",
		[3]any{" Nobody", 0, 0}, [3]any{" answered", 0, 0}, [3]any{" the", 0, 0}, [3]any{" door.", 0, 0},
		[3]any{" Somebody", 0, 0}, [3]any{" surely", 0, 0}, [3]any{" heard", 0, 0}, [3]any{" it.", 0, 0},
		[3]any{" Then", 0, 0}, [3]any{" everything", 0, 0}, [3]any{" went", 0, 0}, [3]any{" quiet.", 0, 0})
	var words []string
	lines := g.lines(nil)
	for i, c := range lines {
		if c.EndMS <= c.StartMS || (i > 0 && c.StartMS < lines[i-1].EndMS) {
			t.Fatalf("line %d runs %d to %d", i, c.StartMS, c.EndMS)
		}
		words = append(words, strings.Fields(c.Text)...)
	}
	if strings.Join(words, " ") != "Nobody answered the door. Somebody surely heard it. Then everything went quiet." {
		t.Fatalf("words %q", words)
	}
}

// Word pieces that split a character cannot be put back together: the
// segment's own text is kept whole instead.
func TestLinesKeepSegmentTextWhenPiecesAreBroken(t *testing.T) {
	text := " Il a été très étonné de le voir arriver si tôt ce matin-là, et moi aussi."
	g := segmentOf(0, 12000, text, [3]any{" Il", 0, 500}, [3]any{" a", 500, 800}, [3]any{" �", 800, 1200}, [3]any{"t�", 1200, 1500}, [3]any{" très", 1500, 2000})
	lines := g.lines(nil)
	if len(lines) != 1 || lines[0].Text != strings.TrimSpace(text) || lines[0].StartMS != 0 || lines[0].EndMS != 12000 {
		t.Fatalf("lines %+v", lines)
	}
}
