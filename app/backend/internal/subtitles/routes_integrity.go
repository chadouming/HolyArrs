package subtitles

import (
	"net/http"
	"strconv"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/webserver"
)

func (a *Handlers) subtitleIntegrity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	summary, err := a.svc.IntegritySummary(ctx)
	if err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	issues, total, err := a.svc.IntegrityIssues(ctx, limitOf(r, 50), max(offset, 0))
	if err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{
		"summary": summary,
		"issues":  issues,
		"total":   total,
	})
}

func (a *Handlers) verifySubtitles(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.QueueVerify(r.Context()); err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	webserver.WriteJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

func (a *Handlers) replaceSubtitle(w http.ResponseWriter, r *http.Request) {
	id, ok := subID(r)
	if !ok {
		webserver.Fail(w, http.StatusNotFound, "no such subtitle")
		return
	}
	if err := a.svc.ReplaceCorrupt(r.Context(), id, actorOf(r)); err != nil {
		subtitleError(w, err)
		return
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{"replaced": true})
}

func (a *Handlers) keepSubtitle(w http.ResponseWriter, r *http.Request) {
	id, ok := subID(r)
	if !ok {
		webserver.Fail(w, http.StatusNotFound, "no such subtitle")
		return
	}
	if err := a.svc.KeepIntegrity(r.Context(), id, actorOf(r)); err != nil {
		subtitleError(w, err)
		return
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{"kept": true})
}
