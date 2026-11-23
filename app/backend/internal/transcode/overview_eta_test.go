package transcode

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
)

func TestComputeBacklogETA_ZeroCases(t *testing.T) {
	if got := computeBacklogETA(0, []int64{1, 2}, map[int64]float64{1: 1000}, nil, nil, nil, 0, 10, 1, 2); got != 0 {
		t.Fatalf("pending=0 gave %d, want 0", got)
	}
	if got := computeBacklogETA(2, nil, nil, nil, nil, nil, 0, 10, 1, 2); got != 0 {
		t.Fatalf("empty fileIDs gave %d, want 0", got)
	}
	if got := computeBacklogETA(1, []int64{1, 2}, map[int64]float64{1: 1000, 2: 1000}, map[int64]bool{1: true, 2: true}, nil, nil, 0, 10, 1, 2); got != 0 {
		t.Fatalf("all finished gave %d, want 0", got)
	}
	if got := computeBacklogETA(2, []int64{1, 2}, map[int64]float64{1: 1000}, nil, nil, nil, 0, 0, 0, 2); got != 0 {
		t.Fatalf("no live or historical speed gave %d, want 0", got)
	}
	if got := computeBacklogETA(1, []int64{1}, nil, nil, []doneEncodeSample{{FileID: 99, EncodeSeconds: 0, AvgSpeed: 10}}, nil, 0, 0, 0, 1); got != 0 {
		t.Fatalf("zero encode_seconds sample gave %d, want 0", got)
	}
}

func TestComputeBacklogETA_LiveSpeedProgressOverheadAndScaling(t *testing.T) {
	fileIDs := []int64{1, 2, 3}
	runtimes := map[int64]float64{
		1: 1000,
		2: 2000,
		3: 0, // uses avgKnownRuntime = (1000+2000)/2 = 1500
	}
	finished := map[int64]bool{1: true}
	activeJobs := []jobs.Job{
		{
			Kind:     KindFile,
			State:    "running",
			Subject:  &domain.Subject{Type: "file", ID: 2},
			Progress: 0.5, // remaining for file 2 = 1000s; total remaining = 2500s
		},
		{
			Kind:     KindScan,
			State:    "running",
			Progress: 0.9,
		},
	}
	recent := []doneEncodeSample{
		// histPerWorkerX = 1000 / 100 = 10.0; overhead = 15.0 / 10.0 = 1.5
		{FileID: 1, EncodeSeconds: 100, AvgSpeed: 15.0},
	}

	// 2 running file jobs, 1 currently reporting ffmpeg speed (other is in VMAF/probing):
	// scaledLive = 12.0 * (2/1) = 24.0; aggX = 24.0 / 1.5 = 16.0; ETA = round(2500 / 16.0) = 156.
	got := computeBacklogETA(2, fileIDs, runtimes, finished, recent, activeJobs, 2, 12.0, 1, 2)
	if got != 156 {
		t.Fatalf("got %d, want 156", got)
	}

	// Overhead clamps at [1.0, 2.5].
	highOverhead := []doneEncodeSample{{FileID: 1, EncodeSeconds: 100, AvgSpeed: 40.0}}
	gotClampedHigh := computeBacklogETA(1, []int64{2}, map[int64]float64{1: 1000, 2: 1000}, nil, highOverhead, nil, 1, 25.0, 1, 1)
	if gotClampedHigh != 100 {
		t.Fatalf("overhead upper clamp gave %d, want 100", gotClampedHigh)
	}

	lowOverhead := []doneEncodeSample{{FileID: 1, EncodeSeconds: 100, AvgSpeed: 5.0}}
	gotClampedLow := computeBacklogETA(1, []int64{2}, map[int64]float64{1: 1000, 2: 1000}, nil, lowOverhead, nil, 1, 10.0, 1, 1)
	if gotClampedLow != 100 {
		t.Fatalf("overhead lower clamp gave %d, want 100", gotClampedLow)
	}
}

func TestComputeBacklogETA_IdleHistoricalFallback(t *testing.T) {
	fileIDs := []int64{1, 2, 3, 4}
	runtimes := map[int64]float64{1: 1200, 2: 1200, 3: 1200, 4: 1200}
	finished := map[int64]bool{1: true} // remFiles = 3, remMediaSec = 3600
	recent := []doneEncodeSample{
		{FileID: 1, EncodeSeconds: 120, AvgSpeed: 12.0}, // histPerWorkerX = 1200 / 120 = 10.0
	}

	// Idle: ConcurrentJobs=2 < remFiles=3 -> workers=2 -> aggX=20.0 -> ETA=180.
	if got := computeBacklogETA(3, fileIDs, runtimes, finished, recent, nil, 0, 0, 0, 2); got != 180 {
		t.Fatalf("concurrent=2 gave %d, want 180", got)
	}

	// Idle: ConcurrentJobs=5 > remFiles=3 -> workers=min(5,3)=3 -> aggX=30.0 -> ETA=120.
	if got := computeBacklogETA(3, fileIDs, runtimes, finished, recent, nil, 0, 0, 0, 5); got != 120 {
		t.Fatalf("concurrent=5 capped at remFiles gave %d, want 120", got)
	}

	// No runtimes known: fallback avgKnownRuntime = 2700.0; sample without runtime uses EncodeSeconds*AvgSpeed.
	sampleNoRT := []doneEncodeSample{{FileID: 99, EncodeSeconds: 100, AvgSpeed: 10.0}} // histPerWorkerX = 10.0
	if got := computeBacklogETA(1, []int64{2}, map[int64]float64{}, nil, sampleNoRT, nil, 0, 0, 0, 1); got != 270 {
		t.Fatalf("fallback runtime gave %d, want 270", got)
	}
}

func TestServiceOverview_ETASeconds(t *testing.T) {
	f := newTestFix(t)
	ctx := context.Background()
	ts := database.FormatTime(time.Now().UTC())
	sysActor := `{"kind":"system"}`

	err := f.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE files SET media_info = '{"runtime_seconds":3600}' WHERE id = ?`, f.movieFile); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE files SET media_info = '{"runtime_seconds":1800}' WHERE id = ?`, f.epFile); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO transcode_items (
			created_at, created_by, updated_at, updated_by, file_id, state,
			encode_seconds, avg_fps, avg_speed, completed_at
		) VALUES (?, ?, ?, ?, ?, 'done', 360.0, 240.0, 12.0, ?)`,
			ts, sysActor, ts, sysActor, f.movieFile, ts)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	ov, err := f.svc.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ov.Counts.Pending != 1 || ov.Counts.Done != 1 {
		t.Fatalf("unexpected counts: %+v", ov.Counts)
	}
	// 1800s remaining / (3600s / 360s * 1 effective worker) = 180s.
	if ov.ETASeconds != 180 {
		t.Fatalf("ETASeconds = %d, want 180", ov.ETASeconds)
	}

	b, err := json.Marshal(ov)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["eta_seconds"] != float64(180) {
		t.Fatalf("json eta_seconds = %v, want 180", payload["eta_seconds"])
	}
}
