package transcode

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
)

func TestProbeVRAMCached_StaleWhileRevalidate(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	var notifyOnce sync.Once

	s := &Service{
		vramCache:   []GPUVRAMInfo{{Index: 0, Name: "Stale GPU", MemoryMB: 8192, FreeMB: 4000}},
		vramCacheAt: time.Now().Add(-time.Minute),
		GPUVRAMProbe: func(_ context.Context) []GPUVRAMInfo {
			calls.Add(1)
			notifyOnce.Do(func() { close(started) })
			<-release
			return []GPUVRAMInfo{{Index: 0, Name: "Fresh GPU", MemoryMB: 12216, FreeMB: 10000}}
		},
	}

	ctx := context.Background()
	start := time.Now()
	got := s.probeVRAMCached(ctx, false)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("expired warm cache blocked for %v", elapsed)
	}
	if len(got) != 1 || got[0].Name != "Stale GPU" {
		t.Fatalf("got %+v, want Stale GPU", got)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background VRAM refresh did not start")
	}

	got2 := s.probeVRAMCached(ctx, false)
	if len(got2) != 1 || got2[0].Name != "Stale GPU" {
		t.Fatalf("got2 %+v, want Stale GPU", got2)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 probe call while refreshing, got %d", calls.Load())
	}

	close(release)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cur := s.probeVRAMCached(ctx, false)
		if len(cur) == 1 && cur[0].Name == "Fresh GPU" {
			if calls.Load() != 1 {
				t.Fatalf("expected 1 total probe call, got %d", calls.Load())
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("VRAM cache did not refresh asynchronously")
}

func TestProbeVRAMCached_ColdStartSingleflight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	var notifyOnce sync.Once

	s := &Service{
		GPUVRAMProbe: func(_ context.Context) []GPUVRAMInfo {
			calls.Add(1)
			notifyOnce.Do(func() { close(started) })
			<-release
			return []GPUVRAMInfo{{Index: 0, Name: "Fresh GPU", MemoryMB: 12216, FreeMB: 10000}}
		},
	}

	const callers = 3
	results := make([][]GPUVRAMInfo, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = s.probeVRAMCached(context.Background(), false)
		}()
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("cold VRAM probe did not start")
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	for i, res := range results {
		if len(res) != 1 || res[0].Name != "Fresh GPU" {
			t.Fatalf("caller %d got %+v, want Fresh GPU", i, res)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 cold probe across %d concurrent callers, got %d", callers, calls.Load())
	}
}

func TestOverview_LiveSpeedSumsConcurrentJobs(t *testing.T) {
	f := newTestFix(t)
	ctx := context.Background()
	ts := database.FormatTime(time.Now().UTC())
	sysActor := `{"kind":"system"}`

	var job1ID, job2ID int64
	err := f.st.Write(ctx, func(tx *sql.Tx) error {
		r1, err := tx.Exec(`INSERT INTO jobs (created_at, created_by, updated_at, updated_by, kind, subject, params, state, progress, run_after, asked_by)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'running', 0.4, ?, ?)`,
			ts, sysActor, ts, sysActor, KindFile, `{"type":"file","id":1}`, `{"file_id":1}`, ts, sysActor)
		if err != nil {
			return err
		}
		job1ID, _ = r1.LastInsertId()

		r2, err := tx.Exec(`INSERT INTO jobs (created_at, created_by, updated_at, updated_by, kind, subject, params, state, progress, run_after, asked_by)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'running', 0.6, ?, ?)`,
			ts, sysActor, ts, sysActor, KindFile, `{"type":"file","id":2}`, `{"file_id":2}`, ts, sysActor)
		if err != nil {
			return err
		}
		job2ID, _ = r2.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	f.svc.setLiveStat(job1ID, liveStat{phase: "encoding", fps: 500.0, speed: 20.8})
	f.svc.setLiveStat(job2ID, liveStat{phase: "encoding", fps: 678.0, speed: 28.25})

	ov, err := f.svc.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ov.Speed.LiveFPS != 1178.0 {
		t.Fatalf("LiveFPS = %v, want 1178.0", ov.Speed.LiveFPS)
	}
	if ov.Speed.LiveRealtimeX != 49.05 {
		t.Fatalf("LiveRealtimeX = %v, want 49.05", ov.Speed.LiveRealtimeX)
	}
}
