package transcode

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var ffmpegStallTimeout = 90 * time.Second

type subprocResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

func buildChildEnv(gpu *GPUDevice) []string {
	if gpu == nil || gpu.Vendor != "intel" {
		return nil
	}
	if strings.TrimSpace(os.Getenv("LIBVA_DRIVER_NAME")) != "" {
		return nil
	}
	env := os.Environ()
	env = append(env, "LIBVA_DRIVER_NAME=iHD")
	return env
}

func runSubproc(ctx context.Context, gpu *GPUDevice, bin string, args ...string) (subprocResult, error) {
	type done struct {
		res subprocResult
		err error
	}
	ch := make(chan done, 1)
	go func() {
		// Pin the OS thread for the child lifetime because Linux Pdeathsig fires when the spawning thread exits.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.SysProcAttr = childSysProcAttr()
		if env := buildChildEnv(gpu); env != nil {
			cmd.Env = env
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		var err error
		if startErr := cmd.Start(); startErr != nil {
			err = startErr
		} else {
			if cmd.Process != nil {
				lowerChildPriority(cmd.Process.Pid)
			}
			err = cmd.Wait()
		}
		exitCode := 0
		if err != nil {
			if cmd.ProcessState != nil {
				exitCode = cmd.ProcessState.ExitCode()
			} else {
				exitCode = -1
			}
		}
		ch <- done{
			res: subprocResult{
				Stdout:   stdout.String(),
				Stderr:   stderr.String(),
				ExitCode: exitCode,
			},
			err: err,
		}
	}()
	d := <-ch
	return d.res, d.err
}

type progressUpdate struct {
	OutSeconds float64
	FPS        float64
	Speed      float64
	Frame      int64
	TotalSize  int64
}

type tailBuffer struct {
	maxBytes int
	buf      []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.maxBytes {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.maxBytes:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	return strings.TrimSpace(string(t.buf))
}

func runStreamingFFmpeg(
	ctx context.Context,
	gpu *GPUDevice,
	bin string,
	args []string,
	onProgress func(progressUpdate),
) (string, error) {
	type done struct {
		stderr string
		err    error
	}
	ch := make(chan done, 1)
	go func() {
		// Pin the OS thread for the child lifetime because Linux Pdeathsig fires when the spawning thread exits.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		runCtx, runCancel := context.WithCancel(ctx)
		defer runCancel()

		cmd := exec.CommandContext(runCtx, bin, args...)
		cmd.SysProcAttr = childSysProcAttr()
		if env := buildChildEnv(gpu); env != nil {
			cmd.Env = env
		}

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			ch <- done{err: fmt.Errorf("ffmpeg stdout pipe: %w", err)}
			return
		}
		stderrTail := &tailBuffer{maxBytes: 8192}
		cmd.Stderr = stderrTail

		if err := cmd.Start(); err != nil {
			ch <- done{err: fmt.Errorf("start ffmpeg: %w", err)}
			return
		}
		if cmd.Process != nil {
			lowerChildPriority(cmd.Process.Pid)
		}

		var lastAdvance atomic.Int64
		lastAdvance.Store(time.Now().UnixNano())
		var lastOutSec float64
		var lastFrame int64
		var lastTotalSize int64
		var stalled atomic.Bool

		wrappedProgress := func(u progressUpdate) {
			advanced := false
			if u.OutSeconds > lastOutSec {
				lastOutSec = u.OutSeconds
				advanced = true
			}
			if u.Frame > lastFrame {
				lastFrame = u.Frame
				advanced = true
			}
			if u.TotalSize > lastTotalSize {
				lastTotalSize = u.TotalSize
				advanced = true
			}
			if advanced {
				lastAdvance.Store(time.Now().UnixNano())
			}
			if onProgress != nil {
				onProgress(u)
			}
		}

		doneWatch := make(chan struct{})
		defer close(doneWatch)
		stallLimit := ffmpegStallTimeout
		if stallLimit > 0 {
			tickInterval := min(max(stallLimit/3, 25*time.Millisecond), 5*time.Second)
			go func() {
				ticker := time.NewTicker(tickInterval)
				defer ticker.Stop()
				for {
					select {
					case <-doneWatch:
						return
					case <-runCtx.Done():
						return
					case <-ticker.C:
						if time.Since(time.Unix(0, lastAdvance.Load())) >= stallLimit && ctx.Err() == nil {
							stalled.Store(true)
							runCancel()
							return
						}
					}
				}
			}()
		}

		readErr := readFFmpegProgress(stdout, wrappedProgress)
		waitErr := cmd.Wait()
		if stalled.Load() && ctx.Err() == nil {
			ch <- done{stderr: stderrTail.String(), err: fmt.Errorf("ffmpeg stalled (no frame progress for %s, last_frame=%d last_out=%.1fs last_size=%d): %s", stallLimit, lastFrame, lastOutSec, lastTotalSize, stderrTail.String())}
			return
		}
		if waitErr != nil {
			ch <- done{stderr: stderrTail.String(), err: fmt.Errorf("ffmpeg exited (%w): %s", waitErr, stderrTail.String())}
			return
		}
		if readErr != nil {
			ch <- done{stderr: stderrTail.String()}
			return
		}
		ch <- done{stderr: stderrTail.String()}
	}()
	d := <-ch
	return d.stderr, d.err
}

func parseFFmpegOutTime(v string) (float64, bool) {
	if v == "" || strings.HasPrefix(v, "-") {
		return 0, false
	}
	parts := strings.Split(v, ":")
	if len(parts) != 3 {
		return 0, false
	}
	h, err1 := strconv.ParseInt(parts[0], 10, 64)
	m, err2 := strconv.ParseInt(parts[1], 10, 64)
	s, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil || h < 0 || m < 0 || m >= 60 || s < 0 || math.IsNaN(s) || math.IsInf(s, 0) {
		return 0, false
	}
	return float64(h)*3600.0 + float64(m)*60.0 + s, true
}

func readFFmpegProgress(r io.Reader, onProgress func(progressUpdate)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 16*1024), 256*1024)
	var cur progressUpdate
	gotOutUS := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch k {
		case "frame":
			if f, err := strconv.ParseInt(v, 10, 64); err == nil && f >= 0 {
				cur.Frame = f
			}
		case "total_size":
			if sz, err := strconv.ParseInt(v, 10, 64); err == nil && sz >= 0 {
				cur.TotalSize = sz
			}
		case "out_time_us":
			if us, err := strconv.ParseInt(v, 10, 64); err == nil && us >= 0 {
				cur.OutSeconds = float64(us) / 1_000_000.0
				gotOutUS = true
			}
		case "out_time_ms":
			// ffmpeg reports out_time_ms in microseconds on some builds unless us already arrived.
			if !gotOutUS {
				if us, err := strconv.ParseInt(v, 10, 64); err == nil && us >= 0 {
					cur.OutSeconds = float64(us) / 1_000_000.0
					gotOutUS = true
				}
			}
		case "out_time":
			if !gotOutUS {
				if sec, ok := parseFFmpegOutTime(v); ok {
					cur.OutSeconds = sec
				}
			}
		case "fps":
			if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
				cur.FPS = f
			}
		case "speed":
			trimmed := strings.TrimSuffix(v, "x")
			if sp, err := strconv.ParseFloat(trimmed, 64); err == nil && sp >= 0 {
				cur.Speed = sp
			}
		case "progress":
			if onProgress != nil {
				onProgress(cur)
			}
			gotOutUS = false
			if v == "end" {
				return nil
			}
		}
	}
	return sc.Err()
}

func ffmpegThreadsFor(numCPU, concurrentJobs int) (decThreads, encThreads int) {
	avail := max(2, numCPU-2)
	perJob := max(2, avail/max(1, concurrentJobs))
	return min(4, perJob), min(6, perJob)
}

func ffmpegThreads(concurrentJobs int) (decThreads, encThreads int) {
	return ffmpegThreadsFor(runtime.NumCPU(), concurrentJobs)
}

func buildCPUArgs(enc string, cfg ProfileConfig, st Settings, plan ScalePlan, tonemapToSDR, useTonemapx bool, colorFlags []string, encThreads int) []string {
	if encThreads <= 0 {
		_, encThreads = ffmpegThreads(st.ConcurrentJobs)
	}
	targetCodec := strings.ToLower(strings.TrimSpace(cfg.TargetCodec))
	tenBit := cfg.TenBit && targetCodec != "h264"
	pixFmt := "yuv420p10le"
	if !tenBit {
		pixFmt = "yuv420p"
	}
	preset := cfg.CPUPreset
	crf := cfg.CPUCRF
	if preset == 0 && cfg.CPUPreset == 0 {
		preset = st.SVTAV1Preset
	}
	if crf == 0 {
		crf = st.SVTAV1CRF
	}
	maxBitrate := cfg.MaxBitrateKbps
	if maxBitrate <= 0 {
		maxBitrate = 10000
	}

	vf := BuildVideoFilter(plan, pixFmt, tonemapToSDR, false, false, useTonemapx)
	args := []string{"-vf", vf}

	switch enc {
	case "libx264":
		x264Preset := "medium"
		if preset <= 2 {
			x264Preset = "slow"
		} else if preset >= 7 {
			x264Preset = "fast"
		}
		args = append(args, "-c:v", "libx264", "-preset", x264Preset, "-crf", strconv.Itoa(crf),
			"-maxrate", strconv.Itoa(maxBitrate)+"k", "-bufsize", strconv.Itoa(maxBitrate*2)+"k")
	case "libx265":
		x265Preset := "medium"
		if preset <= 2 {
			x265Preset = "slow"
		} else if preset >= 7 {
			x265Preset = "fast"
		}
		args = append(args, "-c:v", "libx265", "-preset", x265Preset, "-crf", strconv.Itoa(crf),
			"-maxrate", strconv.Itoa(maxBitrate)+"k", "-bufsize", strconv.Itoa(maxBitrate*2)+"k",
			"-x265-params", fmt.Sprintf("pools=%d", encThreads))
	default:
		args = append(args, "-c:v", "libsvtav1", "-preset", strconv.Itoa(preset), "-crf", strconv.Itoa(crf),
			"-svtav1-params", fmt.Sprintf("mbr=%d:lp=%d", maxBitrate, encThreads))
	}
	args = append(args, "-g", "250")
	args = append(args, colorFlags...)
	return args
}

func isPreInputFlag(tok string) bool {
	switch tok {
	case "-init_hw_device", "-filter_hw_device", "-vaapi_device",
		"-hwaccel", "-hwaccel_device", "-hwaccel_output_format",
		"-extra_hw_frames":
		return true
	}
	return false
}

func assembleFFmpegCmd(effectiveInput string, mapArgs, vEncArgs, tailArgs []string) []string {
	return assembleFFmpegCmdWithThreads(effectiveInput, mapArgs, vEncArgs, tailArgs, 4, 4)
}

func assembleFFmpegCmdWithThreads(effectiveInput string, mapArgs, vEncArgs, tailArgs []string, decThreads, encThreads int) []string {
	if decThreads <= 0 {
		decThreads = 4
	}
	if encThreads <= 0 {
		encThreads = 4
	}
	var preHW, postVideo []string
	for i := 0; i < len(vEncArgs); {
		tok := vEncArgs[i]
		if isPreInputFlag(tok) && i+1 < len(vEncArgs) {
			preHW = append(preHW, tok, vEncArgs[i+1])
			i += 2
		} else {
			postVideo = append(postVideo, tok)
			i++
		}
	}
	out := []string{
		"-hide_banner", "-y", "-nostats",
		"-filter_threads", strconv.Itoa(decThreads),
		"-progress", "pipe:1",
		"-threads", strconv.Itoa(decThreads),
	}
	out = append(out, preHW...)
	out = append(out, "-i", effectiveInput, "-map_metadata", "0")
	out = append(out, mapArgs...)
	out = append(out, postVideo...)
	out = append(out, "-threads", strconv.Itoa(encThreads))
	out = append(out, tailArgs...)
	return out
}

func effectiveEncoderPreference(cfg ProfileConfig, st Settings) string {
	pref := strings.TrimSpace(strings.ToLower(cfg.PreferredEncoder))
	if pref != "" && pref != "auto" {
		return pref
	}
	stPref := strings.TrimSpace(strings.ToLower(st.PreferredEncoder))
	if stPref != "" && stPref != "auto" {
		if codecMatchesTarget(strings.Split(stPref, "_")[0], cfg.TargetCodec) ||
			(cfg.TargetCodec == "av1" && stPref == "libsvtav1") ||
			(cfg.TargetCodec == "hevc" && stPref == "libx265") ||
			(cfg.TargetCodec == "h264" && stPref == "libx264") {
			return stPref
		}
	}
	return "auto"
}

func pickEffectiveEncoderForFile(cfg ProfileConfig, st Settings, hw HardwareInfo) string {
	hasSelectedGPU, vendor := selectedGPUInfo(hw.GPUs)
	return resolveEffectiveEncoder(effectiveEncoderPreference(cfg, st), cfg.TargetCodec, hw.Encoders, hasSelectedGPU, vendor, st.AllowCPUFallback)
}
