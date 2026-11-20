// Command fakerunner stands in for llama-server in tests: it takes the same
// flags, answers /health and the chat call, and acts out failures on request.
//
//	FAKE_LOG            a line printed at start
//	FAKE_EXIT_AFTER     milliseconds after which it exits with status 1
//	FAKE_NEVER_HEALTHY  when set, /health answers 503 for ever
//	FAKE_ARGS_FILE      a file its arguments are written to
//	FAKE_NO_DEVICES     when set, --list-devices lists no graphics card
//	FAKE_BODY_FILE      a file the last chat request is written to
//	FAKE_ECHO           when set, the lines come back as they were sent
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	for _, a := range os.Args[1:] {
		if a == "--list-devices" {
			fmt.Fprintln(os.Stderr, "Failed to create //.cache for shader cache (Permission denied)---disabling.")
			fmt.Println("Available devices:")
			if os.Getenv("FAKE_NO_DEVICES") == "" {
				fmt.Println("  Vulkan0: Intel(R) Arc(tm) B580 Graphics (BMG G21) (12216 MiB, 10552 MiB free)")
				fmt.Println("  Vulkan1: Intel(R) Arc(tm) B580 Graphics (BMG G21) (12216 MiB, 10375 MiB free)")
			}
			return
		}
	}
	if f := os.Getenv("FAKE_ARGS_FILE"); f != "" {
		_ = os.WriteFile(f, []byte(strings.Join(os.Args[1:], " ")+" XDG_CACHE_HOME="+os.Getenv("XDG_CACHE_HOME")), 0o644)
	}
	flags := map[string]string{}
	for i := 1; i+1 < len(os.Args); i += 2 {
		flags[os.Args[i]] = os.Args[i+1]
	}
	if l := os.Getenv("FAKE_LOG"); l != "" {
		fmt.Fprintln(os.Stderr, l)
	}
	if v := os.Getenv("FAKE_EXIT_AFTER"); v != "" {
		ms, _ := strconv.Atoi(v)
		if ms == 0 {
			os.Exit(1)
		}
		time.AfterFunc(time.Duration(ms)*time.Millisecond, func() { os.Exit(1) })
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		if os.Getenv("FAKE_NEVER_HEALTHY") != "" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+flags["--api-key"] {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		if f := os.Getenv("FAKE_BODY_FILE"); f != "" {
			_ = os.WriteFile(f, raw, 0o644)
		}
		var in struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		_ = json.Unmarshal(raw, &in)
		var user struct {
			Lines []struct {
				N    int    `json:"n"`
				Text string `json:"text"`
			} `json:"lines"`
		}
		if len(in.Messages) > 0 {
			// The lines are the JSON object the last message ends with.
			c := in.Messages[len(in.Messages)-1].Content
			if i := strings.Index(c, `{"lines"`); i >= 0 {
				_ = json.Unmarshal([]byte(c[i:]), &user)
			}
		}
		for i := range user.Lines {
			if os.Getenv("FAKE_ECHO") == "" {
				user.Lines[i].Text = "T:" + user.Lines[i].Text
			}
		}
		content, _ := json.Marshal(user)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"finish_reason": "stop", "message": map[string]string{"role": "assistant", "content": string(content)}}}})
	})
	ln, err := net.Listen("tcp", net.JoinHostPort(flags["--host"], flags["--port"]))
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		os.Exit(2)
	}
	_ = http.Serve(ln, mux)
}
