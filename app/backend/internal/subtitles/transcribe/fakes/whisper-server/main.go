// Command whisper-server stands in for whisper.cpp's server in tests. It
// answers /health and /inference the way the real one does, as far as
// HolyArrs reads it.
//
//	FAKE_LANGS       comma list of the languages the language checks answer, in order
//	FAKE_STATE       a folder where the number of starts and of calls is kept
//	FAKE_EMPTY       when set, the transcript has no segments
//	FAKE_FAIL        when set, /inference answers 500
//	FAKE_SLEEP_MS    wait before answering a transcript
//	FAKE_VAD            "orig_start,orig_end,vad_start,vad_end;..." written to the log before the answer
//	FAKE_FIELDS_FILE    a file the last transcript request's fields are written to, as JSON
//	FAKE_DL_FIELDS_FILE a file the last detect_language request's fields are written to, as JSON
//	FAKE_DEVICE_LOST    when set and -nfa is absent, /inference prints ErrorDeviceLost and exits 1
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

func hasArg(name string) bool {
	for _, a := range os.Args {
		if a == name {
			return true
		}
	}
	return false
}

func arg(name string) string {
	for i, a := range os.Args {
		if a == name && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	return ""
}

func bump(name string) int {
	dir := os.Getenv("FAKE_STATE")
	if dir == "" {
		return 0
	}
	p := filepath.Join(dir, name)
	b, _ := os.ReadFile(p)
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	_ = os.WriteFile(p, []byte(strconv.Itoa(n+1)), 0o600)
	return n
}

func main() {
	bump("starts")
	if dir := os.Getenv("FAKE_STATE"); dir != "" {
		b, _ := json.Marshal(os.Args[1:])
		_ = os.WriteFile(filepath.Join(dir, "server-args"), b, 0o600)
		_ = os.WriteFile(filepath.Join(dir, "server-cache"), []byte(os.Getenv("XDG_CACHE_HOME")), 0o600)
	}
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"status":"ok"}`) })
	mux.HandleFunc("/inference", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if _, _, err := r.FormFile("file"); err != nil {
			http.Error(w, `{"error":"no 'file' field in the request"}`, 400)
			return
		}
		f := map[string]string{}
		for k, v := range r.MultipartForm.Value {
			f[k] = v[0]
		}
		if f["detect_language"] == "true" {
			if p := os.Getenv("FAKE_DL_FIELDS_FILE"); p != "" {
				b, _ := json.Marshal(f)
				_ = os.WriteFile(p, b, 0o600)
			}
			langs := strings.Split(os.Getenv("FAKE_LANGS"), ",")
			l := langs[bump("checks")%len(langs)]
			if l == "" {
				l = "en"
			}
			fmt.Fprintf(os.Stderr, "whisper_full_with_state: auto-detected language: %s (p = 0.900000)\n", l)
			fmt.Fprint(w, `{"text":"","language":"x"}`)
			return
		}
		if os.Getenv("FAKE_DEVICE_LOST") != "" && !hasArg("-nfa") {
			fmt.Fprintln(os.Stderr, "ggml_vulkan: ErrorDeviceLost")
			os.Exit(1)
		}
		bump("transcripts")
		if p := os.Getenv("FAKE_FIELDS_FILE"); p != "" {
			b, _ := json.Marshal(f)
			_ = os.WriteFile(p, b, 0o600)
		}
		if ms, _ := strconv.Atoi(os.Getenv("FAKE_SLEEP_MS")); ms > 0 {
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
		if os.Getenv("FAKE_FAIL") != "" {
			fmt.Fprintln(os.Stderr, "whisper: the model broke")
			http.Error(w, `{"error":"failed to process audio"}`, 500)
			return
		}
		if v := os.Getenv("FAKE_VAD"); v != "" {
			for _, piece := range strings.Split(v, ";") {
				p := strings.Split(piece, ",")
				fmt.Fprintf(os.Stderr, "whisper_full_with_state: vad_segment_info: orig_start: %s, orig_end: %s, vad_start: %s, vad_end: %s\n", p[0], p[1], p[2], p[3])
			}
		}
		fmt.Fprintln(os.Stderr, "whisper_print_progress_callback: progress =  50%")
		fmt.Fprintln(os.Stderr, "whisper_print_progress_callback: progress = 100%")
		type word struct {
			Word  string  `json:"word"`
			Start float64 `json:"start"`
			End   float64 `json:"end"`
		}
		type seg struct {
			ID    int     `json:"id"`
			Text  string  `json:"text"`
			Start float64 `json:"start"`
			End   float64 `json:"end"`
			Words []word  `json:"words,omitempty"`
		}
		out := struct {
			Language string `json:"language"`
			Segments []seg  `json:"segments"`
		}{Language: "english", Segments: []seg{}}
		if long := os.Getenv("FAKE_LONG"); long != "" {
			// One segment of twelve words, two seconds each, on the words' own clock.
			base := 500.0
			if v, err := strconv.ParseFloat(os.Getenv("FAKE_TOKEN_BASE"), 64); err == nil {
				base = v / 1000
			}
			s := seg{Start: 10, End: 34}
			if long == "straddle" {
				s.End = 410
			}
			for i := 0; i < 12; i++ {
				w := fmt.Sprintf(" word%d", i+1)
				if i == 5 {
					w += "."
				}
				s.Text += w
				s.Words = append(s.Words, word{w, base + float64(i)*2, base + float64(i)*2 + 2})
			}
			out.Segments = append(out.Segments, s)
		} else if os.Getenv("FAKE_EMPTY") == "" {
			for i := 0; i < 6; i++ {
				out.Segments = append(out.Segments, seg{ID: i, Text: fmt.Sprintf(" Line %d of the fake transcript.", i+1), Start: float64(i) * 3, End: float64(i)*3 + 2.5})
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	addr := arg("--host") + ":" + arg("--port")
	fmt.Printf("\nwhisper server listening at http://%s\n\n", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintln(os.Stderr, "error: server listen failed:", err)
		os.Exit(1)
	}
}
