package transcode

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
)

const pausedKey = "transcode_paused"

var pausedKinds = []string{KindFile}

func isPausedKind(kind string) bool {
	for _, k := range pausedKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// Paused reports whether queued transcode_file jobs are held in the queue.
func (s *Service) Paused() bool { return s.paused.Load() }

// LoadPaused restores the persisted pause state at startup.
func (s *Service) LoadPaused(ctx context.Context) {
	on, _ := strconv.ParseBool(database.SettingOr(ctx, s.Store.Read(), settingsGroup, pausedKey, "false"))
	s.paused.Store(on)
}

// SetPaused holds transcode_file jobs in the queue or lets them run again.
func (s *Service) SetPaused(ctx context.Context, on bool, by database.Actor) error {
	err := s.Store.Write(ctx, func(tx *sql.Tx) error {
		return database.PutSetting(tx, settingsGroup, pausedKey, strconv.FormatBool(on), by, time.Now().UTC())
	})
	if err != nil {
		return err
	}
	s.paused.Store(on)
	if !on {
		s.notifyConcurrency()
	}
	return nil
}

type pauseGate struct {
	inner jobs.Gate
	s     *Service
}

// PauseGate wraps an existing jobs.Gate so paused transcode_file jobs wait.
func PauseGate(inner jobs.Gate, s *Service) jobs.Gate {
	return pauseGate{inner: inner, s: s}
}

func (g pauseGate) Active() bool {
	return g.s.Paused() || (g.inner != nil && g.inner.Active())
}

func (g pauseGate) Held(ctx context.Context, db database.Querier, kind string, subject *database.Subject) bool {
	if g.s.Paused() && isPausedKind(kind) {
		return true
	}
	return g.inner != nil && g.inner.Active() && g.inner.Held(ctx, db, kind, subject)
}

func (g pauseGate) KindOff(kind string) bool {
	if g.s.Paused() && isPausedKind(kind) {
		return true
	}
	return g.inner != nil && g.inner.Active() && g.inner.KindOff(kind)
}

func (g pauseGate) OffKinds() []string {
	var out []string
	if in, ok := g.inner.(jobs.OffKinds); ok && g.inner.Active() {
		out = append(out, in.OffKinds()...)
	}
	if g.s.Paused() {
		out = append(out, pausedKinds...)
	}
	return out
}
