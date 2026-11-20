package subtitles

import (
	"net/http"
	"strings"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/faketranslator"
)

// route: GET /api/v1/titles/{id}/subtitle-terms
// route: PUT /api/v1/titles/{id}/subtitle-terms
func TestRoutes_SubtitleTerms(t *testing.T) {
	f := newFix(t)
	s, _ := withTranslator(t, f, &faketranslator.OpenAI{})
	for i := 0; i < 2; i++ {
		besideSub(t, f, "t"+itoa(int64(i))+".en.srt", "en", "", "We sail with Orlav at dawn.", "Has anyone seen Mirelle today?")
	}
	m := http.NewServeMux()
	NewHandlers(f.st, s.Vault, s).Routes(m)
	path := "/api/v1/titles/" + itoa(f.show) + "/subtitle-terms"
	code, body := call(m, "GET", path+"?language=en", "")
	if code != 200 || !strings.Contains(body, `"gathered":["`) || !strings.Contains(body, "Orlav") || !strings.Contains(body, `"own":[]`) {
		t.Fatalf("get: %d %s", code, body)
	}
	code, body = call(m, "PUT", path, `{"language":"en","own":["Wind Blade Technique","  --x  "],"removed":["Mirelle"]}`)
	if code != 200 || !strings.Contains(body, `"own":["Wind Blade Technique","x"]`) || strings.Contains(body, `"gathered":["Mirelle`) || !strings.Contains(body, `"removed":["Mirelle"]`) {
		t.Fatalf("put: %d %s", code, body)
	}
	if code, _ := call(m, "GET", path+"?language=xx", ""); code != http.StatusBadRequest {
		t.Fatalf("unknown language: %d", code)
	}
	if code, _ := call(m, "PUT", path, `{"language":"","own":[]}`); code != http.StatusBadRequest {
		t.Fatalf("no language: %d", code)
	}
	if code, _ := call(m, "GET", "/api/v1/titles/999999/subtitle-terms?language=en", ""); code != http.StatusNotFound {
		t.Fatalf("unknown title: %d", code)
	}
}
