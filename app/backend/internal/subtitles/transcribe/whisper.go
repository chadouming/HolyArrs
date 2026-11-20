package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	// SampleSeconds is the length of each sample the language is checked on.
	SampleSeconds = 30
	// MaxPrompt is the room for the names and terms: about 200 tokens of the
	// 224 whisper.cpp allows an initial prompt.
	MaxPrompt = 800
	tailLines = 12
	// MaxLineMS and MaxLineChars are the most one line may last and hold
	// when a long segment is cut by its words: two rows of 42 characters.
	MaxLineMS    = 7000
	MaxLineChars = 84
	// lineGapMS is a pause between words long enough to end a line.
	lineGapMS = 700
)

// samplePoints are where in the file the language samples are taken. The
// first half minute is often music or silence, so none is taken there.
var samplePoints = []float64{0.2, 0.5, 0.8}

// limitFor is how long transcribing a file of the given length may take.
var limitFor = func(seconds float64) time.Duration {
	return 10*time.Minute + 2*time.Duration(seconds*float64(time.Second))
}

// Options say how a file is transcribed.
type Options struct {
	Programs Programs
	Model    string // path of the Whisper model
	VAD      string // path of the voice-detection model
	Device   int    // graphics card number; -1 for the CPU
	Threads  int
	Prompt   string // names and terms; may be empty
	TempDir  string // the system's when empty
	Log      func(line string)
	// CacheDir is where the graphics driver keeps what it compiles for a
	// card, so each file does not wait for it again. Not used on the CPU.
	CacheDir string
	// Progress, when set, is told how far the run is, from 0 to 1.
	Progress func(p float64)
}

// Shares of a run: reading the sound, checking the language, and the rest
// for Whisper itself.
const (
	shareAudio    = 0.05
	shareLanguage = 0.10
)

var progressLine = regexp.MustCompile(`progress\s*=\s*(\d+)\s*%`)

func (o Options) tell(p float64) {
	if o.Progress != nil {
		o.Progress(min(max(p, 0), 1))
	}
}

// lineWriter keeps everything written to it and hands each whole line to fn.
type lineWriter struct {
	buf  *bytes.Buffer
	part []byte
	fn   func(line string)
}

func (w *lineWriter) Write(b []byte) (int, error) {
	w.buf.Write(b)
	w.part = append(w.part, b...)
	for {
		i := bytes.IndexAny(w.part, "\r\n")
		if i < 0 {
			break
		}
		if i > 0 {
			w.fn(string(w.part[:i]))
		}
		w.part = w.part[i+1:]
	}
	return len(b), nil
}

// Cue is one timed line as Whisper gave it.
type Cue struct {
	StartMS, EndMS int64
	Text           string
}

// Result is a transcript.
type Result struct {
	Language string // what was spoken, as Whisper's two-letter code
	Cues     []Cue  // not yet shaped for a screen
	Seconds  float64
}

// run runs one program to its end and returns what it printed and what it
// said beside that. A failure carries the program's last lines. The child is started on a locked thread:
// the kernel ends it when that thread ends, and Go may end any other.
func run(ctx context.Context, log func(string), name string, args ...string) (stdout, stderr string, err error) {
	return runWatch(ctx, log, nil, nil, name, args...)
}

// runWatch is run with a listener for each line the program says beside its
// output, as it says it, and with env added to the app's own environment.
func runWatch(ctx context.Context, log func(string), onLine func(string), env []string, name string, args ...string) (stdout, stderr string, err error) {
	type done struct {
		out, errOut string
		err         error
	}
	ch := make(chan done, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.SysProcAttr = childAttr()
		cmd.WaitDelay = 5 * time.Second
		if len(env) > 0 {
			cmd.Env = append(os.Environ(), env...)
		}
		var outBuf, errBuf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
		if onLine != nil {
			cmd.Stderr = &lineWriter{buf: &errBuf, fn: onLine}
		}
		err := cmd.Run()
		if log != nil {
			for _, l := range lastLines(errBuf.String(), tailLines) {
				if progressLine.MatchString(l) {
					continue // told to the listener, not worth a log line each
				}
				log(filepath.Base(name) + ": " + l)
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				err = ctx.Err()
			} else if tail := strings.Join(lastLines(errBuf.String(), 3), "; "); tail != "" {
				err = fmt.Errorf("%s failed: %s", filepath.Base(name), tail)
			} else {
				err = fmt.Errorf("%s failed: %w", filepath.Base(name), err)
			}
		}
		ch <- done{outBuf.String(), errBuf.String(), err}
	}()
	d := <-ch
	return d.out, d.errOut, d.err
}

func lastLines(s string, n int) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// cleanPrompt makes the names and terms one plain line that fits the room.
func cleanPrompt(p string) string {
	p = strings.Join(strings.FieldsFunc(p, func(r rune) bool { return r == '\n' || r == '\r' || r == '\t' || r < 0x20 || r == 0x7f }), " ")
	p = strings.Join(strings.Fields(p), " ")
	if len(p) > MaxPrompt {
		p = p[:MaxPrompt]
		for len(p) > 0 && !strings.HasSuffix(p, " ") && p[len(p)-1] >= 0x80 {
			p = p[:len(p)-1] // not in the middle of a character
		}
		if i := strings.LastIndexByte(p, ' '); i > MaxPrompt/2 {
			p = p[:i] // nor in the middle of a term
		}
	}
	return strings.TrimSpace(p)
}

// env is what the speech program is started with beside the app's own
// environment: nil for nothing more.
func (o Options) env() []string {
	if o.Device < 0 || o.CacheDir == "" {
		return nil
	}
	return []string{"XDG_CACHE_HOME=" + o.CacheDir}
}

func (o Options) device() []string {
	if o.Device < 0 {
		return []string{"-ng"}
	}
	return []string{"-dev", strconv.Itoa(o.Device)}
}

var detected = regexp.MustCompile(`auto-detected language:\s*([a-z]{2,3})\b`)

// spoken tells the language of the sound file from three samples: the one at
// least two agree on, or one that agrees with hint, the track's own tag.
func spoken(ctx context.Context, o Options, dir, wav string, seconds float64, hint string) (string, error) {
	count := map[string]int{}
	var order []string
	for i, at := range samplePoints {
		sample := filepath.Join(dir, fmt.Sprintf("sample%d.wav", i))
		start := max(seconds*at-SampleSeconds/2, 0)
		if _, _, err := run(ctx, o.Log, o.Programs.FFmpeg, "-nostdin", "-loglevel", "error", "-y",
			"-ss", strconv.FormatFloat(start, 'f', 1, 64), "-t", strconv.Itoa(SampleSeconds), "-i", wav, sample); err != nil {
			return "", err
		}
		args := append([]string{"-m", o.Model, "-f", sample, "-dl", "-t", strconv.Itoa(max(o.Threads, 1))}, o.device()...)
		out, errOut, err := runWatch(ctx, o.Log, nil, o.env(), o.Programs.Whisper, args...)
		if err != nil {
			return "", err
		}
		if m := detected.FindStringSubmatch(out + "\n" + errOut); m != nil {
			if count[m[1]] == 0 {
				order = append(order, m[1])
			}
			count[m[1]]++
		}
	}
	for _, l := range order {
		if count[l] >= 2 {
			return l, nil
		}
	}
	if hint != "" && count[hint] > 0 {
		return hint, nil
	}
	return "", fmt.Errorf("%w: the samples said %s", ErrLanguage, strings.Join(order, ", "))
}

// Run reads audio track t of video into a temporary sound file, tells the
// spoken language on three samples (hint is the track's tag as one of
// Whisper's codes, or empty), and transcribes it. Everything temporary is
// removed whatever happens.
func Run(ctx context.Context, video string, t Track, hint string, o Options) (Result, error) {
	res := Result{Seconds: t.Seconds}
	ctx, cancel := context.WithTimeout(ctx, limitFor(t.Seconds))
	defer cancel()
	dir, err := os.MkdirTemp(o.TempDir, "holyarrs-stt-")
	if err != nil {
		return res, fmt.Errorf("no room for the temporary sound file: %w", err)
	}
	defer os.RemoveAll(dir)
	wav := filepath.Join(dir, "audio.wav")
	if _, _, err := run(ctx, o.Log, o.Programs.FFmpeg, "-nostdin", "-loglevel", "error", "-y", "-i", video,
		"-map", "0:a:"+strconv.Itoa(t.Index), "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", wav); err != nil {
		return res, err
	}
	o.tell(shareAudio)
	lang, err := spoken(ctx, o, dir, wav, t.Seconds, hint)
	if err != nil {
		return res, err
	}
	res.Language = lang
	o.tell(shareLanguage)
	base := filepath.Join(dir, "out")
	// The full JSON holds a time for every word, which is what lets a long
	// segment be cut into lines that are timed, not guessed.
	args := []string{"-m", o.Model, "-f", wav, "-l", lang, "-ojf", "-of", base, "-np", "-t", strconv.Itoa(max(o.Threads, 1)), "--vad", "-vm", o.VAD}
	args = append(args, o.device()...)
	if p := cleanPrompt(o.Prompt); p != "" {
		// Given once, at the start. Carried through the whole file it made
		// Whisper stop marking times: one segment for every half minute.
		args = append(args, "--prompt", p)
	}
	var onLine func(string)
	if o.Progress != nil {
		// whisper-cli prints its percent beside its output when asked to.
		args = append(args, "-pp")
		onLine = func(line string) {
			if m := progressLine.FindStringSubmatch(line); m != nil {
				n, _ := strconv.Atoi(m[1])
				o.tell(shareLanguage + (1-shareLanguage)*float64(min(n, 100))/100)
			}
		}
	}
	if _, _, err := runWatch(ctx, o.Log, onLine, o.env(), o.Programs.Whisper, args...); err != nil {
		return res, err
	}
	raw, err := os.ReadFile(base + ".json")
	if err != nil {
		return res, errors.New("whisper-cli wrote no transcript")
	}
	var doc struct {
		Transcription []segment `json:"transcription"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return res, fmt.Errorf("the transcript could not be read: %w", err)
	}
	for _, seg := range doc.Transcription {
		res.Cues = append(res.Cues, seg.lines()...)
	}
	if len(res.Cues) == 0 {
		return res, ErrNoSpeech
	}
	return res, nil
}

// segment is one stretch of the transcript as whisper-cli writes it. Its own
// times are those of the video. Its words' times run on another clock when
// voice detection cut the silence out, so only their distances are used.
type segment struct {
	Offsets struct {
		From int64 `json:"from"`
		To   int64 `json:"to"`
	} `json:"offsets"`
	Text   string `json:"text"`
	Tokens []struct {
		Text    string `json:"text"`
		Offsets struct {
			From int64 `json:"from"`
			To   int64 `json:"to"`
		} `json:"offsets"`
	} `json:"tokens"`
}

type word struct {
	text     string
	from, to int64 // from the segment's first word, in ms
}

// words joins the segment's word pieces into words, timed from its first.
func (g segment) words() []word {
	var out []word
	base := int64(-1)
	for _, t := range g.Tokens {
		if t.Text == "" || strings.HasPrefix(t.Text, "[_") || t.Offsets.To < t.Offsets.From {
			continue // a marker, not speech
		}
		if base < 0 {
			base = t.Offsets.From
		}
		from, to := t.Offsets.From-base, t.Offsets.To-base
		if len(out) > 0 && !strings.HasPrefix(t.Text, " ") {
			out[len(out)-1].text += t.Text
			out[len(out)-1].to = max(out[len(out)-1].to, to)
			continue
		}
		if text := strings.TrimSpace(t.Text); text != "" {
			out = append(out, word{text, from, to})
		}
	}
	return out
}

// lines cuts the segment into lines that fit a screen, timed by its words. A
// segment short enough is one line. A segment that runs across a stretch with
// no speech has a span far longer than its words: its lines keep the pace of
// the words from where the segment starts, and are not spread over the silence.
func (g segment) lines() []Cue {
	text := strings.TrimSpace(g.Text)
	from, to := g.Offsets.From, g.Offsets.To
	if text == "" || to <= from {
		return nil
	}
	ws := g.words()
	short := to-from <= MaxLineMS && len([]rune(text)) <= MaxLineChars
	broken := false
	for _, w := range ws {
		// A word piece that held half a character cannot be read: the
		// segment's own text is whole, so it is kept and cut by length later.
		broken = broken || strings.ContainsRune(w.text, '\uFFFD')
	}
	if short || len(ws) < 2 || broken {
		return []Cue{{StartMS: from, EndMS: to, Text: text}}
	}
	var out []Cue
	var cur []word
	flush := func() {
		if len(cur) == 0 {
			return
		}
		parts := make([]string, len(cur))
		for i, w := range cur {
			parts[i] = w.text
		}
		start, end := from+cur[0].from, from+cur[len(cur)-1].to
		if end <= start {
			end = start + 400
		}
		if len(out) > 0 && start < out[len(out)-1].EndMS {
			start = out[len(out)-1].EndMS
		}
		// Words given no length still make a line: nothing said is dropped.
		end = max(min(end, max(to, start+400)), start+400)
		out = append(out, Cue{StartMS: start, EndMS: end, Text: strings.Join(parts, " ")})
		cur = nil
	}
	size := 0
	for _, w := range ws {
		if len(cur) > 0 && (w.from-cur[len(cur)-1].to > lineGapMS || size+1+len([]rune(w.text)) > MaxLineChars || w.to-cur[0].from > MaxLineMS) {
			flush()
			size = 0
		}
		cur = append(cur, w)
		size += 1 + len([]rune(w.text))
		if last := strings.TrimRight(w.text, `"'”’)]`); size > 12 && strings.ContainsAny(last[max(len(last)-1, 0):], ".?!…") {
			flush()
			size = 0
		}
	}
	flush()
	return out
}
