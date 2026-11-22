package subtitles

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/webserver"
)

// pausedKey holds the pause in the settings, so it lasts a restart.
const pausedKey = "subtitle_paused"

// pausedKinds is the subtitle work a pause holds in the queue: searches,
// translations and subtitles from the audio. Reading what files have and
// model downloads go on.
var pausedKinds = []string{KindSearch, KindMissing, KindBetter, KindTranslate, KindTranscribe}

func pausedKind(kind string) bool {
	for _, k := range pausedKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// Paused reports whether subtitle work is held in the queue.
func (s *Service) Paused() bool { return s.paused.Load() }

// LoadPaused reads the pause back at start.
func (s *Service) LoadPaused(ctx context.Context) {
	on, _ := strconv.ParseBool(database.SettingOr(ctx, s.Store.Read(), "what_to_pick", pausedKey, "false"))
	s.paused.Store(on)
}

// SetPaused holds subtitle work in the queue, or lets it run again. A job
// that is running is not stopped: it ends by itself and nothing new starts.
func (s *Service) SetPaused(ctx context.Context, on bool, by database.Actor) error {
	err := s.Store.Write(ctx, func(tx *sql.Tx) error {
		return database.PutSetting(tx, "what_to_pick", pausedKey, strconv.FormatBool(on), by, now())
	})
	if err != nil {
		return err
	}
	s.paused.Store(on)
	return nil
}

// pauseGate is the queue's gate with the subtitle pause added to it. The
// gate it wraps, which holds the jobs of parts that are off, keeps its say.
type pauseGate struct {
	inner jobs.Gate // may be nil
	s     *Service
}

// PauseGate wraps the queue's gate so that paused subtitle work waits.
func PauseGate(inner jobs.Gate, s *Service) jobs.Gate { return pauseGate{inner, s} }

func (g pauseGate) Active() bool {
	return g.s.Paused() || (g.inner != nil && g.inner.Active())
}

func (g pauseGate) Held(ctx context.Context, db database.Querier, kind string, subject *database.Subject) bool {
	if g.s.Paused() && pausedKind(kind) {
		return true
	}
	return g.inner != nil && g.inner.Active() && g.inner.Held(ctx, db, kind, subject)
}

func (g pauseGate) KindOff(kind string) bool {
	if g.s.Paused() && pausedKind(kind) {
		return true
	}
	return g.inner != nil && g.inner.Active() && g.inner.KindOff(kind)
}

// OffKinds lists the kinds held whatever their subject, so the queue leaves
// them out when it looks for the next job.
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

func (a *Handlers) putPause(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Paused bool `json:"paused"`
	}
	if !webserver.Decode(w, r, &in, `send {"paused": true} or {"paused": false}`) {
		return
	}
	if err := a.svc.SetPaused(r.Context(), in.Paused, actorOf(r)); err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{"paused": a.svc.Paused()})
}
