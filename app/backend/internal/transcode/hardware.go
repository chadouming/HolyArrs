package transcode

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"
)

// HardwareInfo summarizes discovered binaries, filters, encoders, and GPUs.
type HardwareInfo struct {
	Ready             bool        `json:"ready"`
	FFmpegFound       bool        `json:"ffmpeg_found"`
	FFprobeFound      bool        `json:"ffprobe_found"`
	VMAFAvailable     bool        `json:"vmaf_available"`
	FFmpegPath        string      `json:"ffmpeg_path"`
	FFprobePath       string      `json:"ffprobe_path"`
	VMAFFFmpegPath    string      `json:"vmaf_ffmpeg_path"`
	TonemapxAvail     bool        `json:"tonemapx_available"`
	ZscaleAvail       bool        `json:"zscale_available"`
	VPPQSVAvail       bool        `json:"-"`
	ScaleVAAPIAvail   bool        `json:"-"`
	PadVAAPIAvail     bool        `json:"-"`
	TonemapVAAPIAvail bool        `json:"-"`
	Encoders          []string    `json:"encoders"`
	PreferredEncoder  string      `json:"preferred_encoder"`
	GPUs              []GPUDevice `json:"gpus"`

	hasLibx264 bool
}

var knownEncoders = []string{
	"av1_qsv", "av1_vaapi", "av1_nvenc", "av1_vulkan", "libsvtav1",
	"hevc_qsv", "hevc_vaapi", "hevc_nvenc", "hevc_vulkan", "libx265",
	"h264_qsv", "h264_vaapi", "h264_nvenc", "h264_vulkan", "libx264",
}

func resolveBinary(envKey string, candidates []string, lookName string) string {
	if envKey != "" {
		if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
			if st, err := os.Stat(v); err == nil && !st.IsDir() {
				return v
			}
		}
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	if lookName != "" {
		if p, err := exec.LookPath(lookName); err == nil {
			return p
		}
	}
	return ""
}

func probeCommandOutput(ctx context.Context, bin string, args ...string) string {
	if bin == "" {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, args...)
	out, _ := cmd.CombinedOutput()
	return string(out)
}

func hasWordMatch(text, word string) bool {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(word) + `\b`)
	return re.MatchString(text)
}

// ProbeHardware inspects ffmpeg/ffprobe binaries, encoders, filters, and GPUs,
// refreshing the cached static probe.
func (s *Service) ProbeHardware(ctx context.Context) HardwareInfo {
	st := ReadSettings(ctx, s.Store.Read())
	return s.probeHardware(ctx, st, true)
}

func (s *Service) probeHardwareWithSettings(ctx context.Context, st Settings) HardwareInfo {
	return s.probeHardware(ctx, st, false)
}

type hwBinarySig struct {
	path    string
	size    int64
	mtimeNs int64
}

type cachedHardwareProbe struct {
	ffmpegSig         hwBinarySig
	ffprobeSig        hwBinarySig
	vmafSig           hwBinarySig
	encoders          []string
	tonemapxAvail     bool
	zscaleAvail       bool
	vppQSVAvail       bool
	scaleVAAPIAvail   bool
	padVAAPIAvail     bool
	tonemapVAAPIAvail bool
	vmafAvail         bool
	hasLibx264        bool
}

func binarySig(path string) hwBinarySig {
	if path == "" {
		return hwBinarySig{}
	}
	st, err := os.Stat(path)
	if err != nil {
		return hwBinarySig{path: path}
	}
	return hwBinarySig{
		path:    path,
		size:    st.Size(),
		mtimeNs: st.ModTime().UnixNano(),
	}
}

func (s *Service) refreshVRAM() {
	probed := s.GPUVRAMProbe(context.Background())
	s.vramMu.Lock()
	s.vramCache = append([]GPUVRAMInfo(nil), probed...)
	s.vramCacheAt = time.Now()
	s.vramRefreshing = false
	s.vramMu.Unlock()
}

func (s *Service) probeVRAMCached(ctx context.Context, forceRefresh bool) []GPUVRAMInfo {
	if s.GPUVRAMProbe == nil {
		return nil
	}
	for {
		s.vramMu.Lock()
		if !forceRefresh && !s.vramCacheAt.IsZero() {
			if time.Since(s.vramCacheAt) >= 30*time.Second && !s.vramRefreshing {
				s.vramRefreshing = true
				go s.refreshVRAM()
			}
			cached := append([]GPUVRAMInfo(nil), s.vramCache...)
			s.vramMu.Unlock()
			return cached
		}
		if !forceRefresh && s.vramWait != nil {
			wait := s.vramWait
			s.vramMu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil
			}
		}
		var ch chan struct{}
		if !forceRefresh {
			ch = make(chan struct{})
			s.vramWait = ch
		}
		s.vramMu.Unlock()

		probed := s.GPUVRAMProbe(ctx)
		s.vramMu.Lock()
		if forceRefresh || ctx.Err() == nil {
			s.vramCache = append([]GPUVRAMInfo(nil), probed...)
			s.vramCacheAt = time.Now()
		}
		if ch != nil {
			s.vramWait = nil
			close(ch)
		}
		s.vramMu.Unlock()
		return append([]GPUVRAMInfo(nil), probed...)
	}
}

func (s *Service) probeHardware(ctx context.Context, st Settings, forceRefresh bool) HardwareInfo {
	ffmpegPath := resolveBinary("HOLYARRS_FFMPEG", []string{"/usr/bin/ffmpeg", "/usr/lib/jellyfin-ffmpeg/ffmpeg"}, "ffmpeg")
	ffprobePath := resolveBinary("HOLYARRS_FFPROBE", []string{"/usr/bin/ffprobe", "/usr/lib/jellyfin-ffmpeg/ffprobe"}, "ffprobe")
	vmafPath := resolveBinary("HOLYARRS_VMAF_FFMPEG", []string{"/usr/local/bin/ffmpeg-vmaf"}, "")
	if vmafPath == "" {
		vmafPath = ffmpegPath
	}

	ffSig := binarySig(ffmpegPath)
	fpSig := binarySig(ffprobePath)
	vmSig := binarySig(vmafPath)

	info := HardwareInfo{
		FFmpegFound:    ffmpegPath != "",
		FFprobeFound:   ffprobePath != "",
		FFmpegPath:     ffmpegPath,
		FFprobePath:    ffprobePath,
		VMAFFFmpegPath: vmafPath,
		Encoders:       []string{},
		GPUs:           DiscoverGPUs(s.DevDRI, s.SysfsDRM, st.SelectedGPUs, st.GPUDevices, s.gpus.busyCount),
	}
	if vram := s.probeVRAMCached(ctx, forceRefresh); len(vram) > 0 {
		info.GPUs = enrichGPUsWithVRAM(info.GPUs, vram)
	}

	var cached *cachedHardwareProbe
	if !forceRefresh {
		s.hwMu.Lock()
		if c := s.hwCache; c != nil && c.ffmpegSig == ffSig && c.ffprobeSig == fpSig && c.vmafSig == vmSig {
			copyCache := *c
			copyCache.encoders = append([]string(nil), c.encoders...)
			cached = &copyCache
		}
		s.hwMu.Unlock()
	}

	if cached != nil {
		info.Encoders = cached.encoders
		info.TonemapxAvail = cached.tonemapxAvail
		info.ZscaleAvail = cached.zscaleAvail
		info.VPPQSVAvail = cached.vppQSVAvail
		info.ScaleVAAPIAvail = cached.scaleVAAPIAvail
		info.PadVAAPIAvail = cached.padVAAPIAvail
		info.TonemapVAAPIAvail = cached.tonemapVAAPIAvail
		info.VMAFAvailable = cached.vmafAvail
		info.hasLibx264 = cached.hasLibx264
	} else {
		if ffmpegPath != "" {
			encOut := probeCommandOutput(ctx, ffmpegPath, "-hide_banner", "-encoders")
			for _, enc := range knownEncoders {
				if hasWordMatch(encOut, enc) {
					info.Encoders = append(info.Encoders, enc)
				}
			}
			info.hasLibx264 = hasWordMatch(encOut, "libx264")
			fltOut := probeCommandOutput(ctx, ffmpegPath, "-hide_banner", "-filters")
			info.TonemapxAvail = hasWordMatch(fltOut, "tonemapx")
			info.ZscaleAvail = hasWordMatch(fltOut, "zscale")
			info.VPPQSVAvail = hasWordMatch(fltOut, "vpp_qsv")
			info.ScaleVAAPIAvail = hasWordMatch(fltOut, "scale_vaapi")
			info.PadVAAPIAvail = hasWordMatch(fltOut, "pad_vaapi")
			info.TonemapVAAPIAvail = hasWordMatch(fltOut, "tonemap_vaapi")
			if vmafPath == ffmpegPath {
				info.VMAFAvailable = hasWordMatch(fltOut, "libvmaf")
			}
		}
		if vmafPath != "" && vmafPath != ffmpegPath {
			fltOut := probeCommandOutput(ctx, vmafPath, "-hide_banner", "-filters")
			info.VMAFAvailable = hasWordMatch(fltOut, "libvmaf")
		}

		s.hwMu.Lock()
		s.hwCache = &cachedHardwareProbe{
			ffmpegSig:         ffSig,
			ffprobeSig:        fpSig,
			vmafSig:           vmSig,
			encoders:          append([]string(nil), info.Encoders...),
			tonemapxAvail:     info.TonemapxAvail,
			zscaleAvail:       info.ZscaleAvail,
			vppQSVAvail:       info.VPPQSVAvail,
			scaleVAAPIAvail:   info.ScaleVAAPIAvail,
			padVAAPIAvail:     info.PadVAAPIAvail,
			tonemapVAAPIAvail: info.TonemapVAAPIAvail,
			vmafAvail:         info.VMAFAvailable,
			hasLibx264:        info.hasLibx264,
		}
		s.hwMu.Unlock()
	}

	hasSelectedGPU, selectedVendor := selectedGPUInfo(info.GPUs)

	info.PreferredEncoder = resolveEffectiveEncoder(st.PreferredEncoder, "av1", info.Encoders, hasSelectedGPU, selectedVendor, st.AllowCPUFallback)
	info.Ready = info.FFmpegFound && info.FFprobeFound && len(info.Encoders) > 0
	return info
}

func selectedGPUInfo(gpus []GPUDevice) (bool, string) {
	hasGPU := false
	vendor := ""
	mixed := false
	for _, g := range gpus {
		if !g.Selected {
			continue
		}
		hasGPU = true
		v := strings.ToLower(strings.TrimSpace(g.Vendor))
		if v == "" {
			mixed = true
			continue
		}
		if vendor == "" {
			vendor = v
		} else if vendor != v {
			mixed = true
		}
	}
	if mixed {
		return hasGPU, ""
	}
	return hasGPU, vendor
}

func resolveEffectiveEncoder(preferred, targetCodec string, available []string, hasGPU bool, vendor string, allowCPU bool) string {
	targetCodec = strings.ToLower(strings.TrimSpace(targetCodec))
	if targetCodec != "hevc" && targetCodec != "h264" {
		targetCodec = "av1"
	}
	if preferred != "" && preferred != "auto" {
		if slices.Contains(available, preferred) {
			if !isHardwareEncoder(preferred) || hasGPU || isNVENCEncoder(preferred) {
				return preferred
			}
		}
		if !allowCPU {
			return ""
		}
	}

	var order []string
	if hasGPU {
		switch vendor {
		case "amd":
			order = append(order, targetCodec+"_vaapi", targetCodec+"_vulkan")
		case "nvidia":
			order = append(order, targetCodec+"_nvenc", targetCodec+"_vulkan")
		default:
			order = append(order, targetCodec+"_qsv", targetCodec+"_vaapi", targetCodec+"_vulkan", targetCodec+"_nvenc")
		}
	} else {
		order = append(order, targetCodec+"_nvenc")
	}
	if allowCPU || !hasGPU {
		order = append(order, cpuFallbackEncoderFor(targetCodec))
	}

	for _, cand := range order {
		if slices.Contains(available, cand) {
			return cand
		}
	}
	return ""
}

func isHardwareEncoder(enc string) bool {
	return strings.HasSuffix(enc, "_qsv") ||
		strings.HasSuffix(enc, "_vaapi") ||
		strings.HasSuffix(enc, "_vulkan") ||
		strings.HasSuffix(enc, "_nvenc")
}

func isDRMRenderEncoder(enc string) bool {
	return strings.HasSuffix(enc, "_qsv") || strings.HasSuffix(enc, "_vaapi") || strings.HasSuffix(enc, "_vulkan")
}

func isNVENCEncoder(enc string) bool {
	return strings.HasSuffix(enc, "_nvenc")
}

func cpuFallbackEncoderFor(targetCodec string) string {
	switch targetCodec {
	case "hevc":
		return "libx265"
	case "h264":
		return "libx264"
	default:
		return "libsvtav1"
	}
}
