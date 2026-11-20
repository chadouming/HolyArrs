package transcribe

import (
	"os"
	"strings"
)

// Where the programs are looked for, and the settings that say otherwise.
const (
	EnvWhisper     = "HOLYARRS_WHISPER_CLI"
	EnvFFmpeg      = "HOLYARRS_FFMPEG"
	EnvFFprobe     = "HOLYARRS_FFPROBE"
	DefaultWhisper = "/opt/whisper/whisper-cli"
	DefaultFFmpeg  = "/usr/bin/ffmpeg"
	DefaultFFprobe = "/usr/bin/ffprobe"
)

// Programs are the three programs a transcription runs.
type Programs struct{ Whisper, FFmpeg, FFprobe string }

func runnable(env, def string) (string, bool) {
	p := strings.TrimSpace(os.Getenv(env))
	if p == "" {
		p = def
	}
	fi, err := os.Stat(p)
	return p, err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0
}

// Find returns the three programs and whether all are there and can be run.
func Find() (Programs, bool) {
	w, ok1 := runnable(EnvWhisper, DefaultWhisper)
	f, ok2 := runnable(EnvFFmpeg, DefaultFFmpeg)
	p, ok3 := runnable(EnvFFprobe, DefaultFFprobe)
	return Programs{Whisper: w, FFmpeg: f, FFprobe: p}, ok1 && ok2 && ok3
}
