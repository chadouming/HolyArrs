package transcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// SampleWindow describes one reference clip window [Start, Start+Duration].
type SampleWindow struct {
	Start    float64
	Duration float64
}

// QualitySearchResult captures the VMAF binary search outcome.
type QualitySearchResult struct {
	SelectedQ           int
	MeanVMAF            float64
	EstimatedVideoBytes int64
	CopyVideoFallback   bool
	Iterations          int
}

// VMAFScoringError indicates libvmaf scoring failed after GPU sample encode succeeded.
type VMAFScoringError struct {
	Err error
}

func (e *VMAFScoringError) Error() string {
	return e.Err.Error()
}

func (e *VMAFScoringError) Unwrap() error {
	return e.Err
}

var vmafScoreRe = regexp.MustCompile(`(?i)VMAF score:\s*([0-9]+(?:\.[0-9]+)?)`)

func roundTo3(v float64) float64 {
	return math.Round(v*1000.0) / 1000.0
}

// SelectSampleWindows picks count evenly spaced windows in [10%, 85%] of duration.
func SelectSampleWindows(duration float64, count int, clipSecs float64) []SampleWindow {
	if clipSecs <= 0 {
		clipSecs = 20.0
	}
	if duration <= 0 || count <= 0 {
		return []SampleWindow{{Start: 0, Duration: math.Max(1.0, clipSecs)}}
	}
	if duration <= clipSecs*1.25 || count == 1 {
		actual := math.Min(clipSecs, math.Max(1.0, duration*0.8))
		start := math.Max(0, (duration-actual)*0.5)
		return []SampleWindow{{Start: roundTo3(start), Duration: roundTo3(actual)}}
	}

	startBound := duration * 0.10
	endBound := duration * 0.85
	usable := math.Max(0, endBound-startBound)

	actualClip := clipSecs
	var maxStart float64
	if usable < float64(count)*clipSecs {
		actualClip = math.Max(2.0, math.Min(clipSecs, (duration*0.80)/float64(count)))
		startBound = duration * 0.05
		maxStart = math.Max(startBound, duration*0.95-actualClip)
	} else {
		maxStart = math.Max(startBound, endBound-actualClip)
	}

	denom := float64(max(1, count-1))
	step := (maxStart - startBound) / denom
	out := make([]SampleWindow, count)
	for i := 0; i < count; i++ {
		out[i] = SampleWindow{
			Start:    roundTo3(startBound + float64(i)*step),
			Duration: roundTo3(actualClip),
		}
	}
	return out
}

// BinarySearchQuality finds the highest compression q in [qMin..qMax] achieving mean_vmaf >= minVMAF.
func BinarySearchQuality(
	evalFn func(q int) (float64, int64, error),
	qMin, qMax int,
	minVMAF float64,
	maxIterations int,
	sampleDurationTotal, fullDuration float64,
	inputVideoBytes int64,
	alreadyNormalized bool,
) (QualitySearchResult, error) {
	if qMin > qMax {
		qMin, qMax = qMax, qMin
	}
	if maxIterations < 1 {
		maxIterations = 5
	}

	projectBytes := func(sampleBytes int64) int64 {
		if sampleDurationTotal > 0 && fullDuration > 0 {
			return int64(math.Round((float64(sampleBytes) / sampleDurationTotal) * fullDuration))
		}
		return sampleBytes
	}

	type evalRes struct {
		score float64
		bytes int64
	}
	memo := make(map[int]evalRes)
	iterations := 0
	eval := func(q int) (float64, int64, error) {
		if r, ok := memo[q]; ok {
			return r.score, r.bytes, nil
		}
		sc, b, err := evalFn(q)
		if err != nil {
			return 0, 0, err
		}
		memo[q] = evalRes{score: sc, bytes: b}
		iterations++
		return sc, b, nil
	}

	scoreMax, bytesMax, err := eval(qMax)
	if err != nil {
		return QualitySearchResult{}, err
	}
	estMax := projectBytes(bytesMax)
	if alreadyNormalized && inputVideoBytes > 0 && estMax >= inputVideoBytes {
		return QualitySearchResult{
			SelectedQ:           qMax,
			MeanVMAF:            scoreMax,
			EstimatedVideoBytes: estMax,
			CopyVideoFallback:   true,
			Iterations:          iterations,
		}, nil
	}
	if scoreMax >= minVMAF {
		return QualitySearchResult{
			SelectedQ:           qMax,
			MeanVMAF:            scoreMax,
			EstimatedVideoBytes: estMax,
			CopyVideoFallback:   false,
			Iterations:          iterations,
		}, nil
	}

	lo := qMin
	hi := qMax - 1
	hasBest := false
	var bestQ int
	var bestScore float64
	var bestBytes int64

	lowestQ := qMax
	lowestScore := scoreMax
	lowestBytes := bytesMax

	for lo <= hi && iterations < maxIterations {
		mid := (lo + hi + 1) / 2
		score, sBytes, err := eval(mid)
		if err != nil {
			return QualitySearchResult{}, err
		}
		if mid < lowestQ {
			lowestQ, lowestScore, lowestBytes = mid, score, sBytes
		}
		if score >= minVMAF {
			hasBest = true
			bestQ, bestScore, bestBytes = mid, score, sBytes
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}

	var selQ int
	var selScore float64
	var selBytes int64
	switch {
	case hasBest:
		selQ, selScore, selBytes = bestQ, bestScore, bestBytes
	case memo[qMin].bytes != 0 || memo[qMin].score != 0:
		r := memo[qMin]
		selQ, selScore, selBytes = qMin, r.score, r.bytes
	case iterations < maxIterations:
		sc, sb, err := eval(qMin)
		if err != nil {
			return QualitySearchResult{}, err
		}
		selQ, selScore, selBytes = qMin, sc, sb
	default:
		selQ, selScore, selBytes = qMin, lowestScore, lowestBytes
	}

	estSel := projectBytes(selBytes)
	copyFB := alreadyNormalized && inputVideoBytes > 0 && estSel >= inputVideoBytes
	return QualitySearchResult{
		SelectedQ:           selQ,
		MeanVMAF:            selScore,
		EstimatedVideoBytes: estSel,
		CopyVideoFallback:   copyFB,
		Iterations:          iterations,
	}, nil
}

func extractVMAFScore(text string) (float64, error) {
	matches := vmafScoreRe.FindAllStringSubmatch(text, -1)
	if len(matches) > 0 {
		v, err := strconv.ParseFloat(matches[len(matches)-1][1], 64)
		if err == nil {
			return v, nil
		}
	}
	var payload struct {
		PooledMetrics struct {
			VMAF struct {
				Mean *float64 `json:"mean"`
			} `json:"vmaf"`
		} `json:"pooled_metrics"`
	}
	if err := json.Unmarshal([]byte(text), &payload); err == nil && payload.PooledMetrics.VMAF.Mean != nil {
		return *payload.PooledMetrics.VMAF.Mean, nil
	}
	tail := text
	if len(tail) > 400 {
		tail = tail[len(tail)-400:]
	}
	return 0, &VMAFScoringError{Err: fmt.Errorf("unable to parse VMAF score from ffmpeg output: %s", tail)}
}

func formatQSVHWDeviceSpec(gpuDevice, name string) string {
	if name == "" {
		name = "qsv"
	}
	return fmt.Sprintf("qsv=%s:hw_any,child_device=%s,child_device_type=vaapi", name, gpuDevice)
}

func normalizeHWPreset(raw string) (string, error) {
	p := strings.TrimSpace(strings.ToLower(raw))
	if p == "" {
		return "medium", nil
	}
	switch p {
	case "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow":
		return p, nil
	default:
		return "", fmt.Errorf("hw_preset must be veryfast, faster, fast, medium, slow, slower, or veryslow")
	}
}

func qsvPresetFor(hwPreset string) string {
	if p, err := normalizeHWPreset(hwPreset); err == nil {
		return p
	}
	return "medium"
}

func nvencPresetFor(hwPreset string) string {
	switch qsvPresetFor(hwPreset) {
	case "veryfast":
		return "p1"
	case "faster":
		return "p2"
	case "fast":
		return "p3"
	case "medium":
		return "p4"
	case "slow":
		return "p5"
	case "slower":
		return "p6"
	case "veryslow":
		return "p7"
	default:
		return "p4"
	}
}

func buildSWFilterPrefix(plan ScalePlan, tonemapToSDR, useTonemapx bool) string {
	prefix := BuildVideoFilter(plan, "yuv420p10le", tonemapToSDR, false, false, useTonemapx)
	if idx := strings.LastIndex(prefix, ",format="); idx >= 0 {
		return prefix[:idx]
	}
	if strings.HasPrefix(prefix, "format=") {
		return ""
	}
	return prefix
}

func qsvPipelineTuning(gpuVRAMMB int, srcHeight int) (extraFrames int, asyncDepth int) {
	if srcHeight >= 2000 {
		switch {
		case gpuVRAMMB > 0 && gpuVRAMMB <= 8500:
			return 16, 4
		case gpuVRAMMB > 0 && gpuVRAMMB < 10000:
			return 24, 4
		default:
			return 40, 4
		}
	}
	switch {
	case gpuVRAMMB > 0 && gpuVRAMMB <= 8500:
		return 24, 4
	case gpuVRAMMB > 0 && gpuVRAMMB < 10000:
		return 32, 4
	default:
		return 48, 4
	}
}

func vaapiHWDecodePre(gpuPath, extraFramesStr string, deriveQSV bool) []string {
	pre := []string{"-init_hw_device", "vaapi=va:" + gpuPath}
	if deriveQSV {
		pre = append(pre, "-init_hw_device", "qsv=qsv@va")
	}
	return append(pre,
		"-filter_hw_device", "va",
		"-hwaccel", "vaapi",
		"-hwaccel_device", "va",
		"-hwaccel_output_format", "vaapi",
		"-extra_hw_frames", extraFramesStr,
	)
}

func gpuEvalArgs(hw HardwareInfo, enc string, gpu *GPUDevice, q int, hwPreset string, tenBit bool) ([]string, []string) {
	pre, args, _ := hwPipelineArgs(hw, ProbeStream{Height: 1080}, ScalePlan{AlreadyNormalized: true}, enc, gpu, q, hwPreset, tenBit, false, false)
	return pre, args
}

func hwPipelineArgs(
	hw HardwareInfo,
	video ProbeStream,
	plan ScalePlan,
	enc string,
	gpu *GPUDevice,
	q int,
	hwPreset string,
	tenBit bool,
	tonemapToSDR bool,
	allowHWDecode bool,
) ([]string, []string, bool) {
	qsvPix := "p010le"
	vaapiPix := "p010"
	if !tenBit {
		qsvPix = "nv12"
		vaapiPix = "nv12"
	}
	gpuPath := ""
	gpuVRAMMB := 0
	if gpu != nil {
		gpuPath = gpu.Path
		gpuVRAMMB = gpu.MemoryMB
	}
	extraFrames, asyncDepth := qsvPipelineTuning(gpuVRAMMB, video.Height)
	extraFramesStr := strconv.Itoa(extraFrames)
	asyncDepthStr := strconv.Itoa(asyncDepth)

	isDV5 := DetectSourceHDR(video) == "dv5"
	hwDecodeOK := allowHWDecode && canHWDecode(video, enc) && (gpuPath != "" || isNVENCEncoder(enc))

	switch {
	case strings.HasSuffix(enc, "_qsv"):
		encFlags := []string{
			"-c:v", enc,
			"-preset", qsvPresetFor(hwPreset),
			"-async_depth", asyncDepthStr,
			"-global_quality", strconv.Itoa(q),
		}
		if hwDecodeOK && hw.ScaleVAAPIAvail && !plan.NeedsPad && !tonemapToSDR {
			pre := vaapiHWDecodePre(gpuPath, extraFramesStr, true)
			vf := fmt.Sprintf("scale_vaapi=format=%s,hwmap=derive_device=qsv,format=qsv", vaapiPix)
			if !plan.AlreadyNormalized {
				vf = fmt.Sprintf("scale_vaapi=w=%d:h=%d:format=%s,hwmap=derive_device=qsv,format=qsv", plan.ScaledW, plan.ScaledH, vaapiPix)
				encFlags = append(encFlags, "-aspect", fmt.Sprintf("%d:%d", plan.FrameW, plan.FrameH))
			}
			return pre, append([]string{"-noautoscale", "-vf", vf}, encFlags...), true
		}
		if hwDecodeOK && hw.VPPQSVAvail && !plan.NeedsPad && !tonemapToSDR {
			pre := []string{
				"-init_hw_device", "vaapi=va:" + gpuPath,
				"-init_hw_device", "qsv=qsv@va",
				"-filter_hw_device", "qsv",
				"-hwaccel", "qsv",
				"-hwaccel_device", "qsv",
				"-hwaccel_output_format", "qsv",
				"-extra_hw_frames", extraFramesStr,
			}
			vf := "vpp_qsv=format=" + qsvPix
			if !plan.AlreadyNormalized {
				vf = fmt.Sprintf("vpp_qsv=w=%d:h=%d:format=%s", plan.ScaledW, plan.ScaledH, qsvPix)
				encFlags = append(encFlags, "-aspect", fmt.Sprintf("%d:%d", plan.FrameW, plan.FrameH))
			}
			return pre, append([]string{"-noautoscale", "-vf", vf}, encFlags...), true
		}
		if hwDecodeOK && plan.NeedsPad && !tonemapToSDR && hw.ScaleVAAPIAvail && hw.PadVAAPIAvail {
			pre := vaapiHWDecodePre(gpuPath, extraFramesStr, true)
			vf := fmt.Sprintf("scale_vaapi=w=%d:h=%d:format=%s,pad_vaapi=w=%d:h=%d:x=%d:y=%d:color=black,hwmap=derive_device=qsv,format=qsv",
				plan.ScaledW, plan.ScaledH, vaapiPix, plan.FrameW, plan.FrameH, plan.PadX, plan.PadY)
			if !plan.AlreadyNormalized {
				encFlags = append(encFlags, "-aspect", fmt.Sprintf("%d:%d", plan.FrameW, plan.FrameH))
			}
			return pre, append([]string{"-noautoscale", "-vf", vf}, encFlags...), true
		}
		if hwDecodeOK && tonemapToSDR && !isDV5 && hw.TonemapVAAPIAvail && hw.ScaleVAAPIAvail && !plan.NeedsPad {
			pre := vaapiHWDecodePre(gpuPath, extraFramesStr, true)
			vf := ""
			if !plan.AlreadyNormalized {
				vf = fmt.Sprintf("scale_vaapi=w=%d:h=%d,", plan.ScaledW, plan.ScaledH)
				encFlags = append(encFlags, "-aspect", fmt.Sprintf("%d:%d", plan.FrameW, plan.FrameH))
			}
			vf += fmt.Sprintf("tonemap_vaapi=format=%s:matrix=bt709:primaries=bt709:transfer=bt709:tonemap=hable:desat=0.2,hwmap=derive_device=qsv,format=qsv", vaapiPix)
			return pre, append([]string{"-noautoscale", "-vf", vf}, encFlags...), true
		}

		pre := []string{
			"-init_hw_device", formatQSVHWDeviceSpec(gpuPath, "qsv"),
			"-filter_hw_device", "qsv",
			"-extra_hw_frames", extraFramesStr,
		}
		vf := ""
		if allowHWDecode || !plan.AlreadyNormalized || tonemapToSDR {
			vf = buildSWFilterPrefix(plan, tonemapToSDR, hw.TonemapxAvail)
		}
		if vf != "" {
			vf += ","
		}
		vf += "format=" + qsvPix
		return pre, append([]string{"-vf", vf}, encFlags...), false

	case strings.HasSuffix(enc, "_vaapi"):
		encFlags := []string{
			"-c:v", enc,
			"-rc_mode", "ICQ",
			"-global_quality", strconv.Itoa(q),
		}
		if hwDecodeOK && hw.ScaleVAAPIAvail && !plan.NeedsPad && !tonemapToSDR {
			pre := vaapiHWDecodePre(gpuPath, extraFramesStr, false)
			vf := "scale_vaapi=format=" + vaapiPix
			if !plan.AlreadyNormalized {
				vf = fmt.Sprintf("scale_vaapi=w=%d:h=%d:format=%s", plan.ScaledW, plan.ScaledH, vaapiPix)
				encFlags = append(encFlags, "-aspect", fmt.Sprintf("%d:%d", plan.FrameW, plan.FrameH))
			}
			return pre, append([]string{"-noautoscale", "-vf", vf}, encFlags...), true
		}
		if hwDecodeOK && plan.NeedsPad && !tonemapToSDR && hw.ScaleVAAPIAvail && hw.PadVAAPIAvail {
			pre := vaapiHWDecodePre(gpuPath, extraFramesStr, false)
			vf := fmt.Sprintf("scale_vaapi=w=%d:h=%d:format=%s,pad_vaapi=w=%d:h=%d:x=%d:y=%d:color=black",
				plan.ScaledW, plan.ScaledH, vaapiPix, plan.FrameW, plan.FrameH, plan.PadX, plan.PadY)
			if !plan.AlreadyNormalized {
				encFlags = append(encFlags, "-aspect", fmt.Sprintf("%d:%d", plan.FrameW, plan.FrameH))
			}
			return pre, append([]string{"-noautoscale", "-vf", vf}, encFlags...), true
		}
		if hwDecodeOK && tonemapToSDR && !isDV5 && hw.TonemapVAAPIAvail && hw.ScaleVAAPIAvail && !plan.NeedsPad {
			pre := vaapiHWDecodePre(gpuPath, extraFramesStr, false)
			vf := ""
			if !plan.AlreadyNormalized {
				vf = fmt.Sprintf("scale_vaapi=w=%d:h=%d,", plan.ScaledW, plan.ScaledH)
				encFlags = append(encFlags, "-aspect", fmt.Sprintf("%d:%d", plan.FrameW, plan.FrameH))
			}
			vf += fmt.Sprintf("tonemap_vaapi=format=%s:matrix=bt709:primaries=bt709:transfer=bt709:tonemap=hable:desat=0.2", vaapiPix)
			return pre, append([]string{"-noautoscale", "-vf", vf}, encFlags...), true
		}

		pre := []string{"-vaapi_device", gpuPath}
		vf := ""
		if allowHWDecode || !plan.AlreadyNormalized || tonemapToSDR {
			vf = buildSWFilterPrefix(plan, tonemapToSDR, hw.TonemapxAvail)
		}
		if vf != "" {
			vf += ","
		}
		vf += fmt.Sprintf("format=%s,hwupload=extra_hw_frames=%d", qsvPix, extraFrames)
		return pre, append([]string{"-vf", vf}, encFlags...), false

	case strings.HasSuffix(enc, "_vulkan"):
		vf := ""
		if allowHWDecode || !plan.AlreadyNormalized || tonemapToSDR {
			vf = buildSWFilterPrefix(plan, tonemapToSDR, hw.TonemapxAvail)
		}
		pre, args := hardwareVideoArgs(enc, gpu, q, hwPreset, asyncDepth, tenBit, vf)
		return pre, args, false

	case strings.HasSuffix(enc, "_nvenc"):
		var pre []string
		if hwDecodeOK {
			pre = []string{"-hwaccel", "cuda"}
		}
		vf := ""
		if allowHWDecode || !plan.AlreadyNormalized || tonemapToSDR {
			vf = buildSWFilterPrefix(plan, tonemapToSDR, hw.TonemapxAvail)
		}
		_, args := hardwareVideoArgs(enc, gpu, q, hwPreset, asyncDepth, tenBit, vf)
		return pre, args, hwDecodeOK

	default:
		pre, args := hardwareVideoArgs(enc, gpu, q, hwPreset, asyncDepth, tenBit, buildSWFilterPrefix(plan, tonemapToSDR, hw.TonemapxAvail))
		return pre, args, false
	}
}

func hardwareVideoArgs(enc string, gpu *GPUDevice, q int, hwPreset string, asyncDepth int, tenBit bool, vfPrefix string) ([]string, []string) {
	hwPix := "p010le"
	if !tenBit {
		hwPix = "nv12"
	}
	gpuPath := ""
	gpuVRAMMB := 0
	if gpu != nil {
		gpuPath = gpu.Path
		gpuVRAMMB = gpu.MemoryMB
	}
	extraFrames, defaultAsync := qsvPipelineTuning(gpuVRAMMB, 0)
	if asyncDepth <= 0 {
		asyncDepth = defaultAsync
	}

	switch {
	case strings.HasSuffix(enc, "_vaapi"):
		pre := []string{"-vaapi_device", gpuPath}
		vf := vfPrefix
		if vf != "" {
			vf += ","
		}
		vf += fmt.Sprintf("format=%s,hwupload=extra_hw_frames=%d", hwPix, extraFrames)
		args := []string{
			"-vf", vf,
			"-c:v", enc,
			"-rc_mode", "ICQ",
			"-global_quality", strconv.Itoa(q),
		}
		return pre, args

	case strings.HasSuffix(enc, "_vulkan"):
		pre := []string{"-init_hw_device", "vulkan=vk", "-filter_hw_device", "vk"}
		vf := vfPrefix
		if vf != "" {
			vf += ","
		}
		vf += "format=" + hwPix + ",hwupload"
		args := []string{
			"-vf", vf,
			"-c:v", enc,
			"-qp", strconv.Itoa(q),
		}
		return pre, args

	case strings.HasSuffix(enc, "_nvenc"):
		vf := vfPrefix
		if vf != "" {
			vf += ","
		}
		vf += "format=" + hwPix
		args := []string{
			"-vf", vf,
			"-c:v", enc,
			"-preset", nvencPresetFor(hwPreset),
			"-cq", strconv.Itoa(q),
		}
		return nil, args

	default:
		// QSV encoder: explicit VAAPI child_device binding matches Alchemist.
		pre := []string{
			"-init_hw_device", formatQSVHWDeviceSpec(gpuPath, "qsv"),
			"-filter_hw_device", "qsv",
			"-extra_hw_frames", strconv.Itoa(extraFrames),
		}
		vf := vfPrefix
		if vf != "" {
			vf += ","
		}
		vf += "format=" + hwPix
		args := []string{
			"-vf", vf,
			"-c:v", enc,
			"-preset", qsvPresetFor(hwPreset),
			"-async_depth", strconv.Itoa(asyncDepth),
			"-global_quality", strconv.Itoa(q),
		}
		return pre, args
	}
}

// RunGPUVMAFSearch runs sample-averaged VMAF binary search for a GPU encoder.
func RunGPUVMAFSearch(
	ctx context.Context,
	hw HardwareInfo,
	cfg ProfileConfig,
	effectiveInput string,
	relVideoIdx int,
	plan ScalePlan,
	tonemapToSDR bool,
	colorFlags []string,
	videoCodec string,
	gpu *GPUDevice,
	sourceFPS float64,
	fullDuration float64,
	inputVideoBytes int64,
	workDir string,
	concurrentJobs int,
) (QualitySearchResult, error) {
	if !cfg.VMAFEnabled || !hw.VMAFAvailable {
		midQ := (cfg.QMin + cfg.QMax) / 2
		return QualitySearchResult{
			SelectedQ:  midQ,
			MeanVMAF:   cfg.VMAFMinScore,
			Iterations: 0,
		}, nil
	}
	decThreads, _ := ffmpegThreads(concurrentJobs)
	decStr := strconv.Itoa(decThreads)

	targetCodec := strings.ToLower(strings.TrimSpace(cfg.TargetCodec))
	tenBit := cfg.TenBit && targetCodec != "h264"
	refPix := "yuv420p10le"
	if !tenBit {
		refPix = "yuv420p"
	}

	windows := SelectSampleWindows(fullDuration, cfg.VMAFSamples, float64(cfg.VMAFSampleSeconds))
	var totalSampleSecs float64
	for _, w := range windows {
		totalSampleSecs += w.Duration
	}
	vmafFPS := float64(cfg.VMAFFPS)
	if vmafFPS <= 0 {
		vmafFPS = 12
	}
	nSubsample := int(math.Round(sourceFPS / math.Max(1.0, vmafFPS)))
	if nSubsample < 1 {
		nSubsample = 1
	}

	refVF := BuildVideoFilter(plan, refPix, tonemapToSDR, false, false, hw.TonemapxAvail)
	refCodecArgs := []string{"-c:v", "ffv1"}
	if hw.hasLibx264 {
		refCodecArgs = []string{"-c:v", "libx264", "-preset", "ultrafast", "-crf", "10"}
	}

	refClips := make([]string, 0, len(windows))
	for i, w := range windows {
		refPath := filepath.Join(workDir, fmt.Sprintf("ref_%02d.mkv", i))
		args := []string{
			"-hide_banner", "-y", "-nostats",
			"-filter_threads", decStr,
			"-threads", decStr,
			"-ss", fmt.Sprintf("%.3f", w.Start),
			"-t", fmt.Sprintf("%.3f", w.Duration),
			"-i", effectiveInput,
			"-map", fmt.Sprintf("0:v:%d", relVideoIdx),
			"-an", "-sn",
			"-vf", refVF,
			"-threads", decStr,
		}
		args = append(args, refCodecArgs...)
		args = append(args, "-pix_fmt", refPix)
		args = append(args, colorFlags...)
		args = append(args, "-f", "matroska", refPath)

		res, err := runSubproc(ctx, nil, hw.FFmpegPath, args...)
		if err != nil {
			return QualitySearchResult{}, fmt.Errorf("extract VMAF reference clip %d: %w (%s)", i, err, strings.TrimSpace(res.Stderr))
		}
		refClips = append(refClips, refPath)
	}

	vmafBin := hw.VMAFFFmpegPath
	if vmafBin == "" {
		vmafBin = hw.FFmpegPath
	}

	evalQ := func(qVal int) (float64, int64, error) {
		var scores []float64
		var sampleBytes int64
		for i, refClip := range refClips {
			candPath := filepath.Join(workDir, fmt.Sprintf("cand_q%d_%02d.mkv", qVal, i))
			preHW, encArgs := gpuEvalArgs(hw, videoCodec, gpu, qVal, cfg.HWPreset, tenBit)

			cmdEnc := []string{"-hide_banner", "-y", "-nostats", "-filter_threads", decStr, "-threads", decStr}
			cmdEnc = append(cmdEnc, preHW...)
			cmdEnc = append(cmdEnc, "-i", refClip, "-map", "0:v:0", "-an", "-sn")
			cmdEnc = append(cmdEnc, encArgs...)
			cmdEnc = append(cmdEnc, "-threads", decStr)
			cmdEnc = append(cmdEnc, "-g", "250")
			cmdEnc = append(cmdEnc, colorFlags...)
			cmdEnc = append(cmdEnc, "-f", "matroska", candPath)

			resEnc, err := runSubproc(ctx, gpu, hw.FFmpegPath, cmdEnc...)
			if err != nil {
				_ = os.Remove(candPath)
				gpuPath := ""
				if gpu != nil {
					gpuPath = gpu.Path
				}
				return 0, 0, fmt.Errorf("error while opening encoder %s on %s: %w (%s)", videoCodec, gpuPath, err, strings.TrimSpace(resEnc.Stderr))
			}

			if st, statErr := os.Stat(candPath); statErr == nil {
				sampleBytes += st.Size()
			}

			lavfi := fmt.Sprintf("[0:v]setpts=PTS-STARTPTS[d];[1:v]setpts=PTS-STARTPTS[r];[d][r]libvmaf=n_subsample=%d:n_threads=%d", nSubsample, decThreads)
			resVMAF, vmafErr := runSubproc(ctx, nil, vmafBin,
				"-hide_banner", "-nostats",
				"-filter_complex_threads", decStr,
				"-threads", decStr,
				"-i", candPath,
				"-threads", decStr,
				"-i", refClip,
				"-lavfi", lavfi,
				"-f", "null", "-",
			)
			_ = os.Remove(candPath)

			if vmafErr != nil {
				return 0, 0, &VMAFScoringError{Err: fmt.Errorf("libvmaf failed for q=%d: %w (%s)", qVal, vmafErr, strings.TrimSpace(resVMAF.Stderr))}
			}
			sc, parseErr := extractVMAFScore(resVMAF.Stderr + "\n" + resVMAF.Stdout)
			if parseErr != nil {
				return 0, 0, parseErr
			}
			scores = append(scores, sc)
		}
		var sum float64
		for _, sc := range scores {
			sum += sc
		}
		mean := sum / float64(max(1, len(scores)))
		return mean, sampleBytes, nil
	}

	res, err := BinarySearchQuality(
		evalQ,
		cfg.QMin,
		cfg.QMax,
		cfg.VMAFMinScore,
		cfg.MaxIterations,
		totalSampleSecs,
		fullDuration,
		inputVideoBytes,
		cfg.CopyIfLarger && plan.AlreadyNormalized && !tonemapToSDR,
	)
	if err != nil {
		var scoreErr *VMAFScoringError
		if errors.As(err, &scoreErr) {
			return QualitySearchResult{
				SelectedQ:  (cfg.QMin + cfg.QMax) / 2,
				MeanVMAF:   cfg.VMAFMinScore,
				Iterations: 1,
			}, nil
		}
		return QualitySearchResult{}, err
	}
	return res, nil
}
