// Command ffmpeg stands in for ffmpeg and, when run under the name ffprobe,
// for ffprobe, in tests.
//
//	FAKE_TRACKS       comma list of audio language tags ("fra,eng"); "-" for a track with no tag; empty for no audio
//	FAKE_SECONDS      the file's length (default 1200)
//	FAKE_FFMPEG_EXIT  when set, ffmpeg fails with this status
//	FAKE_PROBE_WARN   when set, ffprobe also prints a warning on stderr
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	if strings.Contains(filepath.Base(os.Args[0]), "ffprobe") {
		type stream struct {
			Index int               `json:"index"`
			Tags  map[string]string `json:"tags,omitempty"`
		}
		out := struct {
			Streams []stream          `json:"streams"`
			Format  map[string]string `json:"format"`
		}{Streams: []stream{}, Format: map[string]string{"duration": "1200.0"}}
		if s := os.Getenv("FAKE_SECONDS"); s != "" {
			out.Format["duration"] = s
		}
		if tr := os.Getenv("FAKE_TRACKS"); tr != "" {
			for i, l := range strings.Split(tr, ",") {
				st := stream{Index: i + 1}
				if l != "-" {
					st.Tags = map[string]string{"language": l}
				}
				out.Streams = append(out.Streams, st)
			}
		}
		if os.Getenv("FAKE_PROBE_WARN") != "" {
			fmt.Fprintln(os.Stderr, "[matroska,webm @ 0x1] Element at 0x10 ending past its parent")
		}
		_ = json.NewEncoder(os.Stdout).Encode(out)
		return
	}
	if v := os.Getenv("FAKE_FFMPEG_EXIT"); v != "" {
		n, _ := strconv.Atoi(v)
		fmt.Fprintln(os.Stderr, "ffmpeg: could not read the audio")
		os.Exit(n)
	}
	// The last argument is the file to write.
	if err := os.WriteFile(os.Args[len(os.Args)-1], []byte("RIFF fake sound"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
