package subtitles

import (
	"net/http"
	"strings"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/faketranslator"
)

// route: GET /api/v1/settings/subtitles/translator/models
// route: POST /api/v1/settings/subtitles/translator/models/{id}/download
// route: DELETE /api/v1/settings/subtitles/translator/models/{id}/download
// route: DELETE /api/v1/settings/subtitles/translator/models/{id}
func TestRoutes_TranslatorModels(t *testing.T) {
	f := newFix(t)
	s, q := withTranslator(t, f, &faketranslator.OpenAI{})
	_, m := withCatalogue(t, s)
	mux := http.NewServeMux()
	NewHandlers(f.st, s.Vault, s).Routes(mux)

	code, body := call(mux, "GET", "/api/v1/settings/subtitles/translator/models", "")
	if code != 200 || !strings.Contains(body, `"id":"lab-small"`) || !strings.Contains(body, `"id":"file:lab.gguf"`) ||
		!strings.Contains(body, `"devices":[{"id":"Vulkan0"`) || !strings.Contains(body, `"threads_max":`) || !strings.Contains(body, `"threads_default":`) || !strings.Contains(body, `"total_memory":`) {
		t.Fatalf("list: %d %s", code, body)
	}
	if code, body := call(mux, "POST", "/api/v1/settings/subtitles/translator/models/"+m.ID+"/download", ""); code != http.StatusAccepted {
		t.Fatalf("download: %d %s", code, body)
	}
	if len(*q) != 1 || (*q)[0].kind != KindModelDownload || (*q)[0].params["id"] != m.ID {
		t.Fatalf("queued %+v", *q)
	}
	if code, _ := call(mux, "POST", "/api/v1/settings/subtitles/translator/models/nothing/download", ""); code != http.StatusNotFound {
		t.Fatalf("unknown model: %d", code)
	}
	if code, _ := call(mux, "DELETE", "/api/v1/settings/subtitles/translator/models/"+m.ID+"/download", ""); code != http.StatusNoContent {
		t.Fatalf("cancel: %d", code)
	}
	if code, _ := call(mux, "DELETE", "/api/v1/settings/subtitles/translator/models/file:lab.gguf", ""); code != http.StatusNoContent {
		t.Fatalf("delete own file: %d", code)
	}
	call(mux, "PUT", "/api/v1/settings/subtitles/translator", `{"kind":"builtin","builtin_model":"file:lab.gguf"}`)
	if code, _ := call(mux, "DELETE", "/api/v1/settings/subtitles/translator/models/file:..%2Fx.gguf", ""); code != http.StatusNotFound {
		t.Fatalf("a path outside the folder: %d", code)
	}
}
