package subtitles

import (
	"context"
	"sync"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
)

type progressKey struct{}

// withProgress gives the work under ctx a listener for how far it is, from 0
// to 1. The listener may be called from several goroutines; calls reach it
// one at a time.
func withProgress(ctx context.Context, fn func(p float64)) context.Context {
	var mu sync.Mutex
	return context.WithValue(ctx, progressKey{}, func(p float64) {
		mu.Lock()
		defer mu.Unlock()
		fn(p)
	})
}

// tellProgress tells the listener under ctx, when there is one, how far the
// work is.
func tellProgress(ctx context.Context, p float64) {
	if fn, ok := ctx.Value(progressKey{}).(func(float64)); ok {
		fn(min(max(p, 0), 1))
	}
}

// jobProgress makes a Job the listener: its progress is what the Subtitles
// page and Status show while it runs. Only a step of a whole percent is
// written, so a long file does not write on every line.
func jobProgress(ctx context.Context, r *jobs.Run) context.Context {
	if r.Job.ID == 0 {
		return ctx // a Run made by hand has no stored Job to write to
	}
	last := -1
	return withProgress(ctx, func(p float64) {
		if n := int(p * 100); n > last {
			last = n
			_ = r.Progress(ctx, p, "")
		}
	})
}
