package subtitles

import (
	"net/http"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/webserver"
)

func (a *Handlers) getTranscriber(w http.ResponseWriter, r *http.Request) {
	webserver.WriteJSON(w, http.StatusOK, a.svc.Transcriber(r.Context()))
}

// transcribeSubtitle queues a transcript a person asked for. What can be
// checked at once is said here; the Job reports how the transcript went.
func (a *Handlers) transcribeSubtitle(w http.ResponseWriter, r *http.Request) {
	id, ok := subID(r)
	if !ok {
		webserver.Fail(w, http.StatusNotFound, "no such File")
		return
	}
	ctx := r.Context()
	fc, err := a.svc.load(ctx, id)
	if err != nil {
		subtitleError(w, err)
		return
	}
	if !a.svc.Transcriber(ctx).Ready {
		webserver.Fail(w, http.StatusConflict, ErrNoTranscriber.Error())
		return
	}
	actor := actorOf(r)
	a.svc.UnskipFile(ctx, fc.file.ID, actor)
	st, err := a.svc.look(ctx, fc, actor)
	if err != nil {
		subtitleError(w, err)
		return
	}
	if hasText(st) {
		webserver.Fail(w, http.StatusConflict, ErrHasText.Error())
		return
	}
	if err := a.svc.QueueTranscribe(ctx, fc.file.ID, true); err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	out := map[string]any{"queued": true}
	if jid, ok := a.jobByKey(ctx, KindTranscribe+":"+itoa(fc.file.ID)); ok {
		out["job_id"] = jid
	}
	webserver.WriteJSON(w, http.StatusAccepted, out)
}
