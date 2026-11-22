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
	EnvServer      = "HOLYARRS_WHISPER_SERVER"
	DefaultServer  = "/opt/whisper/whisper-server"
)

// Programs are the three programs a transcription runs.
type Programs struct {
	Whisper, FFmpeg, FFprobe string
	// Server is whisper.cpp's server, which keeps the model loaded between
	// files. It is empty when the image does not hold it: the one-shot
	// program is used then.
	Server string
}

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
	progs := Programs{Whisper: w, FFmpeg: f, FFprobe: p}
	if s, ok := runnable(EnvServer, DefaultServer); ok {
		progs.Server = s
	}
	return progs, ok1 && ok2 && ok3
}
