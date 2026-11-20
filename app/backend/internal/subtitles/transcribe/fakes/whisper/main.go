// Command whisper stands in for whisper-cli in tests.
//
//	FAKE_LANGS      comma list of the languages the language checks answer, in order ("en,en,fr")
//	FAKE_STATE      a folder where the number of language checks so far is kept
//	FAKE_EMPTY      when set, the transcript has no lines
//	FAKE_EXIT       when set, transcription fails with this status
//	FAKE_SLEEP_MS   wait before answering
//	FAKE_ARGS_FILE  a file the transcription's arguments are written to, as a JSON list
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func arg(name string) string {
	for i, a := range os.Args {
		if a == name && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	return ""
}

func has(name string) bool {
	for _, a := range os.Args {
		if a == name {
			return true
		}
	}
	return false
}

func main() {
	if ms, _ := strconv.Atoi(os.Getenv("FAKE_SLEEP_MS")); ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	if has("-dl") {
		langs := strings.Split(os.Getenv("FAKE_LANGS"), ",")
		n := 0
		if dir := os.Getenv("FAKE_STATE"); dir != "" {
			p := filepath.Join(dir, "checks")
			b, _ := os.ReadFile(p)
			n, _ = strconv.Atoi(strings.TrimSpace(string(b)))
			_ = os.WriteFile(p, []byte(strconv.Itoa(n+1)), 0o600)
		}
		l := langs[n%len(langs)]
		if l == "" {
			l = "en"
		}
		fmt.Fprintf(os.Stderr, "whisper_full_with_state: auto-detected language: %s (p = 0.900000)\n", l)
		return
	}
	if f := os.Getenv("FAKE_ENV_FILE"); f != "" {
		_ = os.WriteFile(f, []byte(os.Getenv("XDG_CACHE_HOME")), 0o600)
	}
	if f := os.Getenv("FAKE_ARGS_FILE"); f != "" {
		b, _ := json.Marshal(os.Args[1:])
		_ = os.WriteFile(f, b, 0o600)
	}
	if v := os.Getenv("FAKE_EXIT"); v != "" {
		n, _ := strconv.Atoi(v)
		fmt.Fprintln(os.Stderr, "whisper: the model broke")
		os.Exit(n)
	}
	if has("-pp") {
		// As whisper-cli prints it, on stderr.
		fmt.Fprintln(os.Stderr, "operator(): progress =  50%")
		fmt.Fprintln(os.Stderr, "operator(): progress = 100%")
	}
	type token struct {
		Text    string           `json:"text"`
		Offsets map[string]int64 `json:"offsets"`
	}
	type cue struct {
		Offsets map[string]int64 `json:"offsets"`
		Text    string           `json:"text"`
		Tokens  []token          `json:"tokens,omitempty"`
	}
	out := struct {
		Result        map[string]string `json:"result"`
		Transcription []cue             `json:"transcription"`
	}{Result: map[string]string{"language": arg("-l")}, Transcription: []cue{}}
	// FAKE_LONG: one segment of twelve words, two seconds each, whose word
	// times run on another clock than the segment's own, as with voice
	// detection on. FAKE_LONG=straddle makes the segment end 400 seconds
	// after it starts, as when it runs across a stretch with no speech.
	if long := os.Getenv("FAKE_LONG"); long != "" {
		seg := cue{Offsets: map[string]int64{"from": 10000, "to": 34000}, Tokens: []token{{Text: "[_BEG_]", Offsets: map[string]int64{"from": 500000, "to": 500000}}}}
		if long == "straddle" {
			seg.Offsets["to"] = 410000
		}
		for i := int64(0); i < 12; i++ {
			w := fmt.Sprintf(" word%d", i+1)
			if i == 5 {
				w += "."
			}
			seg.Text += w
			seg.Tokens = append(seg.Tokens, token{Text: w, Offsets: map[string]int64{"from": 500000 + i*2000, "to": 500000 + i*2000 + 2000}})
		}
		out.Transcription = append(out.Transcription, seg)
	} else if os.Getenv("FAKE_EMPTY") == "" {
		for i := int64(0); i < 6; i++ {
			out.Transcription = append(out.Transcription, cue{Offsets: map[string]int64{"from": i * 3000, "to": i*3000 + 2500}, Text: fmt.Sprintf(" Line %d of the fake transcript.", i+1)})
		}
	}
	b, _ := json.Marshal(out)
	if err := os.WriteFile(arg("-of")+".json", b, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
