package transcode

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
)

func TestScanPreservesDoneWithMtimeDriftAndUserSkipped(t *testing.T) {
	f := newTestFix(t)
	ctx := context.Background()

	// Replace the dummy files with valid synthetic MKVs so FastProbeFile parses them without ffprobe.
	movieMKV := filepath.Join(f.libMovie, filepath.FromSlash(f.movieRel))
	writeMKVSegment(t, movieMKV, bytes.Join([][]byte{
		ebmlTracksBytes("V_AV1", 1920, 1080, 0),
		ebmlTagsBytes(fileFlowsCommentTag),
		ebmlClusterBytes(512, 0x01),
	}, nil))
	mStat, err := os.Stat(movieMKV)
	if err != nil {
		t.Fatal(err)
	}

	epMKV := filepath.Join(f.libShow, filepath.FromSlash(f.epRel))
	writeMKVSegment(t, epMKV, bytes.Join([][]byte{
		ebmlTracksBytes("V_MPEG4/ISO/AVC", 1920, 1080, 0),
		ebmlClusterBytes(512, 0x02),
	}, nil))
	eStat, err := os.Stat(epMKV)
	if err != nil {
		t.Fatal(err)
	}

	if err := f.svc.EnsureDefaultProfile(ctx); err != nil {
		t.Fatal(err)
	}
	_, prof, err := f.svc.ResolveFolderProfile(ctx, f.folderM)
	if err != nil {
		t.Fatal(err)
	}

	// Mark movieFile as done with historical stats, and epFile as user_skipped.
	completedAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	vmaf := 94.2
	selQ := 20
	if err := upsertItem(ctx, f.st, &ItemRecord{
		FileID:          f.movieFile,
		ProfileID:       &prof.ID,
		State:           "done",
		FileFlowsTagged: true,
		FileMtimeNs:     mStat.ModTime().UnixNano(),
		FileSizeBytes:   mStat.Size(),
		SourceCodec:     "h264",
		SourceWidth:     1920,
		SourceHeight:    1080,
		TargetCodec:     "av1",
		SelectedQ:       &selQ,
		MeanVMAF:        &vmaf,
		InputBytes:      10_000_000,
		OutputBytes:     mStat.Size(),
		SavedBytes:      10_000_000 - mStat.Size(),
		CompletedAt:     &completedAt,
		ProbedAt:        &completedAt,
	}, domain.System); err != nil {
		t.Fatal(err)
	}

	skipReason := "user_skipped"
	if err := upsertItem(ctx, f.st, &ItemRecord{
		FileID:        f.epFile,
		ProfileID:     &prof.ID,
		State:         "skipped",
		SkipReason:    &skipReason,
		FileMtimeNs:   eStat.ModTime().UnixNano(),
		FileSizeBytes: eStat.Size(),
		SourceCodec:   "h264",
		TargetCodec:   "av1",
		InputBytes:    eStat.Size(),
		ProbedAt:      &completedAt,
	}, domain.System); err != nil {
		t.Fatal(err)
	}

	// Simulate SMB/NFS mtime drift on both files (size unchanged).
	drifted := mStat.ModTime().Add(30 * time.Second)
	if err := os.Chtimes(movieMKV, drifted, drifted); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(epMKV, drifted, drifted); err != nil {
		t.Fatal(err)
	}

	var enqueuedCount atomic.Int32
	f.svc.Enqueue = func(ctx context.Context, kind string, subj *domain.Subject, params map[string]any, key string) error {
		if kind == KindFile {
			enqueuedCount.Add(1)
		}
		return nil
	}

	if _, _, err := f.q.Enqueue(ctx, KindScan, nil, map[string]any{"queue_missing": true}, "transcode:scan:test", domain.System); err != nil {
		t.Fatal(err)
	}
	if err := f.q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer f.q.Stop()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		active, aErr := jobs.ListActive(ctx, f.st.Read(), []string{KindScan}, 10)
		if aErr == nil && len(active) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if enqueuedCount.Load() != 0 {
		t.Fatalf("expected 0 re-queued jobs for done/user_skipped files, got %d", enqueuedCount.Load())
	}

	// Verify movieFile kept state=done and all historical stats.
	mRec, found, err := getItemByFileID(ctx, f.st.Read(), f.movieFile)
	if err != nil || !found {
		t.Fatalf("missing movie item: found=%v err=%v", found, err)
	}
	if mRec.State != "done" || mRec.SavedBytes != 10_000_000-mStat.Size() || mRec.MeanVMAF == nil || *mRec.MeanVMAF != vmaf || mRec.CompletedAt == nil {
		t.Fatalf("done item was modified by scan: %+v", mRec)
	}

	// Verify epFile kept state=skipped and skip_reason=user_skipped.
	eRec, found, err := getItemByFileID(ctx, f.st.Read(), f.epFile)
	if err != nil || !found {
		t.Fatalf("missing ep item: found=%v err=%v", found, err)
	}
	if eRec.State != "skipped" || eRec.SkipReason == nil || *eRec.SkipReason != "user_skipped" {
		t.Fatalf("user_skipped item was overwritten by scan: %+v", eRec)
	}

	// Verify QueueAll also skips both done and user_skipped files.
	f.svc.Enqueue = nil
	queued, skipped, err := f.svc.QueueAll(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if queued != 0 || skipped != 2 {
		t.Fatalf("QueueAll expected queued=0 skipped=2, got queued=%d skipped=%d", queued, skipped)
	}
}

func TestFastSkipHandleFileJobWithoutFFprobe(t *testing.T) {
	f := newTestFix(t)
	ctx := context.Background()

	// Make epMKV a real synthetic MKV tagged by FileFlows (no ffprobe configured!).
	epMKV := filepath.Join(f.libShow, filepath.FromSlash(f.epRel))
	writeMKVSegment(t, epMKV, bytes.Join([][]byte{
		ebmlTracksBytes("V_MPEGH/ISO/HEVC", 1920, 1080, 0),
		ebmlTagsBytes(fileFlowsCommentTag),
		ebmlClusterBytes(1024, 0x05),
	}, nil))

	// Point HOLYARRS_FFPROBE to a nonexistent path so any ffprobe invocation would fail.
	t.Setenv("HOLYARRS_FFPROBE", filepath.Join(t.TempDir(), "missing-ffprobe"))
	t.Setenv("HOLYARRS_FFMPEG", filepath.Join(t.TempDir(), "missing-ffmpeg"))

	if err := f.svc.QueueFile(ctx, f.epFile, false); err != nil {
		t.Fatal(err)
	}
	if err := f.q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer f.q.Stop()
	waitJobDone(t, f.q, f.st)

	rec, found, err := getItemByFileID(ctx, f.st.Read(), f.epFile)
	if err != nil || !found {
		t.Fatalf("expected transcode_items row for fast-skipped MKV, found=%v err=%v", found, err)
	}
	if rec.State != "skipped" || rec.SkipReason == nil || *rec.SkipReason != "tag_matched" || !rec.FileFlowsTagged || rec.SourceCodec != "hevc" {
		t.Fatalf("unexpected fast-skip item record: %+v", rec)
	}
}

func TestWaitingBumpSkipRoutesAndHistoryFilter(t *testing.T) {
	f := newTestFix(t)

	// Queue both files (queue not started so jobs stay queued).
	if err := f.svc.QueueFile(context.Background(), f.movieFile, false); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.QueueFile(context.Background(), f.epFile, false); err != nil {
		t.Fatal(err)
	}

	// GET /api/v1/transcode/waiting returns 2 jobs in FIFO order.
	waitRR := doReq(t, f.mux, "GET", "/api/v1/transcode/waiting", nil)
	if waitRR.Code != http.StatusOK {
		t.Fatalf("GET waiting %d: %s", waitRR.Code, waitRR.Body.String())
	}
	var waitResp struct {
		Jobs  []WaitingJob `json:"jobs"`
		Total int          `json:"total"`
	}
	if err := json.Unmarshal(waitRR.Body.Bytes(), &waitResp); err != nil {
		t.Fatal(err)
	}
	if waitResp.Total != 2 || len(waitResp.Jobs) != 2 {
		t.Fatalf("expected 2 waiting jobs, got %+v", waitResp)
	}
	firstID := waitResp.Jobs[0].ID
	secondID := waitResp.Jobs[1].ID
	if waitResp.Jobs[0].Position != 1 || waitResp.Jobs[1].Position != 2 {
		t.Fatalf("unexpected waiting positions: %+v", waitResp.Jobs)
	}

	// POST /api/v1/transcode/jobs/{id}/bump moves secondID to position 1 with priority='now'.
	bumpRR := doReq(t, f.mux, "POST", "/api/v1/transcode/jobs/"+strconv.FormatInt(secondID, 10)+"/bump", nil)
	if bumpRR.Code != http.StatusOK {
		t.Fatalf("POST bump %d: %s", bumpRR.Code, bumpRR.Body.String())
	}
	var bumpResp struct {
		ID       int64  `json:"id"`
		Priority string `json:"priority"`
		State    string `json:"state"`
	}
	_ = json.Unmarshal(bumpRR.Body.Bytes(), &bumpResp)
	if bumpResp.ID != secondID || bumpResp.Priority != "now" || bumpResp.State != "queued" {
		t.Fatalf("unexpected bump response: %+v", bumpResp)
	}

	// Verify filtered search preserves actual queue position (firstID is now position 2).
	searchRR := doReq(t, f.mux, "GET", "/api/v1/transcode/waiting?q=Test+Movie", nil)
	if searchRR.Code != http.StatusOK {
		t.Fatalf("GET waiting search %d: %s", searchRR.Code, searchRR.Body.String())
	}
	var searchResp struct {
		Jobs  []WaitingJob `json:"jobs"`
		Total int          `json:"total"`
	}
	_ = json.Unmarshal(searchRR.Body.Bytes(), &searchResp)
	if searchResp.Total != 1 || searchResp.Jobs[0].ID != firstID || searchResp.Jobs[0].Position != 2 {
		t.Fatalf("expected bumped-down movie at position 2, got %+v", searchResp)
	}

	// POST /api/v1/transcode/jobs/{id}/skip drops secondID cleanly without leaving a cancelled row in /failed.
	skipJobRR := doReq(t, f.mux, "POST", "/api/v1/transcode/jobs/"+strconv.FormatInt(secondID, 10)+"/skip", nil)
	if skipJobRR.Code != http.StatusOK {
		t.Fatalf("POST skip job %d: %s", skipJobRR.Code, skipJobRR.Body.String())
	}
	var skipJobResp struct {
		ID      int64 `json:"id"`
		Skipped bool  `json:"skipped"`
		FileID  int64 `json:"file_id"`
	}
	_ = json.Unmarshal(skipJobRR.Body.Bytes(), &skipJobResp)
	if !skipJobResp.Skipped || skipJobResp.FileID != f.epFile {
		t.Fatalf("unexpected skip job response: %+v", skipJobResp)
	}

	failRR := doReq(t, f.mux, "GET", "/api/v1/transcode/failed", nil)
	if failRR.Code != http.StatusOK {
		t.Fatalf("GET failed %d: %s", failRR.Code, failRR.Body.String())
	}
	var failResp struct {
		Total int `json:"total"`
	}
	_ = json.Unmarshal(failRR.Body.Bytes(), &failResp)
	if failResp.Total != 0 {
		t.Fatalf("expected 0 failed/cancelled jobs after DropQueued skip, got %d", failResp.Total)
	}

	// POST /api/v1/files/{id}/transcode/skip drops queued movie job and marks movieFile user_skipped.
	skipFileRR := doReq(t, f.mux, "POST", "/api/v1/files/"+strconv.FormatInt(f.movieFile, 10)+"/transcode/skip", nil)
	if skipFileRR.Code != http.StatusOK {
		t.Fatalf("POST skip file %d: %s", skipFileRR.Code, skipFileRR.Body.String())
	}

	// Waiting queue should now be empty.
	afterWaitRR := doReq(t, f.mux, "GET", "/api/v1/transcode/waiting", nil)
	_ = json.Unmarshal(afterWaitRR.Body.Bytes(), &waitResp)
	if waitResp.Total != 0 {
		t.Fatalf("expected 0 waiting jobs after skipping both, got %d", waitResp.Total)
	}

	// GET /api/v1/transcode/items?state=history returns both user_skipped items via fast index path.
	histRR := doReq(t, f.mux, "GET", "/api/v1/transcode/items?state=history", nil)
	if histRR.Code != http.StatusOK {
		t.Fatalf("GET history items %d: %s", histRR.Code, histRR.Body.String())
	}
	var histResp struct {
		Items []ItemRow `json:"items"`
		Total int       `json:"total"`
	}
	_ = json.Unmarshal(histRR.Body.Bytes(), &histResp)
	if histResp.Total != 2 || len(histResp.Items) != 2 {
		t.Fatalf("expected 2 history items, got %+v", histResp)
	}
	for _, it := range histResp.Items {
		if it.State != "skipped" || it.SkipReason != "user_skipped" {
			t.Fatalf("expected user_skipped in history, got %+v", it)
		}
	}

	// Filtered history query (q=Anime) also works via fast path.
	histSearchRR := doReq(t, f.mux, "GET", "/api/v1/transcode/items?state=history&q=Anime", nil)
	if histSearchRR.Code != http.StatusOK {
		t.Fatalf("GET filtered history %d: %s", histSearchRR.Code, histSearchRR.Body.String())
	}
	_ = json.Unmarshal(histSearchRR.Body.Bytes(), &histResp)
	if histResp.Total != 1 || histResp.Items[0].FileID != f.epFile {
		t.Fatalf("expected 1 filtered history item for Anime, got %+v", histResp)
	}
}

func TestHardwareProbeCacheAndRefresh(t *testing.T) {
	f := newTestFix(t)
	ctx := context.Background()
	binDir := t.TempDir()
	countFile := filepath.Join(binDir, "calls")
	fakeFFmpeg := filepath.Join(binDir, "ffmpeg")
	fakeFFprobe := filepath.Join(binDir, "ffprobe")

	script := `#!/usr/bin/env bash
echo x >> "` + countFile + `"
if [[ " ${*} " == *" -encoders "* ]]; then
  echo " V..... libsvtav1 SVT-AV1"
  exit 0
fi
if [[ " ${*} " == *" -filters "* ]]; then
  echo " ... zscale ..."
  exit 0
fi
`
	if err := os.WriteFile(fakeFFmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fakeFFprobe, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOLYARRS_FFMPEG", fakeFFmpeg)
	t.Setenv("HOLYARRS_FFPROBE", fakeFFprobe)
	t.Setenv("HOLYARRS_VMAF_FFMPEG", fakeFFmpeg)

	st := ReadSettings(ctx, f.st.Read())
	hw1 := f.svc.probeHardwareWithSettings(ctx, st)
	if !hw1.Ready || !hw1.ZscaleAvail {
		t.Fatalf("unexpected hw1: %+v", hw1)
	}
	callsAfterFirst := len(mustReadBytes(t, countFile))

	// Second call via probeHardwareWithSettings must hit cache (no extra ffmpeg spawns).
	hw2 := f.svc.probeHardwareWithSettings(ctx, st)
	if !hw2.Ready || len(mustReadBytes(t, countFile)) != callsAfterFirst {
		t.Fatalf("expected cached hardware probe with no extra invocations")
	}

	// Explicit ProbeHardware refreshes the cache.
	_ = f.svc.ProbeHardware(ctx)
	if len(mustReadBytes(t, countFile)) <= callsAfterFirst {
		t.Fatalf("expected explicit ProbeHardware to force refresh")
	}
}

func TestMarkItemErrorWritesJSONActorAndScansInWaiting(t *testing.T) {
	f := newTestFix(t)
	ctx := context.Background()

	// Insert a new transcode_items row via markItemError (no row exists yet for movieFile),
	// then update it to exercise both the INSERT and UPDATE branches.
	markItemError(ctx, f.st, f.movieFile, 41, "initial error")
	markItemError(ctx, f.st, f.movieFile, 42, "retried error")

	if err := f.svc.QueueFile(ctx, f.movieFile, false); err != nil {
		t.Fatal(err)
	}

	jobs, total, err := f.svc.ListWaitingJobs(ctx, "", 50, 0)
	if err != nil {
		t.Fatalf("ListWaitingJobs: %v", err)
	}
	if total != 1 || len(jobs) != 1 || jobs[0].FileID != f.movieFile {
		t.Fatalf("unexpected waiting jobs: total=%d jobs=%+v", total, jobs)
	}

	items, err := listItemsByFileIDs(ctx, f.st.Read(), []int64{f.movieFile})
	if err != nil {
		t.Fatalf("listItemsByFileIDs: %v", err)
	}
	rec, ok := items[f.movieFile]
	if !ok {
		t.Fatalf("expected transcode_items row for file %d", f.movieFile)
	}
	if rec.CreatedBy != domain.System || rec.UpdatedBy != domain.System {
		t.Fatalf("unexpected actors: created_by=%+v updated_by=%+v", rec.CreatedBy, rec.UpdatedBy)
	}
}

func mustReadBytes(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
