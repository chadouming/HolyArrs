package subtitles

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
)

func insertTestJob(t *testing.T, st *database.Store, kind, state, dedupe string, subj *domain.Subject) int64 {
	t.Helper()
	ts := database.FormatTime(now())
	var subjJSON any
	if subj != nil {
		b, _ := json.Marshal(subj)
		subjJSON = string(b)
	}
	var dedupeArg any
	if dedupe != "" {
		dedupeArg = dedupe
	}
	var id int64
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		r, err := tx.Exec(`INSERT INTO jobs (created_at, created_by, updated_at, updated_by, kind, subject, params, dedupe_key, state, run_after, asked_by)
			VALUES (?, ?, ?, ?, ?, ?, '{"language":"fr"}', ?, ?, ?, ?)`,
			ts, sysActor, ts, sysActor, kind, subjJSON, dedupeArg, state, ts, sysActor)
		if err != nil {
			return err
		}
		id, _ = r.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func jobState(t *testing.T, st *database.Store, id int64) string {
	t.Helper()
	var j jobs.Job
	if err := database.Get(context.Background(), st.Read(), "jobs", id, &j); err != nil {
		t.Fatal(err)
	}
	return j.State
}

// route: GET /api/v1/subtitles/failed
func TestRoutes_FailedSubtitles(t *testing.T) {
	f := newFix(t)
	s := &Service{Store: f.st}
	m := http.NewServeMux()
	NewHandlers(f.st, nil, s).Routes(m)

	subj := &domain.Subject{Type: "file", ID: f.movieFile}
	insertTestJob(t, f.st, KindSearch, "failed", "", subj)
	insertTestJob(t, f.st, KindTranscribe, "failed", "", subj)
	insertTestJob(t, f.st, KindTranslate, "cancelled", "", subj)
	insertTestJob(t, f.st, KindSearch, "queued", "", subj)
	insertTestJob(t, f.st, "library_scan", "failed", "", nil)

	check := func(path string, wantTotal, wantFailed, wantCancelled int) {
		t.Helper()
		code, body := call(m, "GET", path, "")
		if code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, code, body)
		}
		var out struct {
			Jobs           []OverviewJob `json:"jobs"`
			Total          int           `json:"total"`
			FailedCount    int           `json:"failed_count"`
			CancelledCount int           `json:"cancelled_count"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatalf("GET %s unmarshal: %v", path, err)
		}
		if out.Total != wantTotal || len(out.Jobs) != wantTotal || out.FailedCount != wantFailed || out.CancelledCount != wantCancelled {
			t.Fatalf("GET %s: got total=%d len=%d failed=%d cancelled=%d", path, out.Total, len(out.Jobs), out.FailedCount, out.CancelledCount)
		}
	}

	check("/api/v1/subtitles/failed", 3, 2, 1)
	check("/api/v1/subtitles/failed?state=all", 3, 2, 1)
	check("/api/v1/subtitles/failed?state=failed", 2, 2, 1)
	check("/api/v1/subtitles/failed?state=cancelled", 1, 2, 1)

	if code, _ := call(m, "GET", "/api/v1/subtitles/failed?state=unknown", ""); code != http.StatusBadRequest {
		t.Fatalf("bad state: %d, want 400", code)
	}
}

// route: POST /api/v1/subtitles/jobs/{id}/stop
func TestRoutes_StopSubtitleJob(t *testing.T) {
	f := newFix(t)
	q := jobs.New(f.st, jobs.Options{Workers: 1, PollInterval: 10 * time.Millisecond})
	started := make(chan struct{})
	q.Handle(KindSearch, func(ctx context.Context, _ *jobs.Run) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	s := &Service{Store: f.st, Jobs: q}
	m := http.NewServeMux()
	NewHandlers(f.st, nil, s).Routes(m)

	// Queued subtitle job stops immediately.
	queuedID := insertTestJob(t, f.st, KindTranslate, "queued", "", &domain.Subject{Type: "file", ID: f.movieFile})
	code, body := call(m, "POST", "/api/v1/subtitles/jobs/"+itoa(queuedID)+"/stop", "")
	if code != http.StatusOK || jobState(t, f.st, queuedID) != "cancelled" {
		t.Fatalf("stop queued: %d %s (state=%s)", code, body, jobState(t, f.st, queuedID))
	}

	// Orphaned running subtitle job (no live worker) cancels cleanly.
	orphanID := insertTestJob(t, f.st, KindTranscribe, "running", "", &domain.Subject{Type: "file", ID: f.ep1File})
	code, body = call(m, "POST", "/api/v1/subtitles/jobs/"+itoa(orphanID)+"/stop", "")
	if code != http.StatusOK || jobState(t, f.st, orphanID) != "cancelled" {
		t.Fatalf("stop orphan: %d %s (state=%s)", code, body, jobState(t, f.st, orphanID))
	}

	// Non-subtitle job returns 404 and stays untouched.
	otherID := insertTestJob(t, f.st, "library_scan", "queued", "", nil)
	if code, _ := call(m, "POST", "/api/v1/subtitles/jobs/"+itoa(otherID)+"/stop", ""); code != http.StatusNotFound {
		t.Fatalf("stop non-subtitle job: %d, want 404", code)
	}
	if jobState(t, f.st, otherID) != "queued" {
		t.Fatalf("non-subtitle job state changed to %s", jobState(t, f.st, otherID))
	}
	if code, _ := call(m, "POST", "/api/v1/subtitles/jobs/999999/stop", ""); code != http.StatusNotFound {
		t.Fatalf("stop missing job: %d, want 404", code)
	}

	// Running subtitle job cancels its worker context and moves to cancelled.
	ctx := context.Background()
	runID, _, err := q.Enqueue(ctx, KindSearch, &domain.Subject{Type: "file", ID: f.ep2File}, nil, "", domain.System)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer q.Stop()
	<-started
	code, body = call(m, "POST", "/api/v1/subtitles/jobs/"+itoa(runID)+"/stop", "")
	if code != http.StatusOK {
		t.Fatalf("stop running: %d %s", code, body)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if jobState(t, f.st, runID) == "cancelled" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if st := jobState(t, f.st, runID); st != "cancelled" {
		t.Fatalf("running job did not become cancelled, got %s", st)
	}
}

// route: POST /api/v1/subtitles/jobs/{id}/retry
// route: POST /api/v1/subtitles/jobs/retry-failed
func TestRoutes_RetrySubtitleJobs(t *testing.T) {
	f := newFix(t)
	q := jobs.New(f.st, jobs.Options{Workers: 1})
	s := &Service{Store: f.st, Jobs: q}
	m := http.NewServeMux()
	NewHandlers(f.st, nil, s).Routes(m)
	ctx := context.Background()

	// Non-subtitle job returns 404.
	otherID := insertTestJob(t, f.st, "library_scan", "failed", "", nil)
	if code, _ := call(m, "POST", "/api/v1/subtitles/jobs/"+itoa(otherID)+"/retry", ""); code != http.StatusNotFound {
		t.Fatalf("retry non-subtitle job: %d, want 404", code)
	}

	// Failed transcribe job retries and clears its transcript hold.
	s.noteTranscript(ctx, f.movieFile, true, "no speech was found")
	if !s.transcriptHeld(ctx, f.movieFile) {
		t.Fatal("expected transcript to be held before retry")
	}
	transcribeID := insertTestJob(t, f.st, KindTranscribe, "failed", "transcribe:"+itoa(f.movieFile), &domain.Subject{Type: "file", ID: f.movieFile})
	code, body := call(m, "POST", "/api/v1/subtitles/jobs/"+itoa(transcribeID)+"/retry", "")
	if code != http.StatusOK || jobState(t, f.st, transcribeID) != "queued" {
		t.Fatalf("retry single: %d %s (state=%s)", code, body, jobState(t, f.st, transcribeID))
	}
	if s.transcriptHeld(ctx, f.movieFile) {
		t.Fatal("retry did not clear transcript hold")
	}

	// retry-failed retries eligible failed jobs, skips dedupe conflicts, and ignores cancelled jobs.
	s.noteTranscript(ctx, f.ep1File, false, "temporary failure")
	failedOK := insertTestJob(t, f.st, KindTranscribe, "failed", "transcribe:"+itoa(f.ep1File), &domain.Subject{Type: "file", ID: f.ep1File})
	failedConflict := insertTestJob(t, f.st, KindSearch, "failed", "search:dup", &domain.Subject{Type: "file", ID: f.ep2File})
	insertTestJob(t, f.st, KindSearch, "queued", "search:dup", &domain.Subject{Type: "file", ID: f.ep2File})
	cancelledID := insertTestJob(t, f.st, KindSearch, "cancelled", "", &domain.Subject{Type: "file", ID: f.ep2File})

	code, body = call(m, "POST", "/api/v1/subtitles/jobs/retry-failed", "")
	if code != http.StatusOK {
		t.Fatalf("retry-failed: %d %s", code, body)
	}
	var out struct {
		Retried int `json:"retried"`
		Skipped int `json:"skipped"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.Retried != 1 || out.Skipped != 1 {
		t.Fatalf("retry-failed counts = %+v, want retried=1 skipped=1", out)
	}
	if jobState(t, f.st, failedOK) != "queued" || s.transcriptHeld(ctx, f.ep1File) {
		t.Fatalf("failedOK state=%s held=%v", jobState(t, f.st, failedOK), s.transcriptHeld(ctx, f.ep1File))
	}
	if jobState(t, f.st, failedConflict) != "failed" {
		t.Fatalf("failedConflict state=%s, want failed", jobState(t, f.st, failedConflict))
	}
	if jobState(t, f.st, cancelledID) != "cancelled" {
		t.Fatalf("cancelledID state=%s, want cancelled", jobState(t, f.st, cancelledID))
	}
}
