package transcode

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

const fileFlowsCommentTag = "Created by FileFlows https://fileflows.com"

// ProbeResult is the subset of ffprobe JSON output inspected by transcode.
type ProbeResult struct {
	Format  ProbeFormat   `json:"format"`
	Streams []ProbeStream `json:"streams"`
}

// ProbeFormat represents the container section of ffprobe JSON output.
type ProbeFormat struct {
	Filename string            `json:"filename"`
	Duration string            `json:"duration"`
	Size     string            `json:"size"`
	BitRate  string            `json:"bit_rate"`
	Tags     map[string]string `json:"tags"`
}

// ProbeStream represents one stream entry of ffprobe JSON output.
type ProbeStream struct {
	Index             int               `json:"index"`
	CodecName         string            `json:"codec_name"`
	CodecType         string            `json:"codec_type"`
	Width             int               `json:"width"`
	Height            int               `json:"height"`
	SampleAspectRatio string            `json:"sample_aspect_ratio"`
	AvgFrameRate      string            `json:"avg_frame_rate"`
	RFrameRate        string            `json:"r_frame_rate"`
	Duration          string            `json:"duration"`
	BitRate           string            `json:"bit_rate"`
	Channels          int               `json:"channels"`
	ColorPrimaries    string            `json:"color_primaries"`
	ColorTransfer     string            `json:"color_transfer"`
	ColorSpace        string            `json:"color_space"`
	ColorRange        string            `json:"color_range"`
	PixFmt            string            `json:"pix_fmt"`
	FieldOrder        string            `json:"field_order"`
	Disposition       map[string]int    `json:"disposition"`
	Tags              map[string]string `json:"tags"`
	SideDataList      []ProbeSideData   `json:"side_data_list"`
}

// ProbeSideData captures Dolby Vision profile metadata attached to video streams.
type ProbeSideData struct {
	SideDataType string `json:"side_data_type"`
	DVProfile    *int   `json:"dv_profile"`
}

// ProbeMedia runs ffprobe in JSON mode on mediaPath.
func ProbeMedia(ctx context.Context, ffprobeBin, mediaPath string) (ProbeResult, error) {
	if ffprobeBin == "" {
		return ProbeResult{}, fmt.Errorf("ffprobe binary not found")
	}
	res, err := runSubproc(ctx, nil, ffprobeBin,
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		mediaPath,
	)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("ffprobe %s: %w (%s)", mediaPath, err, strings.TrimSpace(res.Stderr))
	}
	var out ProbeResult
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		return ProbeResult{}, fmt.Errorf("parse ffprobe json for %s: %w", mediaPath, err)
	}
	return out, nil
}

// MainVideoStream selects the first non-attached-picture video stream and its relative index.
func MainVideoStream(streams []ProbeStream) (int, ProbeStream, bool) {
	relIdx := -1
	var firstVideo ProbeStream
	hasFirst := false
	for _, st := range streams {
		if st.CodecType != "video" {
			continue
		}
		relIdx++
		if !hasFirst {
			firstVideo = st
			hasFirst = true
		}
		if st.Disposition["attached_pic"] == 0 {
			return relIdx, st, true
		}
	}
	if hasFirst {
		return 0, firstVideo, true
	}
	return 0, ProbeStream{}, false
}

// ParseRatio parses "num:den" or "num/den", returning def on invalid input.
func ParseRatio(raw string, defNum, defDen int) (int, int) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "N/A" || raw == "0:0" || raw == "0/0" {
		return defNum, defDen
	}
	sep := ""
	if strings.Contains(raw, ":") {
		sep = ":"
	} else if strings.Contains(raw, "/") {
		sep = "/"
	} else {
		return defNum, defDen
	}
	left, right, ok := strings.Cut(raw, sep)
	if !ok {
		return defNum, defDen
	}
	num, err1 := strconv.Atoi(strings.TrimSpace(left))
	den, err2 := strconv.Atoi(strings.TrimSpace(right))
	if err1 != nil || err2 != nil || num <= 0 || den <= 0 {
		return defNum, defDen
	}
	return num, den
}

// ParseFPS parses ffprobe frame rate strings ("24000/1001" or float), returning def on invalid input.
func ParseFPS(raw string, def float64) float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0/0" || raw == "N/A" {
		return def
	}
	if left, right, ok := strings.Cut(raw, "/"); ok {
		num, err1 := strconv.ParseFloat(strings.TrimSpace(left), 64)
		den, err2 := strconv.ParseFloat(strings.TrimSpace(right), 64)
		if err1 == nil && err2 == nil && num > 0 && den > 0 {
			return num / den
		}
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 {
		return def
	}
	return v
}

// ParseDuration extracts container or stream duration in seconds.
func ParseDuration(probe ProbeResult, v ProbeStream) float64 {
	if d, err := strconv.ParseFloat(strings.TrimSpace(probe.Format.Duration), 64); err == nil && d > 0 {
		return d
	}
	if d, err := strconv.ParseFloat(strings.TrimSpace(v.Duration), 64); err == nil && d > 0 {
		return d
	}
	return 0
}

// ParseFormatSize returns ffprobe format size in bytes or falls back to fileStatSize.
func ParseFormatSize(probe ProbeResult, fileStatSize int64) int64 {
	if sz, err := strconv.ParseInt(strings.TrimSpace(probe.Format.Size), 10, 64); err == nil && sz > 0 {
		return sz
	}
	return fileStatSize
}

// EstimateInputVideoBytes subtracts approximate audio stream bitrates from inputSize.
func EstimateInputVideoBytes(probe ProbeResult, inputSize int64, duration float64) int64 {
	if inputSize <= 0 {
		return 0
	}
	var audioBps int64
	for _, st := range probe.Streams {
		if st.CodecType != "audio" {
			continue
		}
		if b, err := strconv.ParseInt(strings.TrimSpace(st.BitRate), 10, 64); err == nil && b > 0 {
			audioBps += b
		}
	}
	var estAudio int64
	if duration > 0 && audioBps > 0 {
		estAudio = int64((float64(audioBps) / 8.0) * duration)
	}
	floor := inputSize / 2
	rem := inputSize - estAudio
	if rem < floor {
		return floor
	}
	return rem
}

// ProbeTagValues returns all non-empty container format tag values.
func ProbeTagValues(probe ProbeResult) []string {
	if len(probe.Format.Tags) == 0 {
		return nil
	}
	out := make([]string, 0, len(probe.Format.Tags))
	for _, v := range probe.Format.Tags {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}

// MatchSkipTags returns true if any container tag value contains a configured skip tag (case-insensitive).
func MatchSkipTags(tagValues []string, skipTags []string) bool {
	for _, rawSkip := range skipTags {
		needle := strings.ToLower(strings.TrimSpace(rawSkip))
		if needle == "" {
			continue
		}
		for _, rawVal := range tagValues {
			if strings.Contains(strings.ToLower(rawVal), needle) {
				return true
			}
		}
	}
	return false
}

// DetectSkipTag returns true if container metadata contains any configured skip tag.
func DetectSkipTag(probe ProbeResult, skipTags []string) bool {
	return MatchSkipTags(ProbeTagValues(probe), skipTags)
}

// DetectFileFlowsTag returns true if container metadata contains the FileFlows signature.
func DetectFileFlowsTag(probe ProbeResult) bool {
	return DetectSkipTag(probe, []string{"Created by FileFlows"})
}

// DetectSourceHDR classifies the video stream's HDR format: dv5, dv, hdr10, hlg, or "".
func DetectSourceHDR(v ProbeStream) string {
	prim := strings.ToLower(strings.TrimSpace(v.ColorPrimaries))
	trc := strings.ToLower(strings.TrimSpace(v.ColorTransfer))
	sp := strings.ToLower(strings.TrimSpace(v.ColorSpace))

	hasDV := false
	hasDV5 := false
	hasDVFallback := false
	for _, sd := range v.SideDataList {
		if sd.DVProfile == nil {
			continue
		}
		hasDV = true
		if *sd.DVProfile == 5 {
			hasDV5 = true
		}
		if *sd.DVProfile == 7 || *sd.DVProfile == 8 {
			hasDVFallback = true
		}
	}
	if sp == "ipt-c2" || (hasDV5 && trc != "smpte2084" && trc != "arib-std-b67") || (hasDV5 && !hasDVFallback) {
		return "dv5"
	}
	if hasDV {
		return "dv"
	}
	if trc == "smpte2084" {
		return "hdr10"
	}
	if trc == "arib-std-b67" {
		return "hlg"
	}
	if prim == "bt2020" {
		return "hdr10"
	}
	return ""
}

func codecMatchesTarget(codecName, targetCodec string) bool {
	c := strings.ToLower(strings.TrimSpace(codecName))
	t := strings.ToLower(strings.TrimSpace(targetCodec))
	if t == "" {
		t = "av1"
	}
	switch t {
	case "av1":
		return c == "av1"
	case "hevc":
		return c == "hevc" || c == "h265"
	case "h264":
		return c == "h264" || c == "avc" || c == "avc1"
	default:
		return c == t
	}
}

func canHWDecode(video ProbeStream, encoder string) bool {
	if !strings.HasSuffix(encoder, "_qsv") && !strings.HasSuffix(encoder, "_vaapi") && !strings.HasSuffix(encoder, "_nvenc") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(video.FieldOrder)) {
	case "tt", "bb", "tb", "bt":
		return false
	}
	pix := strings.ToLower(strings.TrimSpace(video.PixFmt))
	is8Bit420 := pix == "" || pix == "yuv420p" || pix == "yuvj420p" || pix == "nv12"
	is8Or10Bit420 := is8Bit420 || pix == "yuv420p10le" || pix == "p010le"

	switch strings.ToLower(strings.TrimSpace(video.CodecName)) {
	case "h264", "avc", "avc1", "mpeg2video", "mpeg2":
		return is8Bit420
	case "hevc", "h265", "vp9", "av1":
		return is8Or10Bit420
	default:
		return false
	}
}

// EvaluateProbe decides whether a probed file should be skipped under cfg.
func EvaluateProbe(probe ProbeResult, mediaPath string, fileSizeBytes int64, cfg ProfileConfig, libraryEnabled, force bool) (bool, string) {
	_, v, ok := MainVideoStream(probe.Streams)
	return evaluateSkipDecision(ok, v.CodecName, ProbeTagValues(probe), mediaPath, fileSizeBytes, cfg, libraryEnabled, force)
}

func evaluateSkipDecision(hasVideo bool, codecName string, tagValues []string, mediaPath string, fileSizeBytes int64, cfg ProfileConfig, libraryEnabled, force bool) (bool, string) {
	if !force && !libraryEnabled {
		return true, "library_disabled"
	}
	if !hasVideo {
		return true, "no_video"
	}
	if !force && cfg.MinFileSizeMB > 0 && fileSizeBytes > 0 && fileSizeBytes < int64(cfg.MinFileSizeMB)*1024*1024 {
		return true, "below_min_size"
	}
	if !force {
		if len(cfg.SkipTags) > 0 {
			if MatchSkipTags(tagValues, cfg.SkipTags) {
				return true, "tag_matched"
			}
		} else if filepath.Ext(strings.ToLower(mediaPath)) == ".mkv" {
			target := strings.ToLower(strings.TrimSpace(cfg.TargetCodec))
			if target == "" {
				target = "av1"
			}
			if codecMatchesTarget(codecName, target) {
				return true, "already_" + target
			}
		}
	}
	return false, ""
}
