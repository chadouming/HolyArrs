package subtitles

import (
	"errors"
	"net/http"
	"runtime"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/webserver"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/builtin"
)

func modelError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrUnknownModel):
		webserver.Fail(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrModelInUse):
		webserver.Fail(w, http.StatusConflict, err.Error())
	default:
		webserver.Internal(w, "api", err)
	}
}

// listModels lists the built-in translator's models with what the page needs
// to offer them: the machine's memory and the thread counts to choose from.
func (a *Handlers) listModels(w http.ResponseWriter, r *http.Request) {
	webserver.WriteJSON(w, http.StatusOK, map[string]any{
		"models":          a.svc.Models(r.Context()),
		"devices":         a.svc.Devices(r.Context()),
		"total_memory":    builtin.TotalMemory(),
		"threads_max":     runtime.NumCPU(),
		"threads_default": builtin.DefaultThreads(),
	})
}

func (a *Handlers) downloadModel(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.QueueModelDownload(r.Context(), r.PathValue("id")); err != nil {
		modelError(w, err)
		return
	}
	webserver.WriteJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

func (a *Handlers) cancelModelDownload(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.CancelModelDownload(r.Context(), r.PathValue("id")); err != nil {
		modelError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Handlers) deleteModel(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.DeleteModel(r.Context(), r.PathValue("id")); err != nil {
		modelError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
