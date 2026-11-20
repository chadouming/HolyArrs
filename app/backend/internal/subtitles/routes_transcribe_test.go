package subtitles

import (
	"net/http"
	"strings"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/faketranslator"
)

// route: GET /api/v1/settings/subtitles/transcriber
// route: POST /api/v1/files/{id}/subtitles/transcribe
func TestRoutes_Transcribe(t *testing.T) {
	f := newFix(t)
	s, q := withTranslator(t, f, &faketranslator.OpenAI{})
	withTranscriber(t, f, s, "en,en,en", "eng")
	m := http.NewServeMux()
	NewHandlers(f.st, s.Vault, s).Routes(m)
	code, body := call(m, "GET", "/api/v1/settings/subtitles/transcriber", "")
	if code != 200 || !strings.Contains(body, `"available":true`) || !strings.Contains(body, `"ready":true`) || !strings.Contains(body, `"model":"Lab Speech"`) {
		t.Fatalf("transcriber: %d %s", code, body)
	}
	code, body = call(m, "POST", "/api/v1/files/"+itoa(f.movieFile)+"/subtitles/transcribe", "")
	if code != http.StatusAccepted || queuedKinds(q, KindTranscribe) != 1 {
		t.Fatalf("transcribe: %d %s, %d queued", code, body, queuedKinds(q, KindTranscribe))
	}
	if code, _ := call(m, "POST", "/api/v1/files/999999/subtitles/transcribe", ""); code != http.StatusNotFound {
		t.Fatalf("unknown file: %d", code)
	}
	// A file that already has a text subtitle is refused at once.
	labMovie(t, f)
	if code, body := call(m, "POST", "/api/v1/files/"+itoa(f.movieFile)+"/subtitles/transcribe", ""); code != http.StatusConflict || !strings.Contains(body, "text subtitle") {
		t.Fatalf("file with a text subtitle: %d %s", code, body)
	}
}
