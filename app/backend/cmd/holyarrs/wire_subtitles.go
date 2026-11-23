package main

import (
	"context"
	"log"
	"path/filepath"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/builtin"
)

// newSubtitles gives the subtitles service its models folder and the
// built-in translator's model runners. A runner is started only while a
// translation runs; the caller stops them with the program. It also puts the
// subtitle pause on the queue's gate, in front of the parts gate, and reads
// back a pause set before the restart.
func newSubtitles(ctx context.Context, subs *subtitles.Service, configDir string, q *jobs.Queue, parts jobs.Gate) *subtitles.Service {
	subs.ModelsDir = filepath.Join(configDir, "models")
	subs.Runners = builtin.NewPool(func(line string) { log.Print(line) })
	subs.Jobs = q
	subs.LoadPaused(ctx)
	q.SetGate(subtitles.PauseGate(parts, subs))
	return subs
}

// speechPools is the job queue's worker counts with a ceiling large enough for
// every graphics card's transcript slots, while speechPlaces.take gates active
// concurrency from live settings and VRAM.
func speechPools(ctx context.Context, st *database.Store) map[jobs.Pool]int {
	pools := map[jobs.Pool]int{}
	for k, v := range jobs.DefaultPools {
		pools[k] = v
	}
	pools[jobs.PoolTranscribe] = subtitles.SpeechPoolWorkers(ctx, st)
	pools[jobs.PoolTranslate] = subtitles.TranslatePoolWorkers(ctx, st)
	return pools
}
