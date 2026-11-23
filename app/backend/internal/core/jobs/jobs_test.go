package jobs

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
)

var me = database.Actor{Kind: "user", ID: 1}

func newStore(t *testing.T, dir string) *database.Store {
	t.Helper()
	s, err := database.Open(dir, database.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func fastOpts() Options {
	return Options{Workers: 2, BaseBackoff: 5 * time.Millisecond, PollInterval: 5 * time.Millisecond}
}

func getJob(t *testing.T, s *database.Store, id int64) Job {
	t.Helper()
	var j Job
	if err := database.Get(context.Background(), s.Read(), "jobs", id, &j); err != nil {
		t.Fatal(err)
	}
	return j
}

func waitState(t *testing.T, s *database.Store, id int64, want string) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if j := getJob(t, s, id); j.State == want {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	j := getJob(t, s, id)
	t.Fatalf("job %d: state %s, want %s", id, j.State, want)
	return j
}

func TestDedupe(t *testing.T) {
	s := newStore(t, t.TempDir())
	defer s.Close()
	q := New(s, fastOpts())
	ctx := context.Background()
	a, created, err := q.Enqueue(ctx, "search_slot", &database.Subject{Type: "slot", ID: 1}, nil, "search_slot:1", me)
	if err != nil || !created {
		t.Fatalf("first enqueue: %v %v", created, err)
	}
	b, created, err := q.Enqueue(ctx, "search_slot", &database.Subject{Type: "slot", ID: 1}, nil, "search_slot:1", me)
	if err != nil || created || b != a {
		t.Fatalf("second enqueue should return job %d unchanged, got %d %v %v", a, b, created, err)
	}
	c, created, _ := q.Enqueue(ctx, "search_slot", nil, nil, "search_slot:2", me)
	if !created || c == a {
		t.Fatal("a different key should make a new job")
	}
	// Once the first job is finished the key is free again.
	q.Handle("search_slot", func(context.Context, *Run) error { return nil })
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer q.Stop()
	waitState(t, s, a, "done")
	d, created, _ := q.Enqueue(ctx, "search_slot", nil, nil, "search_slot:1", me)
	if !created || d == a {
		t.Fatal("key should be free after the job is done")
	}
}

func TestRetryWithBackoff(t *testing.T) {
	s := newStore(t, t.TempDir())
	defer s.Close()
	q := New(s, fastOpts())
	var calls atomic.Int32
	var times []time.Time
	var mu sync.Mutex
	q.Handle("flaky", func(context.Context, *Run) error {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		if calls.Add(1) < 3 {
			return errors.New("not yet")
		}
		return nil
	})
	ctx := context.Background()
	id, _, _ := q.Enqueue(ctx, "flaky", nil, nil, "", me)
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer q.Stop()
	j := waitState(t, s, id, "done")
	if j.Attempt != 3 || calls.Load() != 3 {
		t.Fatalf("attempts %d, calls %d, want 3", j.Attempt, calls.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if gap1, gap2 := times[1].Sub(times[0]), times[2].Sub(times[1]); gap1 < 5*time.Millisecond || gap2 < 10*time.Millisecond {
		t.Fatalf("back-off too short: %v then %v", gap1, gap2)
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	s := newStore(t, t.TempDir())
	defer s.Close()
	o := fastOpts()
	o.MaxAttempts = 2
	q := New(s, o)
	q.Handle("broken", func(context.Context, *Run) error { return errors.New("always") })
	ctx := context.Background()
	id, _, _ := q.Enqueue(ctx, "broken", nil, nil, "", me)
	_ = q.Start(ctx)
	defer q.Stop()
	j := waitState(t, s, id, "failed")
	if j.Attempt != 2 || j.Error == nil || j.Error.Message != "always" || j.EndedAt == nil {
		t.Fatalf("failed job wrong: %+v", j)
	}
}

func TestUnknownKindFails(t *testing.T) {
	s := newStore(t, t.TempDir())
	defer s.Close()
	o := fastOpts()
	o.MaxAttempts = 1
	q := New(s, o)
	ctx := context.Background()
	id, _, _ := q.Enqueue(ctx, "nobody_handles_this", nil, nil, "", me)
	_ = q.Start(ctx)
	defer q.Stop()
	j := waitState(t, s, id, "failed")
	if j.Error == nil || j.Error.Code != "no_handler" {
		t.Fatalf("want no_handler, got %+v", j.Error)
	}
}

func TestCrashRecovery(t *testing.T) {
	dir := t.TempDir()
	s := newStore(t, dir)
	q := New(s, fastOpts())
	ctx := context.Background()
	id, _, _ := q.Enqueue(ctx, "scan_folder", nil, nil, "scan:1", me)
	// Simulate a crash mid-job: the row says running, the process is gone.
	err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE jobs SET state = 'running', attempt = 1, started_at = ? WHERE id = ?", database.FormatTime(time.Now()), id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	s = newStore(t, dir)
	defer s.Close()
	q = New(s, fastOpts())
	var ran atomic.Bool
	q.Handle("scan_folder", func(context.Context, *Run) error { ran.Store(true); return nil })
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer q.Stop()
	j := waitState(t, s, id, "done")
	if !ran.Load() || j.Attempt != 2 {
		t.Fatalf("recovered job: ran %v attempt %d", ran.Load(), j.Attempt)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	s := newStore(t, t.TempDir())
	defer s.Close()
	o := fastOpts()
	o.Workers = 3
	q := New(s, o)
	var now, peak atomic.Int32
	q.Handle("work", func(context.Context, *Run) error {
		n := now.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		now.Add(-1)
		return nil
	})
	ctx := context.Background()
	var ids []int64
	for range 9 {
		id, _, _ := q.Enqueue(ctx, "work", nil, nil, "", me)
		ids = append(ids, id)
	}
	_ = q.Start(ctx)
	defer q.Stop()
	for _, id := range ids {
		waitState(t, s, id, "done")
	}
	if peak.Load() != 3 {
		t.Fatalf("peak concurrency %d, want 3", peak.Load())
	}
}

func TestDefaultWorkers(t *testing.T) {
	if New(nil, Options{}).opt.Workers != 2 {
		t.Fatal("default worker count should be 2")
	}
}

func TestProgressAndResult(t *testing.T) {
	s := newStore(t, t.TempDir())
	defer s.Close()
	q := New(s, fastOpts())
	seen := make(chan float64, 10)
	q.opt.OnChange = func(j Job) {
		if j.State == "running" && j.Progress > 0 {
			seen <- j.Progress
		}
	}
	q.Handle("steps", func(ctx context.Context, r *Run) error {
		if err := r.Progress(ctx, 0.5, "half way"); err != nil {
			return err
		}
		r.SetResult(map[string]any{"found": 3.0})
		return nil
	})
	ctx := context.Background()
	id, _, _ := q.Enqueue(ctx, "steps", nil, map[string]any{"a": 1.0}, "", me)
	_ = q.Start(ctx)
	defer q.Stop()
	j := waitState(t, s, id, "done")
	if j.Progress != 1 || j.Result["found"] != 3.0 || j.ProgressNote == nil || *j.ProgressNote != "half way" {
		t.Fatalf("job after run: %+v", j)
	}
	select {
	case p := <-seen:
		if p != 0.5 {
			t.Fatalf("progress %v", p)
		}
	case <-time.After(time.Second):
		t.Fatal("no progress change seen")
	}
}

func TestCancel(t *testing.T) {
	s := newStore(t, t.TempDir())
	defer s.Close()
	q := New(s, fastOpts())
	started := make(chan struct{})
	q.Handle("long", func(ctx context.Context, _ *Run) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	ctx := context.Background()
	queued, _, _ := q.Enqueue(ctx, "later", nil, nil, "", me)
	if err := q.Cancel(ctx, queued); err != nil {
		t.Fatal(err)
	}
	if j := getJob(t, s, queued); j.State != "cancelled" {
		t.Fatalf("queued job state %s", j.State)
	}
	running, _, _ := q.Enqueue(ctx, "long", nil, nil, "", me)
	_ = q.Start(ctx)
	defer q.Stop()
	<-started
	if err := q.Cancel(ctx, running); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, running, "cancelled")
	if err := q.Cancel(ctx, running); err == nil {
		t.Fatal("cancelling a finished job should fail")
	}
}

func TestIdlePollingWritesNothing(t *testing.T) {
	s := newStore(t, t.TempDir())
	defer s.Close()
	q := New(s, fastOpts())
	if err := q.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer q.Stop()
	time.Sleep(20 * time.Millisecond)
	before := s.WriteCount()
	time.Sleep(200 * time.Millisecond) // about 40 polls on each worker
	if n := s.WriteCount() - before; n != 0 {
		t.Fatalf("idle queue made %d write transactions", n)
	}
}

// 4.15 review: a result saved while the Job runs is kept when it is
// cancelled, and a recovered Job sees it.
func TestSaveResultKeptOnCancel(t *testing.T) {
	s := newStore(t, t.TempDir())
	defer s.Close()
	q := New(s, fastOpts())
	saved := make(chan struct{})
	q.Handle("scan", func(ctx context.Context, r *Run) error {
		if err := r.SaveResult(ctx, map[string]any{"part": 1}); err != nil {
			return err
		}
		close(saved)
		<-ctx.Done()
		return ctx.Err()
	})
	ctx := context.Background()
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer q.Stop()
	id, _, _ := q.Enqueue(ctx, "scan", nil, nil, "", me)
	<-saved
	if j := getJob(t, s, id); j.State != "running" || j.Result["part"] != float64(1) {
		t.Fatalf("while running: %s %v", j.State, j.Result)
	}
	if err := q.Cancel(ctx, id); err != nil {
		t.Fatal(err)
	}
	if j := waitState(t, s, id, "cancelled"); j.Result["part"] != float64(1) {
		t.Fatalf("after cancel: %v", j.Result)
	}
}

func TestCancelQueuedNotifiesAndOrphanedRunningCancels(t *testing.T) {
	s := newStore(t, t.TempDir())
	defer s.Close()
	o := fastOpts()
	var events []Job
	var mu sync.Mutex
	o.OnChange = func(j Job) {
		mu.Lock()
		events = append(events, j)
		mu.Unlock()
	}
	q := New(s, o)
	ctx := context.Background()

	// Queued cancel must emit an OnChange notification with state=cancelled.
	queuedID, _, err := q.Enqueue(ctx, "later", nil, nil, "", me)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Cancel(ctx, queuedID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	foundQueuedCancel := false
	for _, ev := range events {
		if ev.ID == queuedID && ev.State == "cancelled" {
			foundQueuedCancel = true
		}
	}
	mu.Unlock()
	if !foundQueuedCancel {
		t.Fatal("queued cancel did not notify OnChange")
	}

	// Orphaned running job (DB says running, no live worker) must cancel cleanly.
	orphanID, _, err := q.Enqueue(ctx, "stuck", nil, nil, "", me)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE jobs SET state = 'running', started_at = ? WHERE id = ?", database.FormatTime(time.Now()), orphanID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.Cancel(ctx, orphanID); err != nil {
		t.Fatalf("cancel orphaned running job: %v", err)
	}
	if j := getJob(t, s, orphanID); j.State != "cancelled" || j.EndedAt == nil {
		t.Fatalf("orphaned job after cancel: %+v", j)
	}
	mu.Lock()
	foundOrphanCancel := false
	for _, ev := range events {
		if ev.ID == orphanID && ev.State == "cancelled" {
			foundOrphanCancel = true
		}
	}
	mu.Unlock()
	if !foundOrphanCancel {
		t.Fatal("orphaned cancel did not notify OnChange")
	}

	// Handler returning nil after user cancel still records state=cancelled.
	started := make(chan struct{})
	q.Handle("nil_on_cancel", func(ctx context.Context, _ *Run) error {
		close(started)
		<-ctx.Done()
		return nil
	})
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer q.Stop()
	nilCancelID, _, _ := q.Enqueue(ctx, "nil_on_cancel", nil, nil, "", me)
	<-started
	if err := q.Cancel(ctx, nilCancelID); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, nilCancelID, "cancelled")
}

func TestRetryJobStatesAndDedupe(t *testing.T) {
	s := newStore(t, t.TempDir())
	defer s.Close()
	o := fastOpts()
	o.MaxAttempts = 1
	var notified []Job
	var mu sync.Mutex
	o.OnChange = func(j Job) {
		mu.Lock()
		notified = append(notified, j)
		mu.Unlock()
	}
	q := New(s, o)
	var succeed atomic.Bool
	q.Handle("flaky_once", func(ctx context.Context, _ *Run) error {
		if !succeed.Load() {
			return errors.New("boom")
		}
		return nil
	})
	started := make(chan struct{})
	release := make(chan struct{})
	q.Handle("hold", func(ctx context.Context, _ *Run) error {
		close(started)
		<-release
		return nil
	})

	ctx := context.Background()
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer q.Stop()

	// Failed job retry.
	failedID, _, err := q.Enqueue(ctx, "flaky_once", nil, nil, "dedupe:1", me)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, s, failedID, "failed")
	succeed.Store(true)
	retried, err := q.Retry(ctx, failedID)
	if err != nil {
		t.Fatalf("retry failed job: %v", err)
	}
	if retried.State != "queued" || retried.Attempt != 0 || retried.Error != nil || retried.EndedAt != nil {
		t.Fatalf("retried job fields wrong: %+v", retried)
	}
	waitState(t, s, failedID, "done")

	// Retrying a done job must return ErrCannotRetry.
	if _, err := q.Retry(ctx, failedID); !errors.Is(err, ErrCannotRetry) {
		t.Fatalf("retry done job: got %v, want ErrCannotRetry", err)
	}

	// Cancelled job retry and dedupe conflict.
	cancelledID, _, _ := q.Enqueue(ctx, "flaky_once", nil, nil, "dedupe:2", me)
	if err := q.Cancel(ctx, cancelledID); err != nil {
		t.Fatal(err)
	}
	// Enqueue another live job with same dedupe key.
	blockerID, _, _ := q.Enqueue(ctx, "hold", nil, nil, "dedupe:2", me)
	<-started
	if _, err := q.Retry(ctx, blockerID); !errors.Is(err, ErrCannotRetry) {
		t.Fatalf("retry running job: got %v, want ErrCannotRetry", err)
	}
	if _, err := q.Retry(ctx, cancelledID); !errors.Is(err, ErrAlreadyQueued) {
		t.Fatalf("retry dedupe conflict: got %v, want ErrAlreadyQueued", err)
	}
	close(release)
	waitState(t, s, blockerID, "done")

	// Now dedupe key is free; retry cancelled job succeeds.
	if _, err := q.Retry(ctx, cancelledID); err != nil {
		t.Fatalf("retry cancelled job after blocker done: %v", err)
	}
	waitState(t, s, cancelledID, "done")
}

func TestPromoteDropQueuedAndListQueued(t *testing.T) {
	s := newStore(t, t.TempDir())
	defer s.Close()
	o := fastOpts()
	o.Workers = 1
	var events []Job
	var evMu sync.Mutex
	o.OnChange = func(j Job) {
		evMu.Lock()
		events = append(events, j)
		evMu.Unlock()
	}
	q := New(s, o)
	ctx := context.Background()

	// Missing job returns ErrNotFound.
	if _, err := q.Promote(ctx, 99999); !errors.Is(err, database.ErrNotFound) {
		t.Fatalf("Promote missing: got %v, want ErrNotFound", err)
	}
	if _, err := q.DropQueued(ctx, 99999); !errors.Is(err, database.ErrNotFound) {
		t.Fatalf("DropQueued missing: got %v, want ErrNotFound", err)
	}

	id1, _, err := q.Enqueue(ctx, "work", nil, map[string]any{"n": 1}, "k:1", me)
	if err != nil {
		t.Fatal(err)
	}
	id2, _, err := q.Enqueue(ctx, "work", nil, map[string]any{"n": 2}, "k:2", me)
	if err != nil {
		t.Fatal(err)
	}
	id3, _, err := q.Enqueue(ctx, "work", nil, map[string]any{"n": 3}, "k:3", me)
	if err != nil {
		t.Fatal(err)
	}
	id4, _, err := q.Enqueue(ctx, "work", nil, map[string]any{"n": 4}, "k:4", me)
	if err != nil {
		t.Fatal(err)
	}

	// Promote id2 first, then promote id3 above id2.
	p2, err := q.Promote(ctx, id2)
	if err != nil {
		t.Fatalf("Promote id2: %v", err)
	}
	if p2.Priority != "now" || p2.RunAfter.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("unexpected promoted job p2: %+v", p2)
	}

	p3, err := q.Promote(ctx, id3)
	if err != nil {
		t.Fatalf("Promote id3: %v", err)
	}
	if p3.Priority != "now" || !p3.RunAfter.Before(p2.RunAfter) {
		t.Fatalf("expected p3 (%v) strictly before p2 (%v)", p3.RunAfter, p2.RunAfter)
	}

	// ListQueued must return [id3, id2, id1, id4] with pagination.
	queued, total, err := ListQueued(ctx, s.Read(), []string{"work"}, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || len(queued) != 2 || queued[0].ID != id3 || queued[1].ID != id2 {
		t.Fatalf("ListQueued page 0: total=%d got=%v want=[%d,%d]", total, queued, id3, id2)
	}
	queued2, total2, err := ListQueued(ctx, s.Read(), []string{"work"}, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if total2 != 4 || len(queued2) != 2 || queued2[0].ID != id1 || queued2[1].ID != id4 {
		t.Fatalf("ListQueued page 1: total=%d got=%v want=[%d,%d]", total2, queued2, id1, id4)
	}

	// ListActive must follow the same priority order for queued jobs.
	active, err := ListActive(ctx, s.Read(), []string{"work"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 4 || active[0].ID != id3 || active[1].ID != id2 || active[2].ID != id1 || active[3].ID != id4 {
		t.Fatalf("ListActive order wrong: got %v", active)
	}

	// DropQueued removes id4 cleanly and emits a cancelled notification.
	dropped, err := q.DropQueued(ctx, id4)
	if err != nil {
		t.Fatalf("DropQueued id4: %v", err)
	}
	if dropped.ID != id4 || dropped.State != "cancelled" {
		t.Fatalf("unexpected dropped job: %+v", dropped)
	}
	var chk Job
	if err := database.Get(ctx, s.Read(), "jobs", id4, &chk); !errors.Is(err, database.ErrNotFound) {
		t.Fatalf("expected dropped job deleted from DB, got %v", err)
	}
	evMu.Lock()
	sawDropEvent := false
	for _, ev := range events {
		if ev.ID == id4 && ev.State == "cancelled" {
			sawDropEvent = true
		}
	}
	evMu.Unlock()
	if !sawDropEvent {
		t.Fatal("DropQueued did not emit cancelled notification")
	}

	// Start single worker and verify execution order: id3, then id2, then id1.
	var order []int64
	var ordMu sync.Mutex
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var firstOnce sync.Once
	q.Handle("work", func(ctx context.Context, r *Run) error {
		ordMu.Lock()
		order = append(order, r.Job.ID)
		ordMu.Unlock()
		firstOnce.Do(func() {
			close(firstStarted)
			<-releaseFirst
		})
		return nil
	})
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer q.Stop()

	<-firstStarted
	// While id3 is running: ListRunning returns [id3], Promote/DropQueued on running id3 fail with ErrNotQueued.
	running, err := ListRunning(ctx, s.Read(), []string{"work"}, 10)
	if err != nil || len(running) != 1 || running[0].ID != id3 {
		t.Fatalf("ListRunning while id3 active: %+v err=%v", running, err)
	}
	if _, err := q.Promote(ctx, id3); !errors.Is(err, ErrNotQueued) {
		t.Fatalf("Promote running job: got %v, want ErrNotQueued", err)
	}
	if _, err := q.DropQueued(ctx, id3); !errors.Is(err, ErrNotQueued) {
		t.Fatalf("DropQueued running job: got %v, want ErrNotQueued", err)
	}
	close(releaseFirst)

	waitState(t, s, id3, "done")
	waitState(t, s, id2, "done")
	waitState(t, s, id1, "done")

	ordMu.Lock()
	defer ordMu.Unlock()
	if len(order) != 3 || order[0] != id3 || order[1] != id2 || order[2] != id1 {
		t.Fatalf("unexpected execution order: got %v, want [%d, %d, %d]", order, id3, id2, id1)
	}
}

func TestBurstWakeAcrossWorkersAndPools(t *testing.T) {
	s := newStore(t, t.TempDir())
	defer s.Close()
	o := fastOpts()
	o.PollInterval = time.Hour // verify wake channel, not poll interval, wakes multiple workers
	o.Pools = map[Pool]int{PoolIO: 2, PoolTranscribe: 2}
	q := New(s, o)

	bothStarted := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	closeRelease := func() { releaseOnce.Do(func() { close(release) }) }
	var started atomic.Int32
	q.Handle("subtitle_transcribe", func(ctx context.Context, _ *Run) error {
		if started.Add(1) == 2 {
			close(bothStarted)
		}
		select {
		case <-bothStarted:
		case <-ctx.Done():
			return ctx.Err()
		}
		<-release
		return nil
	})

	ctx := context.Background()
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer q.Stop()
	defer closeRelease()
	time.Sleep(20 * time.Millisecond) // let all workers settle on select

	id1, _, err := q.Enqueue(ctx, "subtitle_transcribe", nil, nil, "t:1", me)
	if err != nil {
		t.Fatal(err)
	}
	id2, _, err := q.Enqueue(ctx, "subtitle_transcribe", nil, nil, "t:2", me)
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-bothStarted:
	case <-time.After(2 * time.Second):
		t.Fatalf("only %d transcribe worker(s) woke up on burst enqueue", started.Load())
	}
	closeRelease()
	waitState(t, s, id1, "done")
	waitState(t, s, id2, "done")
}
