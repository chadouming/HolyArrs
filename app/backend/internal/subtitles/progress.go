package subtitles

import (
	"context"
	"sync"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
)

var (
	progressMinInterval = 2 * time.Second
	progressNow         = time.Now
)

type progressKey struct{}
type jobIDKey struct{}

type progressFn func(p float64, note string)

// withJobID attaches a job ID to ctx for live speed reporting.
func withJobID(ctx context.Context, id int64) context.Context {
	if id == 0 {
		return ctx
	}
	return context.WithValue(ctx, jobIDKey{}, id)
}

func jobIDFrom(ctx context.Context) int64 {
	id, _ := ctx.Value(jobIDKey{}).(int64)
	return id
}

// withProgress gives the work under ctx a listener for how far it is, from 0
// to 1. The listener may be called from several goroutines; calls reach it
// one at a time.
func withProgress(ctx context.Context, fn func(p float64)) context.Context {
	return withProgressNote(ctx, func(p float64, _ string) {
		fn(p)
	})
}

func withProgressNote(ctx context.Context, fn func(p float64, note string)) context.Context {
	var mu sync.Mutex
	return context.WithValue(ctx, progressKey{}, progressFn(func(p float64, note string) {
		mu.Lock()
		defer mu.Unlock()
		fn(p, note)
	}))
}

// tellProgress tells the listener under ctx, when there is one, how far the
// work is.
func tellProgress(ctx context.Context, p float64) {
	tellProgressNote(ctx, p, "")
}

// tellProgressNote tells the listener under ctx how far the work is and an
// optional short phase note.
func tellProgressNote(ctx context.Context, p float64, note string) {
	if fn, ok := ctx.Value(progressKey{}).(progressFn); ok {
		fn(min(max(p, 0), 1), note)
	}
}

// jobProgress makes a Job the listener: its progress is what the Subtitles
// page and Status show while it runs. Only a step of a whole percent or a
// changed phase note is written, so a long file does not write on every line.
func jobProgress(ctx context.Context, r *jobs.Run) context.Context {
	if r.Job.ID == 0 {
		return ctx // a Run made by hand has no stored Job to write to
	}
	var (
		mu        sync.Mutex
		last      = -1
		lastNote  string
		lastWrite time.Time
	)
	return withProgressNote(ctx, func(p float64, note string) {
		mu.Lock()
		defer mu.Unlock()
		if note == "" {
			note = lastNote
		}
		n := int(p * 100)
		first := last < 0
		noteChanged := note != lastNote
		done := p >= 1 && (n > last || first || noteChanged)
		pctIncreased := n > last
		if !first && !noteChanged && !done && !pctIncreased {
			return
		}
		now := progressNow()
		if !first && !noteChanged && !done && progressMinInterval > 0 && now.Sub(lastWrite) < progressMinInterval {
			return
		}
		if n > last {
			last = n
		}
		lastNote = note
		lastWrite = now
		_ = r.Progress(ctx, p, note)
	})
}

