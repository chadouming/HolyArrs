package translate

import (
	"context"
	"sync/atomic"
	"testing"
)

// A run tells how many lines are translated after every call, so a job can
// show how far it is. In parts the calls come from several goroutines and
// still add up to the whole file.
func TestRunTellsItsProgress(t *testing.T) {
	for _, parts := range []int{1, 4} {
		var total atomic.Int64
		var calls atomic.Int64
		j := jobOf(400, 20)
		j.Progress = func(n int) { total.Add(int64(n)); calls.Add(1) }
		res, err := RunParts(context.Background(), &slowClient{}, j, parts)
		if err != nil {
			t.Fatal(err)
		}
		if total.Load() != int64(res.Translated) || res.Translated != 400 || calls.Load() != 20 {
			t.Fatalf("parts %d: told %d lines in %d calls, translated %d", parts, total.Load(), calls.Load(), res.Translated)
		}
	}
}
