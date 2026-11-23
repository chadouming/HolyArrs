package transcode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediaservers"
)

type spyPublisher struct {
	mu     sync.Mutex
	topics []string
}

func (p *spyPublisher) Publish(topic string, _ any) uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.topics = append(p.topics, topic)
	return uint64(len(p.topics))
}

func (p *spyPublisher) has(topic string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, t := range p.topics {
		if t == topic {
			return true
		}
	}
	return false
}

func installFakeBinaries(t *testing.T, logPath, mode string) (string, string) {
	t.Helper()
	binDir := t.TempDir()
	ffprobe := filepath.Join(binDir, "ffprobe")
	ffmpeg := filepath.Join(binDir, "ffmpeg")

	ffprobeScript := `#!/usr/bin/env bash
set -euo pipefail
target="${@: -1}"
if [[ "$target" == *"encoded.mkv"* ]] || [[ "$target" == *".mkv"* && "$target" != *"AnimeShow"* ]]; then
  cat <<'EOF'
{"format":{"filename":"'$target'","duration":"120.0","size":"1200","tags":{"comment":"Created by FileFlows https://fileflows.com"}},"streams":[{"index":0,"codec_type":"video","codec_name":"av1","width":1920,"height":1080,"sample_aspect_ratio":"1:1","avg_frame_rate":"24/1","disposition":{"attached_pic":0}},{"index":1,"codec_type":"audio","codec_name":"aac","channels":2,"bit_rate":"192000","tags":{"language":"fra"}}]}
EOF
  exit 0
fi
cat <<'EOF'
{"format":{"filename":"'$target'","duration":"120.0","size":"4096","tags":{}},"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"sample_aspect_ratio":"1:1","avg_frame_rate":"24/1","disposition":{"attached_pic":0}},{"index":1,"codec_type":"audio","codec_name":"aac","channels":2,"bit_rate":"192000","tags":{"language":"fra","title":"VFQ"}},{"index":2,"codec_type":"subtitle","codec_name":"subrip","tags":{"language":"eng"}}]}
EOF
`

	ffmpegScript := `#!/usr/bin/env bash
set -euo pipefail
echo "LOG:${*}" >> "` + logPath + `"
if [[ " ${*} " == *" -encoders "* ]]; then
  echo " V..... av1_qsv AV1 QSV"
  echo " V..... av1_vaapi AV1 VAAPI"
  echo " V..... libsvtav1 SVT-AV1"
  echo " V..... libx264 H264"
  exit 0
fi
if [[ " ${*} " == *" -filters "* ]]; then
  echo " ... tonemapx ..."
  echo " ... libvmaf ..."
  exit 0
fi
if [[ " ${*} " == *"libvmaf"* ]]; then
  if [[ "` + mode + `" == "vmaf_fail" ]]; then
    echo "libvmaf runtime error" >&2
    exit 1
  fi
  echo "VMAF score: 94.500000" >&2
  exit 0
fi
out="${@: -1}"
if [[ "` + mode + `" == "gpu_fail" ]] && [[ " ${*} " == *"_qsv"* || " ${*} " == *"_vaapi"* ]]; then
  echo "simulated GPU failure" >&2
  exit 1
fi
if [[ "` + mode + `" == "qsv_fail_vaapi_ok" ]] && [[ " ${*} " == *"_qsv"* ]]; then
  echo "simulated QSV failure" >&2
  exit 1
fi
mkdir -p "$(dirname "$out")"
if [[ "$out" == *"encoded.mkv"* ]]; then
  printf 'progress=continue\nout_time_us=60000000\nfps=95.5\nspeed=3.98x\n'
  printf 'progress=end\nout_time_us=120000000\nfps=100.0\nspeed=4.10x\n'
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
	return ffmpeg, ffprobe
}

func makeFakeIntelArcNode(t *testing.T, svc *Service) {
	t.Helper()
	root := t.TempDir()
	devDRI := filepath.Join(root, "dev", "dri")
	sysfs := filepath.Join(root, "sys", "class", "drm")
	slot := filepath.Join(root, "pci", "0000:03:00.0")
	_ = os.MkdirAll(devDRI, 0o755)
	_ = os.MkdirAll(slot, 0o755)
	_ = os.MkdirAll(filepath.Join(sysfs, "renderD129"), 0o755)
	_ = os.WriteFile(filepath.Join(devDRI, "renderD129"), []byte{}, 0o644)
	_ = os.WriteFile(filepath.Join(slot, "vendor"), []byte("0x8086\n"), 0o644)
	_ = os.WriteFile(filepath.Join(slot, "device"), []byte("0xe20b\n"), 0o644)
	_ = os.Symlink(slot, filepath.Join(sysfs, "renderD129", "device"))
	svc.DevDRI = devDRI
	svc.SysfsDRM = sysfs
}

func waitJobDone(t *testing.T, q *jobs.Queue, st *database.Store) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		active, err := jobs.ListActive(context.Background(), st.Read(), []string{KindFile}, 10)
		if err == nil && len(active) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out waiting for transcode job to complete")
}

func TestEndToEndPipeline_HoldAndEventAndEDL(t *testing.T) {
	f := newTestFix(t)
	logPath := filepath.Join(t.TempDir(), "ffmpeg.log")
	installFakeBinaries(t, logPath, "ok")
	makeFakeIntelArcNode(t, f.svc)

	pub := &spyPublisher{}
	f.svc.Live = pub
	yielded := false
	transcodedFile := int64(0)
	f.svc.YieldGPU = func(_ context.Context) { yielded = true }
	f.svc.OnTranscoded = func(_ context.Context, fid int64) { transcodedFile = fid }

	// Enable keeping originals in holding and write an EDL cutlist next to the movie.
	st := ReadSettings(context.Background(), f.st.Read())
	st.KeepOriginalHolding = true
	if err := f.svc.UpdateSettings(context.Background(), st, database.Actor{Kind: "user", ID: 1}); err != nil {
		t.Fatal(err)
	}
	edlPath := filepath.Join(f.libMovie, "Movie (2024)", "Movie (2024).edl")
	_ = os.WriteFile(edlPath, []byte("5.0 15.0 0\n"), 0o644)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := f.q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer f.q.Stop()

	if err := f.svc.QueueFile(ctx, f.movieFile, false); err != nil {
		t.Fatal(err)
	}
	waitJobDone(t, f.q, f.st)

	if !yielded || transcodedFile != f.movieFile {
		t.Fatalf("expected YieldGPU and OnTranscoded callbacks, got yielded=%v fid=%d", yielded, transcodedFile)
	}

	// Verify file row kept same ID, updated path to .mkv, and original moved to holding.
	var relPath string
	var qualJSON string
	if err := f.st.Read().QueryRow("SELECT relative_path, quality FROM files WHERE id = ?", f.movieFile).Scan(&relPath, &qualJSON); err != nil {
		t.Fatal(err)
	}
	if relPath != "Movie (2024)/Movie (2024).mkv" {
		t.Fatalf("expected updated .mkv relative_path, got %q", relPath)
	}
	if !strings.Contains(qualJSON, "AV1") {
		t.Fatalf("expected AV1 quality update, got %s", qualJSON)
	}
	if _, err := os.Stat(filepath.Join(f.libMovie, filepath.FromSlash(relPath))); err != nil {
		t.Fatalf("expected promoted .mkv on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.libMovie, filepath.FromSlash(f.movieRel))); !os.IsNotExist(err) {
		t.Fatalf("expected original .mp4 moved out of library path")
	}

	// Verify transcode_items row recorded profile, VMAF, EDL, and bytes.
	rec, found, err := getItemByFileID(ctx, f.st.Read(), f.movieFile)
	if err != nil || !found {
		t.Fatalf("expected transcode_items row, found=%v err=%v", found, err)
	}
	if rec.State != "done" || !rec.EDLApplied || rec.ProfileID == nil || rec.MeanVMAF == nil || rec.SelectedQ == nil {
		t.Fatalf("unexpected transcode_items record: %+v", rec)
	}

	// Verify transcode_done event emitted with paths, published to Live, and recognized as Modified by mediaservers.ChangeOf.
	evs, _, err := f.st.Events(ctx, database.EventFilter{Kinds: []string{EventTranscoded}, Limit: 10})
	if err != nil || len(evs) != 1 {
		t.Fatalf("expected 1 transcode_done event, got %d (%v)", len(evs), err)
	}
	if mediaservers.ChangeOf(evs[0]) != mediaservers.Modified {
		t.Fatalf("expected mediaservers.ChangeOf == Modified, got %q", mediaservers.ChangeOf(evs[0]))
	}
	if ps, _ := evs[0].Data["paths"].([]any); len(ps) != 1 || ps[0] != filepath.Join(f.libMovie, filepath.FromSlash(relPath)) {
		t.Fatalf("expected paths in event data, got %#v", evs[0].Data["paths"])
	}
	if !pub.has("event") {
		t.Fatalf("expected Live.Publish(\"event\", ev) to be called")
	}

	// Verify ffmpeg log saw EDL cut, VMAF search, and HolyArrs comment tag.
	logData, _ := os.ReadFile(logPath)
	if !strings.Contains(string(logData), "edl_keep_") || !strings.Contains(string(logData), "comment="+DefaultWriteTag) {
		t.Fatalf("expected EDL cut and HolyArrs write_tag in ffmpeg log:\n%s", string(logData))
	}
	_ = json.Valid(nil)
}

func TestEndToEndPipeline_VMAFFailureKeepsGPU(t *testing.T) {
	f := newTestFix(t)
	logPath := filepath.Join(t.TempDir(), "ffmpeg.log")
	installFakeBinaries(t, logPath, "vmaf_fail")
	makeFakeIntelArcNode(t, f.svc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := f.q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer f.q.Stop()

	if err := f.svc.QueueFile(ctx, f.movieFile, false); err != nil {
		t.Fatal(err)
	}
	waitJobDone(t, f.q, f.st)

	rec, found, _ := getItemByFileID(ctx, f.st.Read(), f.movieFile)
	if !found || rec.State != "done" || !strings.HasSuffix(rec.GPUDevice, "renderD129") {
		t.Fatalf("expected libvmaf failure to stay on GPU at midpoint q, got %+v", rec)
	}
}

func TestEndToEndPipeline_QSVFailRetriesVAAPIOnGPU(t *testing.T) {
	f := newTestFix(t)
	logPath := filepath.Join(t.TempDir(), "ffmpeg.log")
	installFakeBinaries(t, logPath, "qsv_fail_vaapi_ok")
	makeFakeIntelArcNode(t, f.svc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := f.q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer f.q.Stop()

	if err := f.svc.QueueFile(ctx, f.movieFile, false); err != nil {
		t.Fatal(err)
	}
	waitJobDone(t, f.q, f.st)

	rec, found, _ := getItemByFileID(ctx, f.st.Read(), f.movieFile)
	if !found || rec.State != "done" || !strings.HasSuffix(rec.GPUDevice, "renderD129") {
		t.Fatalf("expected VAAPI retry completion on GPU renderD129, got %+v", rec)
	}
	logData, _ := os.ReadFile(logPath)
	logStr := string(logData)
	if !strings.Contains(logStr, "av1_qsv") || !strings.Contains(logStr, "av1_vaapi") {
		t.Fatalf("expected both av1_qsv attempt and av1_vaapi retry in log:\n%s", logStr)
	}
	if strings.Contains(logStr, "libsvtav1") {
		t.Fatalf("did not expect CPU fallback when av1_vaapi retry succeeded:\n%s", logStr)
	}
}

func TestEndToEndPipeline_CPUFallbackWhenGPUFails(t *testing.T) {
	f := newTestFix(t)
	logPath := filepath.Join(t.TempDir(), "ffmpeg.log")
	installFakeBinaries(t, logPath, "gpu_fail")
	makeFakeIntelArcNode(t, f.svc)

	var notesMu sync.Mutex
	var notes []string
	q := jobs.New(f.st, jobs.Options{
		Workers:      1,
		PollInterval: 50 * time.Millisecond,
		OnChange: func(j jobs.Job) {
			if j.ProgressNote != nil {
				notesMu.Lock()
				notes = append(notes, *j.ProgressNote)
				notesMu.Unlock()
			}
		},
	})
	f.svc.Register(q)
	f.svc.Enqueue = func(ctx context.Context, kind string, subj *domain.Subject, params map[string]any, key string) error {
		_, _, err := q.Enqueue(ctx, kind, subj, params, key, domain.System)
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer q.Stop()

	if err := f.svc.QueueFile(ctx, f.movieFile, false); err != nil {
		t.Fatal(err)
	}
	waitJobDone(t, q, f.st)

	rec, found, _ := getItemByFileID(ctx, f.st.Read(), f.movieFile)
	if !found || rec.State != "done" || rec.GPUDevice != "" {
		t.Fatalf("expected CPU fallback completion with empty GPUDevice, got %+v", rec)
	}
	notesMu.Lock()
	sawFallback := false
	revertedToEncoding := false
	for _, n := range notes {
		if n == "cpu_fallback" {
			sawFallback = true
		} else if sawFallback && n == "encoding" {
			revertedToEncoding = true
		}
	}
	notesCopy := append([]string(nil), notes...)
	notesMu.Unlock()
	if !sawFallback || revertedToEncoding {
		t.Fatalf("expected cpu_fallback progress phase without reverting to encoding, got notes=%v", notesCopy)
	}
	logData, _ := os.ReadFile(logPath)
	logStr := string(logData)
	if !strings.Contains(logStr, "libsvtav1") || !strings.Contains(logStr, "-filter_threads") || !strings.Contains(logStr, ":lp=") {
		t.Fatalf("expected bounded libsvtav1 fallback call in log:\n%s", logStr)
	}
}

func TestAtomicPromoteFile_RollbackHoldingOnRenameError(t *testing.T) {
	f := newTestFix(t)
	ctx := context.Background()
	fc, err := mediafiles.LoadTranscodeFile(ctx, f.st.Read(), f.movieFile)
	if err != nil {
		t.Fatalf("LoadTranscodeFile failed: %v", err)
	}

	inputPath := filepath.Join(f.libMovie, filepath.FromSlash(f.movieRel))
	info, err := os.Stat(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	targetRel := replaceRelExtMKV(f.movieRel)
	targetPath := filepath.Join(f.libMovie, filepath.FromSlash(targetRel))
	missingStage := filepath.Join(t.TempDir(), "does-not-exist.mkv")

	cfg := DefaultProfileConfig()
	cfg.KeepOriginalHolding = true

	err = f.svc.atomicPromoteFile(
		ctx,
		fc,
		info.Size(), info.ModTime().UnixNano(),
		inputPath, missingStage, targetPath, targetRel,
		ScalePlan{FrameH: 1080},
		false,
		cfg,
		1, 1,
		ProbeStream{CodecName: "h264", Width: 1920, Height: 1080},
		"",
		nil, nil, "", false, 0, 0, time.Now(),
	)
	if err == nil {
		t.Fatal("expected atomicPromoteFile to fail when stagePath is missing")
	}
	if _, statErr := os.Stat(inputPath); statErr != nil {
		t.Fatalf("expected original file restored to %s after rename failure: %v", inputPath, statErr)
	}
}

func TestEffectiveConcurrentJobs(t *testing.T) {
	if got := effectiveConcurrentJobs(1, []GPUDevice{{Selected: true, MemoryMB: 16384}}); got != 1 {
		t.Fatalf("wanted=1 expected 1, got %d", got)
	}
	single6GB := []GPUDevice{
		{Path: "/dev/dri/renderD128", Selected: false, MemoryMB: 0},
		{Path: "/dev/dri/renderD129", Selected: true, MemoryMB: 6144},
	}
	if got := effectiveConcurrentJobs(2, single6GB); got != 1 {
		t.Fatalf("single 6GB GPU expected cap to 1, got %d", got)
	}
	single12GB := []GPUDevice{
		{Path: "/dev/dri/renderD129", Selected: true, MemoryMB: 12288},
	}
	if got := effectiveConcurrentJobs(2, single12GB); got != 2 {
		t.Fatalf("single 12GB GPU expected 2, got %d", got)
	}
	dual12GB := []GPUDevice{
		{Path: "/dev/dri/renderD129", Selected: true, MemoryMB: 12288},
		{Path: "/dev/dri/renderD130", Selected: true, MemoryMB: 12288},
	}
	if got := effectiveConcurrentJobs(2, dual12GB); got != 2 {
		t.Fatalf("dual 12GB GPUs expected 2, got %d", got)
	}
	dual6GB := []GPUDevice{
		{Path: "/dev/dri/renderD129", Selected: true, MemoryMB: 6144},
		{Path: "/dev/dri/renderD130", Selected: true, MemoryMB: 6144},
	}
	if got := effectiveConcurrentJobs(2, dual6GB); got != 2 {
		t.Fatalf("dual 6GB GPUs expected 2, got %d", got)
	}
}

func TestShouldStageInWorkDir(t *testing.T) {
	mkUsage := func(targetPct int, targetFS string, workAvail uint64, workPct int, workFS string, ok bool) func(string) (uint64, int, string, bool) {
		return func(p string) (uint64, int, string, bool) {
			if p == "/media" {
				return 1 << 30, targetPct, targetFS, ok
			}
			return workAvail, workPct, workFS, ok
		}
	}

	ok, pct := shouldStageInWorkDirWith("/media", "/tmp/work", 2<<30, mkUsage(99, "fs-media", 40<<30, 30, "fs-tmp", true))
	if !ok || pct != 99 {
		t.Fatalf("expected 99%%-full target + roomy workDir to stage in workDir, got ok=%v pct=%d", ok, pct)
	}

	ok, _ = shouldStageInWorkDirWith("/media", "/tmp/work", 2<<30, mkUsage(60, "fs-media", 40<<30, 30, "fs-tmp", true))
	if ok {
		t.Fatal("expected normal 60%% target filesystem to stay same-FS")
	}

	ok, _ = shouldStageInWorkDirWith("/media", "/tmp/work", 2<<30, mkUsage(99, "fs-same", 40<<30, 30, "fs-same", true))
	if ok {
		t.Fatal("expected same filesystem not to fallback")
	}

	ok, _ = shouldStageInWorkDirWith("/media", "/tmp/work", 2<<30, mkUsage(99, "fs-media", 5<<30, 30, "fs-tmp", true))
	if ok {
		t.Fatal("expected small workDir (<10GiB) not to fallback")
	}
}

func TestActiveGPUVRAMTrackingDuringEncode(t *testing.T) {
	f := newTestFix(t)
	logPath := filepath.Join(t.TempDir(), "ffmpeg.log")
	installFakeBinaries(t, logPath, "ok")
	makeFakeIntelArcNode(t, f.svc)
	f.svc.GPUVRAMProbe = func(_ context.Context) []GPUVRAMInfo {
		return []GPUVRAMInfo{{Index: 0, MemoryMB: 12288, FreeMB: 10000}}
	}

	var yieldCard, yieldReserve, activeDuringEncode int
	f.svc.YieldGPUVRAM = func(_ context.Context, card int, reserveMB int) {
		yieldCard = card
		yieldReserve = reserveMB
		activeDuringEncode = f.svc.ActiveGPUVRAMMB(card)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := f.q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer f.q.Stop()

	if err := f.svc.QueueFile(ctx, f.movieFile, false); err != nil {
		t.Fatal(err)
	}
	waitJobDone(t, f.q, f.st)

	if yieldCard != 0 || yieldReserve <= 0 {
		t.Fatalf("expected YieldGPUVRAM called with card=0 reserve>0, got card=%d reserve=%d", yieldCard, yieldReserve)
	}
	if activeDuringEncode != yieldReserve {
		t.Fatalf("expected ActiveGPUVRAMMB(0) == %d during encode, got %d", yieldReserve, activeDuringEncode)
	}
	if after := f.svc.ActiveGPUVRAMMB(-1); after != 0 {
		t.Fatalf("expected ActiveGPUVRAMMB(-1) == 0 after encode release, got %d", after)
	}
}

func TestRunStreamingFFmpegStallWatchdog(t *testing.T) {
	orig := ffmpegStallTimeout
	ffmpegStallTimeout = 200 * time.Millisecond
	t.Cleanup(func() {
		ffmpegStallTimeout = orig
	})

	dir := t.TempDir()
	fakeFFmpeg := filepath.Join(dir, "ffmpeg")
	script := `#!/usr/bin/env bash
while true; do
  printf 'out_time_us=1000000\nprogress=continue\n'
  sleep 0.05
done
`
	if err := os.WriteFile(fakeFFmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err := runStreamingFFmpeg(context.Background(), nil, fakeFFmpeg, []string{"-version"}, nil)
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("expected stalled error, got err=%v", err)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("expected stall watchdog to terminate within 2s, took %s", elapsed)
	}
}
