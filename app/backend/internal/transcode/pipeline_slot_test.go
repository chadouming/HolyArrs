package transcode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
)

func installDynamicVMAFBinaries(t *testing.T, logPath, probeDir string) {
	t.Helper()
	binDir := t.TempDir()
	ffprobe := filepath.Join(binDir, "ffprobe")
	ffmpeg := filepath.Join(binDir, "ffmpeg")

	ffprobeScript := `#!/usr/bin/env bash
set -euo pipefail
target="${@: -1}"
if [[ "$target" == *"Movie (2024)"* ]]; then
  touch "` + filepath.Join(probeDir, "movie.probed") + `"
fi
if [[ "$target" == *"AnimeShow"* ]]; then
  touch "` + filepath.Join(probeDir, "ep.probed") + `"
fi
if [[ "$target" == *"encoded.mkv"* ]]; then
  cat <<'EOF'
{"format":{"filename":"'$target'","duration":"120.0","size":"1200","tags":{"comment":"HolyArrs"}},"streams":[{"index":0,"codec_type":"video","codec_name":"av1","width":1920,"height":1080,"sample_aspect_ratio":"1:1","avg_frame_rate":"24/1","disposition":{"attached_pic":0}},{"index":1,"codec_type":"audio","codec_name":"aac","channels":2,"bit_rate":"192000","tags":{"language":"fra"}}]}
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
  echo " V..... hevc_qsv HEVC QSV"
  echo " V..... hevc_vaapi HEVC VAAPI"
  echo " V..... libsvtav1 SVT-AV1"
  echo " V..... libx265 HEVC"
  echo " V..... libx264 H264"
  exit 0
fi
if [[ " ${*} " == *" -filters "* ]]; then
  echo " ... tonemapx ..."
  echo " ... libvmaf ..."
  exit 0
fi
if [[ " ${*} " == *"libvmaf"* ]]; then
  if [[ " ${*} " == *"cand_q18_"* || " ${*} " == *"cand_q17_"* || " ${*} " == *"cand_q16_"* || " ${*} " == *"cand_q15_"* ]]; then
    echo "VMAF score: 96.000000" >&2
  else
    echo "VMAF score: 94.000000" >&2
  fi
  exit 0
fi
out="${@: -1}"
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
}

func waitMarker(t *testing.T, markerPath string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(markerPath); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for marker %s", markerPath)
}

func TestPipeline_WaitingJobInheritsDefaultProfileUpdate(t *testing.T) {
	f := newTestFix(t)
	logPath := filepath.Join(t.TempDir(), "ffmpeg.log")
	probeDir := t.TempDir()
	installDynamicVMAFBinaries(t, logPath, probeDir)
	makeFakeIntelArcNode(t, f.svc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := ReadSettings(ctx, f.st.Read())
	st.ConcurrentJobs = 1
	if err := f.svc.UpdateSettings(ctx, st, domain.Actor{Kind: "user", ID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.acquireEncodeSlot(ctx, func() int { return 1 }); err != nil {
		t.Fatal(err)
	}
	held := true
	defer func() {
		if held {
			f.svc.releaseEncodeSlot()
		}
	}()

	if err := f.q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer f.q.Stop()

	if err := f.svc.QueueFile(ctx, f.movieFile, false); err != nil {
		t.Fatal(err)
	}
	waitMarker(t, filepath.Join(probeDir, "movie.probed"))

	profs, _, err := f.svc.ListProfiles(ctx)
	if err != nil || len(profs) == 0 {
		t.Fatalf("ListProfiles: %v len=%d", err, len(profs))
	}
	cfg := profs[0].ProfileConfig
	cfg.VMAFMinScore = 95.0
	if _, err := f.svc.UpdateProfile(ctx, profs[0].ID, ProfileInput{
		Name:      profs[0].Name,
		IsDefault: true,
		Config:    &cfg,
	}, domain.Actor{Kind: "user", ID: 1}); err != nil {
		t.Fatal(err)
	}

	held = false
	f.svc.releaseEncodeSlot()
	waitJobDone(t, f.q, f.st)

	rec, found, err := getItemByFileID(ctx, f.st.Read(), f.movieFile)
	if err != nil || !found {
		t.Fatalf("expected transcode_items row, found=%v err=%v", found, err)
	}
	if rec.State != "done" || rec.MeanVMAF == nil || *rec.MeanVMAF < 95.0 || rec.SelectedQ == nil || *rec.SelectedQ != 18 {
		t.Fatalf("expected inherited VMAFMinScore=95 with MeanVMAF>=95 and SelectedQ=18, got %+v", rec)
	}
}

func TestPipeline_WaitingJobInheritsFolderReassignmentAndDisable(t *testing.T) {
	f := newTestFix(t)
	logPath := filepath.Join(t.TempDir(), "ffmpeg.log")
	probeDir := t.TempDir()
	installDynamicVMAFBinaries(t, logPath, probeDir)
	makeFakeIntelArcNode(t, f.svc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := ReadSettings(ctx, f.st.Read())
	st.ConcurrentJobs = 1
	if err := f.svc.UpdateSettings(ctx, st, domain.Actor{Kind: "user", ID: 1}); err != nil {
		t.Fatal(err)
	}

	customCfg := DefaultProfileConfig()
	customCfg.TargetCodec = "hevc"
	customCfg.VMAFMinScore = 95.0
	customProf, err := f.svc.CreateProfile(ctx, ProfileInput{
		Name:   "Movie HEVC",
		Config: &customCfg,
	}, domain.Actor{Kind: "user", ID: 1})
	if err != nil {
		t.Fatal(err)
	}

	if err := f.svc.acquireEncodeSlot(ctx, func() int { return 1 }); err != nil {
		t.Fatal(err)
	}
	held := true
	defer func() {
		if held {
			f.svc.releaseEncodeSlot()
		}
	}()

	if err := f.q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer f.q.Stop()

	// Part A: Reassign folderM to customProf while job is waiting for encode slot.
	if err := f.svc.QueueFile(ctx, f.movieFile, false); err != nil {
		t.Fatal(err)
	}
	waitMarker(t, filepath.Join(probeDir, "movie.probed"))

	if _, err := f.svc.SetLibraryFolderProfile(ctx, f.folderM, true, &customProf.ID, domain.Actor{Kind: "user", ID: 1}); err != nil {
		t.Fatal(err)
	}
	held = false
	f.svc.releaseEncodeSlot()
	waitJobDone(t, f.q, f.st)

	rec, found, err := getItemByFileID(ctx, f.st.Read(), f.movieFile)
	if err != nil || !found {
		t.Fatalf("expected movie transcode_items row, found=%v err=%v", found, err)
	}
	if rec.State != "done" || rec.ProfileID == nil || *rec.ProfileID != customProf.ID || rec.TargetCodec != "hevc" {
		t.Fatalf("expected reassigned profile %d (hevc), got %+v", customProf.ID, rec)
	}
	if rec.MeanVMAF == nil || *rec.MeanVMAF < 95.0 || rec.SelectedQ == nil || *rec.SelectedQ != 18 {
		t.Fatalf("expected reassigned profile VMAF>=95 SelectedQ=18, got %+v", rec)
	}

	// Part B: Disable folderS while epFile job is waiting for encode slot.
	_ = os.Remove(logPath)
	if err := f.svc.acquireEncodeSlot(ctx, func() int { return 1 }); err != nil {
		t.Fatal(err)
	}
	held = true
	if err := f.svc.QueueFile(ctx, f.epFile, false); err != nil {
		t.Fatal(err)
	}
	waitMarker(t, filepath.Join(probeDir, "ep.probed"))

	if _, err := f.svc.SetLibraryFolderProfile(ctx, f.folderS, false, nil, domain.Actor{Kind: "user", ID: 1}); err != nil {
		t.Fatal(err)
	}
	held = false
	f.svc.releaseEncodeSlot()
	waitJobDone(t, f.q, f.st)

	epRec, found, err := getItemByFileID(ctx, f.st.Read(), f.epFile)
	if err != nil || !found {
		t.Fatalf("expected ep transcode_items row, found=%v err=%v", found, err)
	}
	if epRec.State != "skipped" || epRec.SkipReason == nil || *epRec.SkipReason != "library_disabled" {
		t.Fatalf("expected skipped/library_disabled, got %+v", epRec)
	}
	if logData, err := os.ReadFile(logPath); err == nil && strings.Contains(string(logData), "AnimeShow") {
		t.Fatalf("expected disabled folder job not to invoke ffmpeg encode, got log:\n%s", string(logData))
	}
}

func TestAcquireEncodeSlot_DynamicConcurrencyAndPause(t *testing.T) {
	f := newTestFix(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := ReadSettings(ctx, f.st.Read())
	st.ConcurrentJobs = 1
	if err := f.svc.UpdateSettings(ctx, st, domain.Actor{Kind: "user", ID: 1}); err != nil {
		t.Fatal(err)
	}

	limitFn := func() int {
		return ReadSettings(ctx, f.st.Read()).ConcurrentJobs
	}
	if err := f.svc.acquireEncodeSlot(ctx, limitFn); err != nil {
		t.Fatal(err)
	}

	slot2Acquired := make(chan struct{})
	go func() {
		if err := f.svc.acquireEncodeSlot(ctx, limitFn); err == nil {
			close(slot2Acquired)
		}
	}()

	select {
	case <-slot2Acquired:
		t.Fatal("slot 2 should block while concurrent_jobs == 1")
	case <-time.After(60 * time.Millisecond):
	}

	// Live increase to concurrent_jobs = 2 wakes slot 2 without releasing slot 1.
	st.ConcurrentJobs = 2
	if err := f.svc.UpdateSettings(ctx, st, domain.Actor{Kind: "user", ID: 1}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-slot2Acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for slot 2 after concurrent_jobs increased to 2")
	}
	f.svc.releaseEncodeSlot()
	f.svc.releaseEncodeSlot()

	// Pause blocks new slot acquisition even when slots are free until resumed.
	if err := f.svc.SetPaused(ctx, true, domain.System); err != nil {
		t.Fatal(err)
	}
	slot3Acquired := make(chan struct{})
	go func() {
		if err := f.svc.acquireEncodeSlot(ctx, limitFn); err == nil {
			close(slot3Acquired)
		}
	}()

	select {
	case <-slot3Acquired:
		t.Fatal("slot 3 should block while Paused() is true")
	case <-time.After(60 * time.Millisecond):
	}

	if err := f.svc.SetPaused(ctx, false, domain.System); err != nil {
		t.Fatal(err)
	}
	select {
	case <-slot3Acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for slot 3 after unpausing")
	}
	f.svc.releaseEncodeSlot()
}
