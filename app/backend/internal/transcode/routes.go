package transcode

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/webserver"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

// Handlers serves the HTTP endpoints for the transcode module.
type Handlers struct {
	store *database.Store
	svc   *Service
}

// NewHandlers builds the transcode HTTP route handlers.
func NewHandlers(store *database.Store, svc *Service) *Handlers {
	return &Handlers{store: store, svc: svc}
}

// Routes registers all 22 transcode HTTP endpoints on m.
func (a *Handlers) Routes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/transcode/overview", a.overview)
	m.HandleFunc("GET /api/v1/transcode/items", a.items)
	m.HandleFunc("GET /api/v1/transcode/waiting", a.waiting)
	m.HandleFunc("GET /api/v1/transcode/failed", a.failed)
	m.HandleFunc("POST /api/v1/transcode/scan", a.scan)
	m.HandleFunc("POST /api/v1/transcode/queue-all", a.queueAll)
	m.HandleFunc("POST /api/v1/files/{id}/transcode", a.queueFile)
	m.HandleFunc("POST /api/v1/files/{id}/transcode/skip", a.skipFile)
	m.HandleFunc("POST /api/v1/transcode/jobs/{id}/bump", a.bumpJob)
	m.HandleFunc("POST /api/v1/transcode/jobs/{id}/skip", a.skipJob)
	m.HandleFunc("POST /api/v1/transcode/jobs/{id}/stop", a.stopJob)
	m.HandleFunc("POST /api/v1/transcode/jobs/{id}/retry", a.retryJob)
	m.HandleFunc("POST /api/v1/transcode/jobs/retry-failed", a.retryFailed)
	m.HandleFunc("PUT /api/v1/transcode/pause", a.putPause)
	m.HandleFunc("GET /api/v1/transcode/settings", a.getSettings)
	m.HandleFunc("PUT /api/v1/transcode/settings", a.putSettings)
	m.HandleFunc("GET /api/v1/transcode/hardware", a.hardware)
	m.HandleFunc("GET /api/v1/transcode/profiles", a.listProfiles)
	m.HandleFunc("POST /api/v1/transcode/profiles", a.createProfile)
	m.HandleFunc("PUT /api/v1/transcode/profiles/{id}", a.updateProfile)
	m.HandleFunc("DELETE /api/v1/transcode/profiles/{id}", a.deleteProfile)
	m.HandleFunc("PUT /api/v1/transcode/library-folders/{id}", a.putLibraryFolder)
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func actorOf(r *http.Request) domain.Actor {
	if _, ok := webserver.From(r.Context()); ok {
		return webserver.Actor(r.Context())
	}
	return domain.Actor{Kind: "user", ID: 1}
}

func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Body == nil {
		return true
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		webserver.Fail(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	if len(raw) == 0 {
		return true
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		webserver.Fail(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func queryInt(r *http.Request, key string, def, maxVal int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil || n <= 0 {
		return def
	}
	if maxVal > 0 && n > maxVal {
		return maxVal
	}
	return n
}

func (a *Handlers) overview(w http.ResponseWriter, r *http.Request) {
	ov, err := a.svc.Overview(r.Context())
	if err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	webserver.WriteJSON(w, http.StatusOK, ov)
}

func (a *Handlers) items(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	if state == "" {
		state = "all"
	}
	switch state {
	case "pending", "done", "skipped", "history", "failed", "all":
	default:
		webserver.Fail(w, http.StatusBadRequest, "state must be pending, done, skipped, history, failed, or all")
		return
	}
	limit := queryInt(r, "limit", 50, 200)
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	rows, total, err := a.svc.ListLibraryItems(r.Context(), state, q.Get("q"), q.Get("codec"), limit, offset)
	if err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items":  rows,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

func (a *Handlers) waiting(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := queryInt(r, "limit", 50, 200)
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	list, total, err := a.svc.ListWaitingJobs(r.Context(), q.Get("q"), limit, offset)
	if err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{
		"jobs":   list,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

func (a *Handlers) bumpJob(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		webserver.Fail(w, http.StatusNotFound, "no such job")
		return
	}
	fresh, err := a.svc.BumpJob(r.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, database.ErrNotFound):
			webserver.Fail(w, http.StatusNotFound, "no such job")
		case errors.Is(err, jobs.ErrNotQueued):
			webserver.Fail(w, http.StatusConflict, err.Error())
		default:
			webserver.Internal(w, "api", err)
		}
		return
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{
		"id":       fresh.ID,
		"priority": fresh.Priority,
		"state":    fresh.State,
	})
}

func (a *Handlers) skipJob(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		webserver.Fail(w, http.StatusNotFound, "no such job")
		return
	}
	fileID, err := a.svc.SkipJob(r.Context(), id, actorOf(r))
	if err != nil {
		switch {
		case errors.Is(err, database.ErrNotFound):
			webserver.Fail(w, http.StatusNotFound, "no such job")
		case errors.Is(err, jobs.ErrNotQueued):
			webserver.Fail(w, http.StatusConflict, err.Error())
		default:
			webserver.Internal(w, "api", err)
		}
		return
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{
		"id":      id,
		"skipped": true,
		"file_id": fileID,
	})
}

func (a *Handlers) skipFile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		webserver.Fail(w, http.StatusNotFound, "no such file")
		return
	}
	if err := a.svc.SkipFile(r.Context(), id, actorOf(r)); err != nil {
		switch {
		case errors.Is(err, database.ErrNotFound):
			webserver.Fail(w, http.StatusNotFound, "no such file")
		case errors.Is(err, mediafiles.ErrTranscodeNotMain):
			webserver.Fail(w, http.StatusConflict, err.Error())
		default:
			webserver.Internal(w, "api", err)
		}
		return
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{
		"skipped": true,
		"file_id": id,
	})
}

func (a *Handlers) failed(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := queryInt(r, "limit", 50, 200)
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	list, total, failedCount, cancelledCount, err := a.svc.ListFailedJobs(r.Context(), q.Get("state"), limit, offset)
	if err != nil {
		webserver.Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{
		"jobs":            list,
		"total":           total,
		"failed_count":    failedCount,
		"cancelled_count": cancelledCount,
	})
}

func (a *Handlers) scan(w http.ResponseWriter, r *http.Request) {
	var in struct {
		QueueMissing bool `json:"queue_missing"`
		Enqueue      bool `json:"enqueue"`
	}
	if !decodeOptionalJSON(w, r, &in) {
		return
	}
	if err := a.svc.QueueScan(r.Context(), in.QueueMissing || in.Enqueue); err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	webserver.WriteJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

func (a *Handlers) queueAll(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Limit int `json:"limit"`
	}
	if !decodeOptionalJSON(w, r, &in) {
		return
	}
	queued, skipped, err := a.svc.QueueAll(r.Context(), in.Limit)
	if err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	webserver.WriteJSON(w, http.StatusAccepted, map[string]any{
		"queued":  queued,
		"skipped": skipped,
	})
}

func (a *Handlers) queueFile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		webserver.Fail(w, http.StatusNotFound, "no such file")
		return
	}
	var in struct {
		Force bool `json:"force"`
	}
	if !decodeOptionalJSON(w, r, &in) {
		return
	}
	if err := a.svc.QueueFile(r.Context(), id, in.Force); err != nil {
		switch {
		case errors.Is(err, database.ErrNotFound):
			webserver.Fail(w, http.StatusNotFound, "no such file")
		case errors.Is(err, mediafiles.ErrTranscodeNotMain),
			errors.Is(err, ErrFolderDisabled),
			errors.Is(err, jobs.ErrAlreadyQueued):
			webserver.Fail(w, http.StatusConflict, err.Error())
		default:
			webserver.Internal(w, "api", err)
		}
		return
	}
	webserver.WriteJSON(w, http.StatusAccepted, map[string]any{"queued": true, "file_id": id})
}

func (a *Handlers) stopJob(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		webserver.Fail(w, http.StatusNotFound, "no such job")
		return
	}
	if err := a.svc.StopJob(r.Context(), id); err != nil {
		if errors.Is(err, database.ErrNotFound) {
			webserver.Fail(w, http.StatusNotFound, "no such job")
			return
		}
		webserver.Fail(w, http.StatusConflict, err.Error())
		return
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{"id": id, "state": "cancelled"})
}

func (a *Handlers) retryJob(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		webserver.Fail(w, http.StatusNotFound, "no such job")
		return
	}
	fresh, err := a.svc.RetryJob(r.Context(), id)
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

func (a *Handlers) retryFailed(w http.ResponseWriter, r *http.Request) {
	retried, skipped, err := a.svc.RetryFailedJobs(r.Context())
	if err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{"retried": retried, "skipped": skipped})
}

func (a *Handlers) putPause(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Paused bool `json:"paused"`
	}
	if !webserver.Decode(w, r, &in, "send paused: true or paused: false") {
		return
	}
	if err := a.svc.SetPaused(r.Context(), in.Paused, actorOf(r)); err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{"paused": a.svc.Paused()})
}

func (a *Handlers) getSettings(w http.ResponseWriter, r *http.Request) {
	webserver.WriteJSON(w, http.StatusOK, ReadSettings(r.Context(), a.store.Read()))
}

func (a *Handlers) putSettings(w http.ResponseWriter, r *http.Request) {
	cur := ReadSettings(r.Context(), a.store.Read())
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || len(raw) == 0 {
		webserver.Fail(w, http.StatusBadRequest, "send transcode settings JSON")
		return
	}
	if err := decodeSettingsOverlay(raw, &cur); err != nil {
		webserver.Fail(w, http.StatusBadRequest, "invalid settings JSON: "+err.Error())
		return
	}
	if err := a.svc.UpdateSettings(r.Context(), cur, actorOf(r)); err != nil {
		webserver.Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	webserver.WriteJSON(w, http.StatusOK, ReadSettings(r.Context(), a.store.Read()))
}

func (a *Handlers) hardware(w http.ResponseWriter, r *http.Request) {
	webserver.WriteJSON(w, http.StatusOK, a.svc.ProbeHardware(r.Context()))
}

func (a *Handlers) listProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, folders, err := a.svc.ListProfiles(r.Context())
	if err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{
		"profiles":        profiles,
		"library_folders": folders,
	})
}

func (a *Handlers) createProfile(w http.ResponseWriter, r *http.Request) {
	var in ProfileInput
	if !webserver.Decode(w, r, &in, "send profile fields") {
		return
	}
	prof, err := a.svc.CreateProfile(r.Context(), in, actorOf(r))
	if err != nil {
		webserver.Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	webserver.WriteJSON(w, http.StatusCreated, prof)
}

func (a *Handlers) updateProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		webserver.Fail(w, http.StatusNotFound, "no such profile")
		return
	}
	var in ProfileInput
	if !webserver.Decode(w, r, &in, "send profile fields") {
		return
	}
	prof, err := a.svc.UpdateProfile(r.Context(), id, in, actorOf(r))
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			webserver.Fail(w, http.StatusNotFound, "no such profile")
			return
		}
		webserver.Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	webserver.WriteJSON(w, http.StatusOK, prof)
}

func (a *Handlers) deleteProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		webserver.Fail(w, http.StatusNotFound, "no such profile")
		return
	}
	err := a.svc.DeleteProfile(r.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, database.ErrNotFound):
			webserver.Fail(w, http.StatusNotFound, "no such profile")
		case errors.Is(err, ErrLastProfile), errors.Is(err, ErrDefaultProfile):
			webserver.Fail(w, http.StatusConflict, err.Error())
		default:
			webserver.Internal(w, "api", err)
		}
		return
	}
	webserver.WriteJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (a *Handlers) putLibraryFolder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		webserver.Fail(w, http.StatusNotFound, "no such library folder")
		return
	}
	var in struct {
		Enabled   bool   `json:"enabled"`
		ProfileID *int64 `json:"profile_id"`
	}
	if !webserver.Decode(w, r, &in, "send enabled and profile_id") {
		return
	}
	la, err := a.svc.SetLibraryFolderProfile(r.Context(), id, in.Enabled, in.ProfileID, actorOf(r))
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			webserver.Fail(w, http.StatusNotFound, "library folder or profile not found")
			return
		}
		webserver.Fail(w, http.StatusBadRequest, err.Error())
		return
	}
	webserver.WriteJSON(w, http.StatusOK, la)
}
