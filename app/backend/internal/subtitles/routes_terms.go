package subtitles

import (
	"errors"
	"net/http"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/webserver"
)

// termsTitle reads the title in the path and says whether it is there.
func (a *Handlers) termsTitle(w http.ResponseWriter, r *http.Request) (int64, bool) {
	t, ok := a.titleID(w, r)
	if !ok {
		return 0, false
	}
	if t.State == "removed" {
		webserver.Fail(w, http.StatusNotFound, "no such Title")
		return 0, false
	}
	return t.ID, true
}

// getTerms answers with a title's names and terms for one language: the
// owner's own, and those gathered from the title's subtitles.
func (a *Handlers) getTerms(w http.ResponseWriter, r *http.Request) {
	id, ok := a.termsTitle(w, r)
	if !ok {
		return
	}
	lang := NormLanguage(r.URL.Query().Get("language"))
	if lang == "" {
		webserver.Fail(w, http.StatusBadRequest, "unknown language")
		return
	}
	t, err := a.svc.Terms(r.Context(), id, lang)
	if err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	webserver.WriteJSON(w, http.StatusOK, t)
}

func (a *Handlers) putTerms(w http.ResponseWriter, r *http.Request) {
	id, ok := a.termsTitle(w, r)
	if !ok {
		return
	}
	var in struct {
		Language string   `json:"language"`
		Own      []string `json:"own"`
		Removed  []string `json:"removed"`
	}
	if !webserver.Decode(w, r, &in, `send {"language": "en", "own": ["a name or term"], "removed": ["a gathered term to leave out"]}`) {
		return
	}
	t, err := a.svc.SaveTerms(r.Context(), id, in.Language, in.Own, in.Removed, actorOf(r))
	switch {
	case errors.Is(err, ErrBadLanguage):
		webserver.Fail(w, http.StatusBadRequest, "unknown language")
	case err != nil:
		webserver.Internal(w, "api", err)
	default:
		webserver.WriteJSON(w, http.StatusOK, t)
	}
}
