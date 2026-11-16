package requests

import (
	"net/http/httptest"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/webserver"
)

// A person named by a key with X-HolyArrs-User carries that key's label, so
// History can say the ask came through it; their own key leaves no mark.
func TestPersonOfOnBehalf(t *testing.T) {
	who := webserver.Caller{UserID: 7, Name: "ana", Via: "api_key", KeyLabel: "Jellyfin plugin", OnBehalf: true}
	r := httptest.NewRequest("POST", "/api/v1/asks", nil)
	p := PersonOf(r.WithContext(webserver.ContextWith(r.Context(), who)), nil, nil)
	if p.UserID != 7 || p.Name != "ana" || p.ViaKey != "Jellyfin plugin" {
		t.Fatalf("on behalf: %+v", p)
	}
	// The label lands in a History line, so characters that would break or
	// hide part of that line are dropped.
	who.KeyLabel = "Jelly\nfin​ ‮plugin\x00"
	if p := PersonOf(r.WithContext(webserver.ContextWith(r.Context(), who)), nil, nil); p.ViaKey != "Jellyfin plugin" {
		t.Fatalf("label %q", p.ViaKey)
	}
	who.OnBehalf = false
	if p := PersonOf(r.WithContext(webserver.ContextWith(r.Context(), who)), nil, nil); p.ViaKey != "" {
		t.Fatalf("own key: %+v", p)
	}
}
