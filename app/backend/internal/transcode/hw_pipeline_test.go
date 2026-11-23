package transcode

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCanHWDecode(t *testing.T) {
	cases := []struct {
		name  string
		video ProbeStream
		enc   string
		want  bool
	}{
		{"h264_8bit_qsv", ProbeStream{CodecName: "h264", PixFmt: "yuv420p"}, "av1_qsv", true},
		{"h264_yuvj420p_qsv", ProbeStream{CodecName: "h264", PixFmt: "yuvj420p"}, "av1_qsv", true},
		{"h264_nv12_vaapi", ProbeStream{CodecName: "h264", PixFmt: "nv12"}, "av1_vaapi", true},
		{"h264_10bit_rejected", ProbeStream{CodecName: "h264", PixFmt: "yuv420p10le"}, "av1_qsv", false},
		{"h264_422_rejected", ProbeStream{CodecName: "h264", PixFmt: "yuv422p"}, "av1_qsv", false},
		{"hevc_10bit_qsv", ProbeStream{CodecName: "hevc", PixFmt: "yuv420p10le"}, "av1_qsv", true},
		{"vp9_10bit_vaapi", ProbeStream{CodecName: "vp9", PixFmt: "p010le"}, "hevc_vaapi", true},
		{"av1_10bit_nvenc", ProbeStream{CodecName: "av1", PixFmt: "yuv420p10le"}, "av1_nvenc", true},
		{"mpeg2_8bit_qsv", ProbeStream{CodecName: "mpeg2video", PixFmt: "yuv420p"}, "av1_qsv", true},
		{"interlaced_tt_rejected", ProbeStream{CodecName: "h264", PixFmt: "yuv420p", FieldOrder: "tt"}, "av1_qsv", false},
		{"mpeg4_rejected", ProbeStream{CodecName: "mpeg4", PixFmt: "yuv420p"}, "av1_qsv", false},
		{"vulkan_rejected", ProbeStream{CodecName: "h264", PixFmt: "yuv420p"}, "av1_vulkan", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canHWDecode(tc.video, tc.enc); got != tc.want {
				t.Fatalf("canHWDecode(%+v, %q) = %v, want %v", tc.video, tc.enc, got, tc.want)
			}
		})
	}
}

func assertBeforeInput(t *testing.T, cmd []string, flag, val string) {
	t.Helper()
	inputIdx := slices.Index(cmd, "-i")
	if inputIdx < 0 {
		t.Fatalf("missing -i in cmd: %v", cmd)
	}
	pre := cmd[:inputIdx]
	for i := 0; i < len(pre)-1; i++ {
		if pre[i] == flag && (val == "" || pre[i+1] == val) {
			return
		}
	}
	t.Fatalf("expected %s %s before -i in %v", flag, val, pre)
}

func valueAfter(cmd []string, flag string) string {
	for i := 0; i < len(cmd)-1; i++ {
		if cmd[i] == flag {
			return cmd[i+1]
		}
	}
	return ""
}

func TestHWPipelineArgsAndAssembleCmd(t *testing.T) {
	hw := HardwareInfo{
		VPPQSVAvail:       true,
		ScaleVAAPIAvail:   true,
		PadVAAPIAvail:     true,
		TonemapVAAPIAvail: true,
		TonemapxAvail:     true,
	}
	gpu := &GPUDevice{Vendor: "intel", Path: "/dev/dri/renderD129"}

	t.Run("1080p_h264_to_av1_qsv_zero_copy", func(t *testing.T) {
		video := ProbeStream{CodecName: "h264", PixFmt: "yuv420p", Width: 1920, Height: 1080}
		plan, err := ComputeScalePlan(1920, 1080, "1:1", DefaultProfileConfig())
		if err != nil {
			t.Fatal(err)
		}
		pre, encArgs, usedHWD := hwPipelineArgs(hw, video, plan, "av1_qsv", gpu, 20, "", true, false, true)
		if !usedHWD {
			t.Fatal("expected usedHWDecode=true")
		}
		cmd := assembleFFmpegCmd("/in.mkv", []string{"-map", "0:v:0"}, append(pre, encArgs...), []string{"-f", "matroska", "/out.mkv"})
		assertBeforeInput(t, cmd, "-threads", "4")
		assertBeforeInput(t, cmd, "-init_hw_device", "vaapi=va:/dev/dri/renderD129")
		assertBeforeInput(t, cmd, "-init_hw_device", "qsv=qsv@va")
		assertBeforeInput(t, cmd, "-filter_hw_device", "va")
		assertBeforeInput(t, cmd, "-hwaccel", "vaapi")
		assertBeforeInput(t, cmd, "-hwaccel_device", "va")
		assertBeforeInput(t, cmd, "-hwaccel_output_format", "vaapi")
		assertBeforeInput(t, cmd, "-extra_hw_frames", "48")

		inIdx := slices.Index(cmd, "-i")
		if !slices.Contains(cmd[inIdx:], "-noautoscale") {
			t.Fatalf("expected -noautoscale after -i in %v", cmd)
		}
		vf := valueAfter(cmd, "-vf")
		if vf != "scale_vaapi=format=p010,hwmap=derive_device=qsv,format=qsv" {
			t.Fatalf("unexpected -vf: %q", vf)
		}
		if valueAfter(cmd, "-preset") != "medium" || valueAfter(cmd, "-async_depth") != "4" || valueAfter(cmd, "-global_quality") != "20" {
			t.Fatalf("unexpected QSV encoder flags in %v", cmd)
		}
	})

	t.Run("4k_10bit_hevc_to_1080p_av1_qsv_zero_copy", func(t *testing.T) {
		video := ProbeStream{CodecName: "hevc", PixFmt: "yuv420p10le", Width: 3840, Height: 2160}
		plan, err := ComputeScalePlan(3840, 2160, "1:1", DefaultProfileConfig())
		if err != nil {
			t.Fatal(err)
		}
		pre, encArgs, usedHWD := hwPipelineArgs(hw, video, plan, "av1_qsv", gpu, 18, "medium", true, false, true)
		if !usedHWD {
			t.Fatal("expected usedHWDecode=true for 10-bit HEVC")
		}
		cmd := assembleFFmpegCmd("/in.mkv", []string{"-map", "0:v:0"}, append(pre, encArgs...), []string{"-f", "matroska", "/out.mkv"})
		assertBeforeInput(t, cmd, "-hwaccel", "vaapi")
		if vf := valueAfter(cmd, "-vf"); vf != "scale_vaapi=w=1920:h=1080:format=p010,hwmap=derive_device=qsv,format=qsv" {
			t.Fatalf("unexpected 4K->1080p -vf: %q", vf)
		}
		if aspect := valueAfter(cmd, "-aspect"); aspect != "1920:1080" {
			t.Fatalf("unexpected 4K->1080p -aspect: %q", aspect)
		}
	})

	t.Run("vpp_qsv_fallback_when_scale_vaapi_unavailable", func(t *testing.T) {
		hwVPPOnly := HardwareInfo{VPPQSVAvail: true}
		video := ProbeStream{CodecName: "hevc", PixFmt: "yuv420p10le", Width: 3840, Height: 2160}
		plan, err := ComputeScalePlan(3840, 2160, "1:1", DefaultProfileConfig())
		if err != nil {
			t.Fatal(err)
		}
		pre, encArgs, usedHWD := hwPipelineArgs(hwVPPOnly, video, plan, "av1_qsv", gpu, 18, "medium", true, false, true)
		if !usedHWD {
			t.Fatal("expected usedHWDecode=true for vpp_qsv fallback")
		}
		cmd := assembleFFmpegCmd("/in.mkv", []string{"-map", "0:v:0"}, append(pre, encArgs...), []string{"-f", "matroska", "/out.mkv"})
		assertBeforeInput(t, cmd, "-filter_hw_device", "qsv")
		assertBeforeInput(t, cmd, "-hwaccel", "qsv")
		assertBeforeInput(t, cmd, "-hwaccel_device", "qsv")
		assertBeforeInput(t, cmd, "-hwaccel_output_format", "qsv")
		if !slices.Contains(cmd[slices.Index(cmd, "-i"):], "-noautoscale") {
			t.Fatalf("expected -noautoscale on vpp_qsv HW-decode path: %v", cmd)
		}
		if vf := valueAfter(cmd, "-vf"); vf != "vpp_qsv=w=1920:h=1080:format=p010le" {
			t.Fatalf("unexpected vpp_qsv fallback -vf: %q", vf)
		}
	})

	t.Run("10bit_h264_and_vmaf_evalQ_use_sw_decode", func(t *testing.T) {
		video10 := ProbeStream{CodecName: "h264", PixFmt: "yuv420p10le", Width: 1920, Height: 1080}
		plan, _ := ComputeScalePlan(1920, 1080, "1:1", DefaultProfileConfig())
		pre, encArgs, usedHWD := hwPipelineArgs(hw, video10, plan, "av1_qsv", gpu, 21, "", true, false, true)
		if usedHWD {
			t.Fatal("expected usedHWDecode=false for 10-bit H.264")
		}
		cmd := assembleFFmpegCmd("/in.mkv", nil, append(pre, encArgs...), nil)
		inputIdx := slices.Index(cmd, "-i")
		if slices.Contains(cmd[:inputIdx], "-hwaccel") || slices.Contains(cmd[:inputIdx], "-hwaccel_output_format") {
			t.Fatalf("unexpected hwaccel for 10-bit H.264: %v", cmd)
		}
		assertBeforeInput(t, cmd, "-extra_hw_frames", "48")
		if slices.Contains(cmd, "-noautoscale") {
			t.Fatalf("unexpected -noautoscale on SW-decode QSV path: %v", cmd)
		}
		if strings.Contains(valueAfter(cmd, "-vf"), "hwupload") {
			t.Fatalf("unexpected hwupload in SW-decode QSV -vf: %q", valueAfter(cmd, "-vf"))
		}
		if valueAfter(cmd, "-preset") != "medium" || valueAfter(cmd, "-async_depth") != "4" {
			t.Fatalf("expected medium/async_depth=4 on SW-decode QSV path: %v", cmd)
		}

		// evalQ path via gpuEvalArgs
		preEval, evalArgs := gpuEvalArgs(hw, "av1_qsv", gpu, 21, "", true)
		cmdEval := assembleFFmpegCmd("/ref_00.mkv", nil, append(preEval, evalArgs...), nil)
		assertBeforeInput(t, cmdEval, "-extra_hw_frames", "48")
		if slices.Contains(cmdEval, "-hwaccel") || strings.Contains(valueAfter(cmdEval, "-vf"), "hwupload") || valueAfter(cmdEval, "-preset") != "medium" || valueAfter(cmdEval, "-async_depth") != "4" {
			t.Fatalf("unexpected evalQ cmd: %v", cmdEval)
		}
	})

	t.Run("1080p_h264_to_av1_vaapi", func(t *testing.T) {
		video := ProbeStream{CodecName: "h264", PixFmt: "yuv420p", Width: 1920, Height: 1080}
		plan, _ := ComputeScalePlan(1920, 1080, "1:1", DefaultProfileConfig())
		pre, encArgs, usedHWD := hwPipelineArgs(hw, video, plan, "av1_vaapi", gpu, 20, "medium", true, false, true)
		if !usedHWD {
			t.Fatal("expected usedHWDecode=true for av1_vaapi")
		}
		cmd := assembleFFmpegCmd("/in.mkv", nil, append(pre, encArgs...), nil)
		assertBeforeInput(t, cmd, "-hwaccel", "vaapi")
		assertBeforeInput(t, cmd, "-hwaccel_output_format", "vaapi")
		if !slices.Contains(cmd[slices.Index(cmd, "-i"):], "-noautoscale") {
			t.Fatalf("expected -noautoscale on vaapi HW-decode path: %v", cmd)
		}
		if vf := valueAfter(cmd, "-vf"); vf != "scale_vaapi=format=p010" {
			t.Fatalf("unexpected vaapi -vf: %q", vf)
		}
	})

	t.Run("10bit_h264_and_evalQ_vaapi_use_extra_hw_frames_on_hwupload", func(t *testing.T) {
		video10 := ProbeStream{CodecName: "h264", PixFmt: "yuv420p10le", Width: 1920, Height: 1080}
		plan, _ := ComputeScalePlan(1920, 1080, "1:1", DefaultProfileConfig())
		pre, encArgs, usedHWD := hwPipelineArgs(hw, video10, plan, "av1_vaapi", gpu, 21, "", true, false, true)
		if usedHWD {
			t.Fatal("expected usedHWDecode=false for 10-bit H.264")
		}
		cmd := assembleFFmpegCmd("/in.mkv", nil, append(pre, encArgs...), nil)
		if slices.Contains(cmd, "-noautoscale") {
			t.Fatalf("unexpected -noautoscale on SW-decode VAAPI path: %v", cmd)
		}
		if vf := valueAfter(cmd, "-vf"); vf != "format=p010le,hwupload=extra_hw_frames=48" {
			t.Fatalf("unexpected SW-decode VAAPI -vf: %q", vf)
		}
		preEval, evalArgs := gpuEvalArgs(hw, "av1_vaapi", gpu, 21, "", true)
		cmdEval := assembleFFmpegCmd("/ref_00.mkv", nil, append(preEval, evalArgs...), nil)
		if vf := valueAfter(cmdEval, "-vf"); vf != "format=p010le,hwupload=extra_hw_frames=48" {
			t.Fatalf("unexpected evalQ VAAPI -vf: %q", vf)
		}
	})

	t.Run("hardwareVideoArgs_qsv_and_vaapi_extra_hw_frames", func(t *testing.T) {
		gpu6 := &GPUDevice{Vendor: "intel", Path: "/dev/dri/renderD129", MemoryMB: 6144}
		preQ, argsQ := hardwareVideoArgs("av1_qsv", gpu6, 20, "medium", 0, true, "")
		if valueAfter(preQ, "-extra_hw_frames") != "24" {
			t.Fatalf("expected hardwareVideoArgs av1_qsv -extra_hw_frames 24, got %v", preQ)
		}
		if strings.Contains(valueAfter(argsQ, "-vf"), "hwupload") {
			t.Fatalf("unexpected hwupload in hardwareVideoArgs av1_qsv: %v", argsQ)
		}

		_, argsV := hardwareVideoArgs("av1_vaapi", gpu6, 20, "medium", 0, true, "")
		if vf := valueAfter(argsV, "-vf"); vf != "format=p010le,hwupload=extra_hw_frames=24" {
			t.Fatalf("unexpected hardwareVideoArgs av1_vaapi -vf: %q", vf)
		}
	})

	t.Run("hdr10_tonemap_vaapi_to_qsv", func(t *testing.T) {
		video := ProbeStream{
			CodecName:      "hevc",
			PixFmt:         "yuv420p10le",
			Width:          3840,
			Height:         2160,
			ColorPrimaries: "bt2020",
			ColorTransfer:  "smpte2084",
		}
		plan, _ := ComputeScalePlan(3840, 2160, "1:1", DefaultProfileConfig())
		pre, encArgs, usedHWD := hwPipelineArgs(hw, video, plan, "av1_qsv", gpu, 19, "medium", true, true, true)
		if !usedHWD {
			t.Fatal("expected usedHWDecode=true for HDR10 tonemap_vaapi")
		}
		cmd := assembleFFmpegCmd("/in.mkv", nil, append(pre, encArgs...), nil)
		assertBeforeInput(t, cmd, "-hwaccel", "vaapi")
		assertBeforeInput(t, cmd, "-hwaccel_output_format", "vaapi")
		assertBeforeInput(t, cmd, "-filter_hw_device", "va")
		vf := valueAfter(cmd, "-vf")
		if !strings.Contains(vf, "scale_vaapi=w=1920:h=1080,tonemap_vaapi=format=p010:matrix=bt709:primaries=bt709:transfer=bt709:tonemap=hable:desat=0.2,hwmap=derive_device=qsv,format=qsv") {
			t.Fatalf("unexpected tonemap_vaapi -vf: %q", vf)
		}
	})

	t.Run("4_3_pillarbox_pad_vaapi_to_qsv", func(t *testing.T) {
		video := ProbeStream{CodecName: "h264", PixFmt: "yuv420p", Width: 640, Height: 480}
		plan, _ := ComputeScalePlan(640, 480, "1:1", DefaultProfileConfig())
		pre, encArgs, usedHWD := hwPipelineArgs(hw, video, plan, "av1_qsv", gpu, 20, "", true, false, true)
		if !usedHWD {
			t.Fatal("expected usedHWDecode=true for pad_vaapi")
		}
		vf := valueAfter(append(pre, encArgs...), "-vf")
		if !strings.Contains(vf, "scale_vaapi=w=1440:h=1080:format=p010,pad_vaapi=w=1920:h=1080:x=240:y=0:color=black,hwmap=derive_device=qsv,format=qsv") {
			t.Fatalf("unexpected pad_vaapi -vf: %q", vf)
		}
	})
}

func TestPipelineHWDecodeFallbackToSWDecodeOnGPU(t *testing.T) {
	f := newTestFix(t)
	logPath := filepath.Join(t.TempDir(), "ffmpeg.log")
	binDir := t.TempDir()
	ffprobe := filepath.Join(binDir, "ffprobe")
	ffmpeg := filepath.Join(binDir, "ffmpeg")

	ffprobeScript := `#!/usr/bin/env bash
set -euo pipefail
target="${@: -1}"
if [[ "$target" == *"encoded.mkv"* ]]; then
  cat <<'EOF'
{"format":{"filename":"'$target'","duration":"120.0","size":"1200","tags":{"comment":"Created by FileFlows https://fileflows.com"}},"streams":[{"index":0,"codec_type":"video","codec_name":"av1","pix_fmt":"yuv420p10le","width":1920,"height":1080,"sample_aspect_ratio":"1:1","avg_frame_rate":"24/1","disposition":{"attached_pic":0}}]}
EOF
  exit 0
fi
cat <<'EOF'
{"format":{"filename":"'$target'","duration":"120.0","size":"4096","tags":{}},"streams":[{"index":0,"codec_type":"video","codec_name":"h264","pix_fmt":"yuv420p","width":1920,"height":1080,"sample_aspect_ratio":"1:1","avg_frame_rate":"24/1","disposition":{"attached_pic":0}},{"index":1,"codec_type":"audio","codec_name":"aac","channels":2,"bit_rate":"192000","tags":{"language":"fra"}}]}
EOF
`
	ffmpegScript := `#!/usr/bin/env bash
set -euo pipefail
echo "LOG:${*}" >> "` + logPath + `"
if [[ " ${*} " == *" -encoders "* ]]; then
  echo " V..... av1_qsv AV1 QSV"
  echo " V..... libsvtav1 SVT-AV1"
  exit 0
fi
if [[ " ${*} " == *" -filters "* ]]; then
  echo " ... vpp_qsv ..."
  echo " ... scale_vaapi ..."
  echo " ... pad_vaapi ..."
  echo " ... tonemap_vaapi ..."
  echo " ... libvmaf ..."
  exit 0
fi
if [[ " ${*} " == *"libvmaf"* ]]; then
  echo "VMAF score: 94.0" >&2
  exit 0
fi
if [[ " ${*} " == *" -hwaccel vaapi "* ]]; then
  echo "simulated hwaccel vaapi decode failure" >&2
  exit 1
fi
out="${@: -1}"
mkdir -p "$(dirname "$out")"
if [[ "$out" == *"encoded.mkv"* ]]; then
  printf 'progress=end\nout_time_us=120000000\nfps=280.0\nspeed=11.5x\n'
fi
dd if=/dev/zero bs=1024 count=1 status=none > "$out"
`
	if err := os.WriteFile(ffprobe, []byte(ffprobeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ffmpeg, []byte(ffmpegScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOLYARRS_FFPROBE", ffprobe)
	t.Setenv("HOLYARRS_FFMPEG", ffmpeg)
	t.Setenv("HOLYARRS_VMAF_FFMPEG", ffmpeg)

	makeFakeIntelArcNode(t, f.svc)

	ctx := context.Background()
	hw := f.svc.ProbeHardware(ctx)
	if !hw.VPPQSVAvail || !hw.ScaleVAAPIAvail || !hw.PadVAAPIAvail || !hw.TonemapVAAPIAvail {
		t.Fatalf("expected all VPP/VAAPI filter capabilities detected, got %+v", hw)
	}

	if err := f.q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer f.q.Stop()

	if err := f.svc.QueueFile(ctx, f.movieFile, false); err != nil {
		t.Fatal(err)
	}
	waitJobDone(t, f.q, f.st)

	rec, found, err := getItemByFileID(ctx, f.st.Read(), f.movieFile)
	if err != nil || !found {
		t.Fatalf("expected item record, found=%v err=%v", found, err)
	}
	if rec.State != "done" || !strings.HasSuffix(rec.GPUDevice, "renderD129") {
		t.Fatalf("expected SW-decode retry to stay on GPU av1_qsv, got %+v", rec)
	}

	rawLog, _ := os.ReadFile(logPath)
	logStr := string(rawLog)
	if !strings.Contains(logStr, "-hwaccel vaapi") || !strings.Contains(logStr, "scale_vaapi=format=p010,hwmap=derive_device=qsv,format=qsv") {
		t.Fatalf("expected initial full-GPU VAAPI->QSV attempt in log:\n%s", logStr)
	}
	if strings.Contains(logStr, "libsvtav1") {
		t.Fatalf("did not expect CPU libsvtav1 fallback when SW-decode QSV retry succeeded:\n%s", logStr)
	}
}

func TestQSVPipelineTuning(t *testing.T) {
	cases := []struct {
		name       string
		vramMB     int
		height     int
		wantFrames int
		wantAsync  int
	}{
		{"6gb_4k", 6144, 2160, 16, 4},
		{"9gb_4k", 9216, 2160, 24, 4},
		{"12gb_4k", 12288, 2160, 40, 4},
		{"16gb_4k", 16384, 2160, 40, 4},
		{"unknown_4k", 0, 2160, 40, 4},
		{"6gb_1080p", 6144, 1080, 24, 4},
		{"9gb_1080p", 9216, 1080, 32, 4},
		{"12gb_1080p", 12288, 1080, 48, 4},
		{"16gb_1080p", 16384, 1080, 48, 4},
		{"unknown_1080p", 0, 1080, 48, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frames, async := qsvPipelineTuning(tc.vramMB, tc.height)
			if frames != tc.wantFrames || async != tc.wantAsync {
				t.Fatalf("qsvPipelineTuning(%d, %d) = (%d, %d), want (%d, %d)",
					tc.vramMB, tc.height, frames, async, tc.wantFrames, tc.wantAsync)
			}
		})
	}

	hw := HardwareInfo{VPPQSVAvail: true}
	gpu6 := &GPUDevice{Vendor: "intel", Path: "/dev/dri/renderD129", MemoryMB: 6144}
	video4k := ProbeStream{CodecName: "hevc", PixFmt: "yuv420p10le", Width: 3840, Height: 2160}
	plan4k, _ := ComputeScalePlan(3840, 2160, "1:1", DefaultProfileConfig())
	pre, encArgs, usedHWD := hwPipelineArgs(hw, video4k, plan4k, "av1_qsv", gpu6, 20, "", true, false, true)
	if !usedHWD {
		t.Fatal("expected usedHWDecode=true")
	}
	all := append(pre, encArgs...)
	if valueAfter(all, "-extra_hw_frames") != "16" || valueAfter(all, "-async_depth") != "4" {
		t.Fatalf("expected 6GB 4K tuning extra_hw_frames=16 async_depth=4, got %v", all)
	}
}
