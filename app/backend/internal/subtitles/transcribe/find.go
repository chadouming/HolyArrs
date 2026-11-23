package transcribe

import (
	"io"
	"os"
	"path/filepath"
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

func parseBoolEnv(key string) (val, ok bool) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	}
	return false, false
}

// UsesOpenVINO reports whether speech transcription uses the OpenVINO backend.
func UsesOpenVINO() bool {
	if v, ok := parseBoolEnv("HOLYARRS_OV_SPEECH"); ok {
		return v
	}
	serverPath := strings.TrimSpace(os.Getenv(EnvServer))
	if serverPath == "" {
		serverPath = DefaultServer
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(serverPath), "whisper_openvino_server.py")); err == nil {
		return true
	}
	f, err := os.Open(serverPath)
	if err != nil {
		return false
	}
	defer f.Close()
	buf, _ := io.ReadAll(io.LimitReader(f, 512))
	return strings.Contains(string(buf), "whisper_openvino_server.py")
}

// OpenVINOPreferFP16 reports whether FP16 OpenVINO speech weights are preferred.
func OpenVINOPreferFP16() bool {
	v, ok := parseBoolEnv("HOLYARRS_OV_PREFER_FP16")
	return ok && v
}
