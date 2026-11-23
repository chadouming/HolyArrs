package subtitles

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/webserver"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

func (a *Handlers) failedSubtitles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	stateParam := q.Get("state")
	var states []string
	switch stateParam {
	case "", "all":
		states = []string{"failed", "cancelled"}
	case "failed", "cancelled":
		states = []string{stateParam}
	default:
		webserver.Fail(w, http.StatusBadRequest, "state must be all, failed or cancelled")
		return
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit := limitOf(r, 50)
	ctx, db := r.Context(), a.store.Read()
	list, total, err := jobs.ListByStates(ctx, db, overviewKinds, states, limit, max(offset, 0))
	if err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	counts, err := jobs.CountByState(ctx, db, overviewKinds, "failed", "cancelled")
	if err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	summaries, err := mediafiles.GetFileSummariesByIDs(ctx, db, jobFileIDs(list))
	if err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	out := make([]OverviewJob, 0, len(list))
	for _, j := range list {
		out = append(out, a.svc.toOverviewJob(j, summaries))
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{
		"jobs":            out,
		"total":           total,
		"failed_count":    counts["failed"],
		"cancelled_count": counts["cancelled"],
	})
}

func (a *Handlers) stopSubtitleJob(w http.ResponseWriter, r *http.Request) {
	id, ok := subID(r)
	if !ok {
		webserver.Fail(w, http.StatusNotFound, "no such job")
		return
	}
	if a.svc.Jobs == nil {
		webserver.Internal(w, "api", errors.New("no job queue"))
		return
	}
	ctx := r.Context()
	var j jobs.Job
	if err := database.Get(ctx, a.store.Read(), "jobs", id, &j); err != nil {
		if errors.Is(err, database.ErrNotFound) {
			webserver.Fail(w, http.StatusNotFound, "no such job")
			return
		}
		webserver.Internal(w, "api", err)
		return
	}
	if !isSubtitleJobKind(j.Kind) {
		webserver.Fail(w, http.StatusNotFound, "no such job")
		return
	}
	if j.Kind == KindModelDownload {
		if mid, _ := j.Params["id"].(string); mid != "" {
			_ = a.svc.CancelModelDownload(ctx, mid)
		}
	}
	if err := a.svc.Jobs.Cancel(ctx, id); err != nil {
		if errors.Is(err, database.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
			webserver.Fail(w, http.StatusNotFound, "no such job")
			return
		}
		webserver.Fail(w, http.StatusConflict, err.Error())
		return
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{"id": id, "state": "cancelled"})
}

func (a *Handlers) clearJobHold(ctx context.Context, j jobs.Job) {
	if j.Subject == nil || j.Subject.Type != "file" {
		return
	}
	switch j.Kind {
	case KindTranscribe:
		a.svc.clearTranscript(ctx, j.Subject.ID)
	case KindTranslate:
		if lang, _ := j.Params["language"].(string); lang != "" {
			if norm := NormLanguage(lang); norm != "" {
				forced, _ := j.Params["forced"].(bool)
				a.svc.clearWishHold(ctx, j.Subject.ID, norm, forced)
			}
		}
	case KindSearch:
		a.svc.clearFileWishHold(ctx, j.Subject.ID)
	}
}

func (a *Handlers) retrySubtitleJob(w http.ResponseWriter, r *http.Request) {
	id, ok := subID(r)
	if !ok {
		webserver.Fail(w, http.StatusNotFound, "no such job")
		return
	}
	if a.svc.Jobs == nil {
		webserver.Internal(w, "api", errors.New("no job queue"))
		return
	}
	ctx := r.Context()
	var j jobs.Job
	if err := database.Get(ctx, a.store.Read(), "jobs", id, &j); err != nil {
		if errors.Is(err, database.ErrNotFound) {
			webserver.Fail(w, http.StatusNotFound, "no such job")
			return
		}
		webserver.Internal(w, "api", err)
		return
	}
	if !isSubtitleJobKind(j.Kind) {
		webserver.Fail(w, http.StatusNotFound, "no such job")
		return
	}
	a.clearJobHold(ctx, j)
	fresh, err := a.svc.Jobs.Retry(ctx, id)
	if err != nil {
		switch {
		case errors.Is(err, database.ErrNotFound):
			webserver.Fail(w, http.StatusNotFound, "no such job")
		case errors.Is(err, jobs.ErrAlreadyQueued), errors.Is(err, jobs.ErrCannotRetry):
			webserver.Fail(w, http.StatusConflict, err.Error())
		default:
			webserver.Internal(w, "api", err)
		}
		return
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{"id": fresh.ID, "state": fresh.State})
}

func (a *Handlers) retryFailedSubtitleJobs(w http.ResponseWriter, r *http.Request) {
	if a.svc.Jobs == nil {
		webserver.Internal(w, "api", errors.New("no job queue"))
		return
	}
	ctx := r.Context()
	list, _, err := jobs.ListByStates(ctx, a.store.Read(), overviewKinds, []string{"failed"}, 500, 0)
	if err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	retried, skipped := 0, 0
	for _, j := range list {
		a.clearJobHold(ctx, j)
		if _, err := a.svc.Jobs.Retry(ctx, j.ID); err != nil {
			skipped++
			continue
		}
		retried++
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{"retried": retried, "skipped": skipped})
}
