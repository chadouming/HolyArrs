package transcode

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSelectSampleWindows(t *testing.T) {
	w := SelectSampleWindows(3600, 4, 20)
	if len(w) != 4 {
		t.Fatalf("expected 4 windows, got %d", len(w))
	}
	if w[0].Start != 360.0 || w[0].Duration != 20.0 {
		t.Fatalf("unexpected first window: %+v", w[0])
	}
	if w[3].Start <= w[2].Start || w[3].Start >= 3600*0.85 {
		t.Fatalf("unexpected last window: %+v", w[3])
	}

	short := SelectSampleWindows(15, 4, 20)
	if len(short) != 1 {
		t.Fatalf("expected 1 window for short clip, got %+v", short)
	}
}

func TestBinarySearchQuality(t *testing.T) {
	res, err := BinarySearchQuality(func(q int) (float64, int64, error) {
		switch {
		case q <= 18:
			return 96.0, 20_000_000, nil
		case q <= 21:
			return 93.5, 14_000_000, nil
		default:
			return 89.0, 9_000_000, nil
		}
	}, 15, 25, 93.0, 5, 80, 1000, 500_000_000, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.SelectedQ != 21 || res.MeanVMAF != 93.5 || res.CopyVideoFallback {
		t.Fatalf("expected q=21 with vmaf=93.5, got %+v", res)
	}

	copyRes, err := BinarySearchQuality(func(q int) (float64, int64, error) {
		return 95.0, 50_000_000, nil
	}, 15, 25, 93.0, 5, 80, 1000, 300_000_000, true)
	if err != nil {
		t.Fatal(err)
	}
	if !copyRes.CopyVideoFallback {
		t.Fatalf("expected copy_video_fallback when projected > input, got %+v", copyRes)
	}
}

func TestParseEDLKeepSegments(t *testing.T) {
	dir := t.TempDir()
	edl := filepath.Join(dir, "show.edl")
	content := "# comskip\n10.0 30.0 0\n25.0 40.0 0\n100.0 120.0 1\n150.0 170.0 0\n"
	if err := os.WriteFile(edl, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	keep := ParseEDLKeepSegments(edl, 200.0)
	if len(keep) != 3 {
		t.Fatalf("expected 3 keep segments, got %+v", keep)
	}
	if keep[0] != (KeepSegment{0, 10}) || keep[1] != (KeepSegment{40, 150}) || keep[2] != (KeepSegment{170, 200}) {
		t.Fatalf("unexpected keep segments: %+v", keep)
	}
}

func TestDiscoverGPUsAndAllocator(t *testing.T) {
	root := t.TempDir()
	devDRI := filepath.Join(root, "dev", "dri")
	sysfs := filepath.Join(root, "sys", "class", "drm")
	pciRoot := filepath.Join(root, "pci")

	makeNode := func(name, pciSlot, vendor, devID string) {
		_ = os.MkdirAll(devDRI, 0o755)
		_ = os.WriteFile(filepath.Join(devDRI, name), []byte{}, 0o644)
		slotDir := filepath.Join(pciRoot, pciSlot)
		_ = os.MkdirAll(slotDir, 0o755)
		_ = os.WriteFile(filepath.Join(slotDir, "vendor"), []byte(vendor+"\n"), 0o644)
		_ = os.WriteFile(filepath.Join(slotDir, "device"), []byte(devID+"\n"), 0o644)
		nodeSys := filepath.Join(sysfs, name)
		_ = os.MkdirAll(nodeSys, 0o755)
		_ = os.Symlink(slotDir, filepath.Join(nodeSys, "device"))
	}

	makeNode("renderD128", "0000:00:02.0", "0x8086", "0xa780")
	makeNode("renderD129", "0000:03:00.0", "0x8086", "0xe20b")
	makeNode("renderD130", "0000:04:00.0", "0x8086", "0xe20b")

	auto := DiscoverGPUs(devDRI, sysfs, nil, "auto", nil)
	if len(auto) != 3 {
		t.Fatalf("expected ALL 3 render nodes returned, got %d", len(auto))
	}
	if auto[0].Selected || !auto[1].Selected || !auto[2].Selected {
		t.Fatalf("expected discrete Arc GPUs selected and iGPU unselected under auto: %+v", auto)
	}

	manual := DiscoverGPUs(devDRI, sysfs, []string{filepath.Join(devDRI, "renderD128")}, "auto", nil)
	if !manual[0].Selected || manual[1].Selected || manual[2].Selected {
		t.Fatalf("expected explicit selected_gpus override: %+v", manual)
	}

	var alloc gpuAllocator
	g1, rel1, ok := alloc.Acquire(auto)
	if !ok || !strings.HasSuffix(g1.Path, "renderD129") {
		t.Fatalf("expected first GPU renderD129, got %+v", g1)
	}
	g2, rel2, ok := alloc.Acquire(auto)
	if !ok || !strings.HasSuffix(g2.Path, "renderD130") {
		t.Fatalf("expected least-busy GPU renderD130, got %+v", g2)
	}
	rel1()
	rel2()
}

func TestBuildChildEnvIntelLibva(t *testing.T) {
	t.Setenv("LIBVA_DRIVER_NAME", "")
	env := buildChildEnv(&GPUDevice{Vendor: "intel", Path: "/dev/dri/renderD129"})
	found := false
	for _, e := range env {
		if e == "LIBVA_DRIVER_NAME=iHD" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected LIBVA_DRIVER_NAME=iHD injected for Intel GPU when unset")
	}

	t.Setenv("LIBVA_DRIVER_NAME", "custom")
	if env2 := buildChildEnv(&GPUDevice{Vendor: "intel"}); env2 != nil {
		t.Fatalf("expected nil override when LIBVA_DRIVER_NAME already set, got %v", env2)
	}
}

func TestVendorAwareEncoderSelection(t *testing.T) {
	all := []string{"av1_qsv", "av1_vaapi", "av1_vulkan", "av1_nvenc", "libsvtav1"}
	if got := resolveEffectiveEncoder("auto", "av1", all, true, "amd", true); got != "av1_vaapi" {
		t.Fatalf("AMD auto expected av1_vaapi, got %q", got)
	}
	if got := resolveEffectiveEncoder("auto", "av1", []string{"av1_qsv", "av1_vulkan", "libsvtav1"}, true, "amd", true); got != "av1_vulkan" {
		t.Fatalf("AMD without vaapi should pick av1_vulkan and skip av1_qsv, got %q", got)
	}
	if got := resolveEffectiveEncoder("auto", "av1", all, true, "nvidia", true); got != "av1_nvenc" {
		t.Fatalf("NVIDIA auto expected av1_nvenc, got %q", got)
	}
	if got := resolveEffectiveEncoder("auto", "av1", all, true, "intel", true); got != "av1_qsv" {
		t.Fatalf("Intel auto expected av1_qsv, got %q", got)
	}
	if got := resolveEffectiveEncoder("auto", "av1", all, true, "", true); got != "av1_qsv" {
		t.Fatalf("Mixed/unknown auto expected av1_qsv, got %q", got)
	}
}

func TestHWPipelineNoBrokenHWUploadAndNoSetsar(t *testing.T) {
	hw := HardwareInfo{
		VPPQSVAvail:     true,
		ScaleVAAPIAvail: true,
	}
	gpu := &GPUDevice{Vendor: "intel", Path: "/dev/dri/renderD129"}

	// QSV SW-decode path and gpuEvalArgs must NOT emit hwupload and must use formatQSVHWDeviceSpec.
	video10 := ProbeStream{CodecName: "h264", PixFmt: "yuv420p10le", Width: 1920, Height: 1080}
	plan1080, _ := ComputeScalePlan(1920, 1080, "1:1", DefaultProfileConfig())
	preSW, encSW, usedHWD := hwPipelineArgs(hw, video10, plan1080, "av1_qsv", gpu, 20, "medium", true, false, true)
	if usedHWD {
		t.Fatal("expected usedHWDecode=false for 10-bit H.264")
	}
	joinedSW := strings.Join(append(preSW, encSW...), " ")
	if strings.Contains(joinedSW, "hwupload") || !strings.Contains(joinedSW, "child_device=/dev/dri/renderD129,child_device_type=vaapi") {
		t.Fatalf("unexpected QSV SW-decode args: %s", joinedSW)
	}
	preEval, encEval := gpuEvalArgs(hw, "av1_qsv", gpu, 20, "medium", true)
	joinedEval := strings.Join(append(preEval, encEval...), " ")
	if strings.Contains(joinedEval, "hwupload") || !strings.Contains(joinedEval, "format=p010le") {
		t.Fatalf("unexpected gpuEvalArgs: %s", joinedEval)
	}

	// Scaled 4K->1080p QSV and VAAPI HW pipelines must NOT append ",setsar=1" and MUST emit "-aspect 1920:1080".
	videoHEVC := ProbeStream{CodecName: "hevc", PixFmt: "yuv420p10le", Width: 3840, Height: 2160}
	plan4k, _ := ComputeScalePlan(3840, 2160, "1:1", DefaultProfileConfig())
	preQ, encQ, usedQ := hwPipelineArgs(hw, videoHEVC, plan4k, "av1_qsv", gpu, 18, "medium", true, false, true)
	if !usedQ {
		t.Fatal("expected QSV HW decode for 4K HEVC")
	}
	cmdQ := assembleFFmpegCmd("/in.mkv", nil, append(preQ, encQ...), nil)
	if vf := valueAfter(cmdQ, "-vf"); vf != "scale_vaapi=w=1920:h=1080:format=p010,hwmap=derive_device=qsv,format=qsv" {
		t.Fatalf("unexpected QSV scaled -vf: %q", vf)
	}
	if asp := valueAfter(cmdQ, "-aspect"); asp != "1920:1080" {
		t.Fatalf("expected QSV -aspect 1920:1080, got %q", asp)
	}

	preV, encV, usedV := hwPipelineArgs(hw, videoHEVC, plan4k, "av1_vaapi", gpu, 18, "medium", true, false, true)
	if !usedV {
		t.Fatal("expected VAAPI HW decode for 4K HEVC")
	}
	cmdV := assembleFFmpegCmd("/in.mkv", nil, append(preV, encV...), nil)
	if vf := valueAfter(cmdV, "-vf"); vf != "scale_vaapi=w=1920:h=1080:format=p010" {
		t.Fatalf("unexpected VAAPI scaled -vf: %q", vf)
	}
	if asp := valueAfter(cmdV, "-aspect"); asp != "1920:1080" {
		t.Fatalf("expected VAAPI -aspect 1920:1080, got %q", asp)
	}
}

func TestFFmpegThreadBudgetAndCmdAssembly(t *testing.T) {
	if d, e := ffmpegThreadsFor(16, 2); d != 4 || e != 6 {
		t.Fatalf("16 cores / 2 jobs: got (%d, %d), want (4, 6)", d, e)
	}
	if d, e := ffmpegThreadsFor(8, 2); d != 3 || e != 3 {
		t.Fatalf("8 cores / 2 jobs: got (%d, %d), want (3, 3)", d, e)
	}
	if d, e := ffmpegThreadsFor(4, 2); d != 2 || e != 2 {
		t.Fatalf("4 cores / 2 jobs: got (%d, %d), want (2, 2)", d, e)
	}

	cmd := assembleFFmpegCmdWithThreads("/in.mkv", []string{"-map", "0:v:0"}, []string{"-c:v", "libsvtav1"}, []string{"-f", "matroska", "/out.mkv"}, 3, 5)
	inIdx := slices.Index(cmd, "-i")
	if inIdx < 0 {
		t.Fatalf("missing -i in %v", cmd)
	}
	if valueAfter(cmd[:inIdx], "-threads") != "3" || valueAfter(cmd[:inIdx], "-filter_threads") != "3" {
		t.Fatalf("expected -filter_threads 3 and -threads 3 before -i, got %v", cmd[:inIdx])
	}
	post := cmd[inIdx:]
	if slices.Contains(post, "-filter_threads") || valueAfter(post, "-threads") != "5" {
		t.Fatalf("expected no -filter_threads and -threads 5 after -i, got %v", post)
	}

	plan, _ := ComputeScalePlan(1920, 1080, "1:1", DefaultProfileConfig())
	svtArgs := buildCPUArgs("libsvtav1", DefaultProfileConfig(), DefaultSettings(), plan, false, false, nil, 5)
	if !strings.Contains(strings.Join(svtArgs, " "), "mbr=10000:lp=5") {
		t.Fatalf("expected libsvtav1 lp=5 param, got %v", svtArgs)
	}
	x265Args := buildCPUArgs("libx265", DefaultProfileConfig(), DefaultSettings(), plan, false, false, nil, 5)
	if !strings.Contains(strings.Join(x265Args, " "), "pools=5") {
		t.Fatalf("expected libx265 pools=5 param, got %v", x265Args)
	}
}

func TestEnrichGPUsWithVRAMAndAcquireTieBreak(t *testing.T) {
	gpus := []GPUDevice{
		{Path: "/dev/dri/renderD128", Vendor: "intel", Discrete: false, Selected: false},
		{Path: "/dev/dri/renderD129", Vendor: "intel", Discrete: true, Selected: true},
		{Path: "/dev/dri/renderD130", Vendor: "intel", Discrete: true, Selected: true},
	}
	enriched := enrichGPUsWithVRAM(gpus, []GPUVRAMInfo{
		{Index: 1, MemoryMB: 12288, FreeMB: 9000},
		{Index: 0, MemoryMB: 6144, FreeMB: 2000},
	})
	if enriched[0].MemoryMB != 0 {
		t.Fatalf("expected iGPU to remain unenriched when discrete GPUs need VRAM, got %+v", enriched[0])
	}
	if enriched[1].MemoryMB != 6144 || enriched[1].FreeMB != 2000 {
		t.Fatalf("expected renderD129 to receive index 0 VRAM (6144/2000), got %+v", enriched[1])
	}
	if enriched[2].MemoryMB != 12288 || enriched[2].FreeMB != 9000 {
		t.Fatalf("expected renderD130 to receive index 1 VRAM (12288/9000), got %+v", enriched[2])
	}

	var alloc gpuAllocator
	first, rel1, ok := alloc.Acquire(enriched)
	if !ok || first.Path != "/dev/dri/renderD130" {
		t.Fatalf("expected tie-break to prefer higher FreeMB renderD130, got %+v", first)
	}
	second, rel2, ok := alloc.Acquire(enriched)
	if !ok || second.Path != "/dev/dri/renderD129" {
		t.Fatalf("expected least-busy renderD129 next, got %+v", second)
	}
	rel1()
	rel2()

	// Fallback to MemoryMB when FreeMB is zero.
	memOnly := []GPUDevice{
		{Path: "/dev/dri/renderD129", Selected: true, MemoryMB: 6144, FreeMB: 0},
		{Path: "/dev/dri/renderD130", Selected: true, MemoryMB: 16384, FreeMB: 0},
	}
	var alloc2 gpuAllocator
	chosen, rel, ok := alloc2.Acquire(memOnly)
	if !ok || chosen.Path != "/dev/dri/renderD130" {
		t.Fatalf("expected MemoryMB tie-break to prefer renderD130, got %+v", chosen)
	}
	rel()
}

func TestRunGPUVMAFSearchPreInputFilterThreads(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "ffmpeg.log")
	ffmpeg := filepath.Join(dir, "ffmpeg")
	script := `#!/usr/bin/env bash
set -euo pipefail
echo "LOG:${*}" >> "` + logPath + `"
if [[ " ${*} " == *"libvmaf"* ]]; then
  echo "VMAF score: 94.5" >&2
  exit 0
fi
out="${@: -1}"
mkdir -p "$(dirname "$out")"
dd if=/dev/zero bs=256 count=1 status=none > "$out"
`
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	hw := HardwareInfo{
		FFmpegFound:    true,
		FFmpegPath:     ffmpeg,
		VMAFFFmpegPath: ffmpeg,
		VMAFAvailable:  true,
	}
	cfg := DefaultProfileConfig()
	cfg.VMAFSamples = 1
	cfg.MaxIterations = 1
	plan, _ := ComputeScalePlan(1920, 1080, "1:1", cfg)
	gpu := &GPUDevice{Vendor: "intel", Path: "/dev/dri/renderD129", MemoryMB: 12288}
	workDir := filepath.Join(dir, "work")
	_ = os.MkdirAll(workDir, 0o755)

	res, err := RunGPUVMAFSearch(
		context.Background(), hw, cfg, "/in.mkv", 0,
		plan, false, nil, "av1_qsv", gpu,
		24.0, 120.0, 10_000_000, workDir, 2,
	)
	if err != nil {
		t.Fatalf("RunGPUVMAFSearch failed: %v", err)
	}
	if res.Iterations == 0 {
		t.Fatalf("expected at least 1 VMAF iteration, got %+v", res)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 ffmpeg calls (ref, enc, vmaf), got:\n%s", string(raw))
	}

	assertFlagBeforeFirstInput := func(line, flag string) {
		t.Helper()
		toks := strings.Fields(strings.TrimPrefix(line, "LOG:"))
		inIdx := slices.Index(toks, "-i")
		if inIdx < 0 {
			t.Fatalf("missing -i in %q", line)
		}
		if !slices.Contains(toks[:inIdx], flag) {
			t.Fatalf("expected %s before first -i in %q", flag, line)
		}
		if slices.Contains(toks[inIdx:], flag) {
			t.Fatalf("unexpected %s after first -i in %q", flag, line)
		}
	}

	assertFlagBeforeFirstInput(lines[0], "-filter_threads")
	assertFlagBeforeFirstInput(lines[1], "-filter_threads")
	assertFlagBeforeFirstInput(lines[2], "-filter_complex_threads")
}
