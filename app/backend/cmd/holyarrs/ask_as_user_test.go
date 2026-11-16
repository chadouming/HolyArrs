package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/accounts"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/requests"
)

// An app with the owner's key (a Jellyfin plugin) asks for the Jellyfin user
// in X-HolyArrs-User: the ask is filed under the linked person, with their
// weekly limit and their waiting for approval, never the owner's free pass.
func TestAskAsLinkedUser(t *testing.T) {
	t.Parallel()
	r := newAskRig(t)
	ctx := context.Background()
	const jfAna, jfNobody = "a1b2c3d4e5f60718293a4b5c6d7e8f90", "ffffffffffffffffffffffffffffffff"
	if err := r.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO user_links(user_id, provider, external_id, external_name, created_at) VALUES (?, 'jellyfin', ?, 'ana', '2026-01-01T00:00:00Z')", r.ana.id, jfAna)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r.owner.call("PUT", "/api/v1/settings/asks", `{"movies_per_week":1,"search_on_approve":false}`, 200)
	var made struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(r.owner.call("POST", "/api/v1/auth/keys", `{"label":"Jellyfin plugin"}`, 201), &made)

	// keyCall sends one call with the owner's key, as asUser when it is set.
	keyCall := func(method, path, body, asUser string, want int) []byte {
		t.Helper()
		req, _ := http.NewRequest(method, r.srv.URL+path, strings.NewReader(body))
		req.Header.Set(accounts.APIKeyHeader, made.Key)
		if asUser != "" {
			req.Header.Set(accounts.AsUserHeader, asUser)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s as %q: %d %s; want %d", method, path, asUser, resp.StatusCode, b, want)
		}
		return b
	}
	asAna := "jellyfin:" + jfAna

	a := askOf(t, keyCall("POST", "/api/v1/asks", `{"id":"tmdb:693134"}`, asAna, 201))
	if a.AskedBy.ID != r.ana.id || a.State != "asked" {
		t.Fatalf("filed under user %d in state %q; want ana (%d), asked", a.AskedBy.ID, a.State, r.ana.id)
	}

	// History names ana and the key the ask came through.
	es, _ := r.st.RecentEvents(ctx, 50)
	var ev *domain.Event
	for i := range es {
		if es[i].Kind == domain.EventAskMade {
			ev = &es[i]
		}
	}
	if ev == nil {
		t.Fatal("no ask Event")
	}
	if ev.Data["asked_by"] != "ana" || ev.Data["via_key"] != "Jellyfin plugin" || !strings.HasSuffix(ev.Summary, `(through the key "Jellyfin plugin")`) || !strings.HasPrefix(ev.Summary, "ana asked for ") {
		t.Fatalf("Event %q %v", ev.Summary, ev.Data)
	}

	// ana's weekly limit of one movie holds, though the key's owner has none.
	if b := keyCall("POST", "/api/v1/asks", `{"id":"tmdb:900002"}`, asAna, 429); !strings.Contains(string(b), `"per_week":1`) {
		t.Fatalf("over the limit: %s", b)
	}
	var lim requests.Limit
	_ = json.Unmarshal(keyCall("GET", "/api/v1/asks/limit", "", asAna, 200), &lim)
	if lim.Movies.PerWeek != 1 || lim.Movies.Left != 0 {
		t.Fatalf("ana's limit through the key: %+v", lim.Movies)
	}

	// An unlinked Jellyfin user files nothing, least of all under the owner.
	if b := keyCall("POST", "/api/v1/asks", `{"id":"tmdb:900003"}`, "jellyfin:"+jfNobody, 404); !strings.Contains(string(b), `"code":"user_not_linked"`) {
		t.Fatalf("unlinked: %s", b)
	}
	var all struct {
		Asks []requests.View `json:"asks"`
	}
	_ = json.Unmarshal(r.owner.call("GET", "/api/v1/asks", "", 200), &all)
	if len(all.Asks) != 1 || all.Asks[0].AskedBy.ID != r.ana.id {
		t.Fatalf("asks after the refusals: %+v", all.Asks)
	}

	// The owner's own ask with the same key still counts as the owner.
	own := askOf(t, keyCall("POST", "/api/v1/asks", `{"id":"tmdb:900003"}`, "", 201))
	if own.AskedBy.ID == r.ana.id || own.State == "asked" {
		t.Fatalf("owner's ask: by %d, state %q", own.AskedBy.ID, own.State)
	}

	// Through the key ana sees only her own asks, and can take hers back.
	_ = json.Unmarshal(keyCall("GET", "/api/v1/asks", "", asAna, 200), &all)
	if len(all.Asks) != 1 || all.Asks[0].ID != a.ID {
		t.Fatalf("ana's list through the key: %+v", all.Asks)
	}
	keyCall("GET", "/api/v1/asks/"+itoa(own.ID), "", asAna, 404)
	keyCall("DELETE", "/api/v1/asks/"+itoa(a.ID), "", asAna, 204)
	r.owner.call("GET", "/api/v1/asks/"+itoa(a.ID), "", 404)

	// last is the newest History entry of a kind whose summary starts with prefix.
	last := func(kind, prefix string) domain.Event {
		t.Helper()
		es, _ := r.st.RecentEvents(ctx, 50)
		for _, e := range es {
			if e.Kind == kind && strings.HasPrefix(e.Summary, prefix) {
				return e
			}
		}
		t.Fatalf("no %s Event starting %q", kind, prefix)
		return domain.Event{}
	}
	const mark = `(through the key "Jellyfin plugin")`
	if e := last(domain.EventAskWithdrawn, "ana took back "); e.Data["via_key"] != "Jellyfin plugin" || !strings.HasSuffix(e.Summary, mark) {
		t.Fatalf("take-back Event %q %v", e.Summary, e.Data)
	}

	// bo may auto-approve and approve: his ask through the key is approved at
	// once, and a request he removes through the key says so too.
	const jfBo = "b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0"
	if err := r.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO user_links(user_id, provider, external_id, external_name, created_at) VALUES (?, 'jellyfin', ?, 'bo', '2026-01-01T00:00:00Z')", r.bo.id, jfBo)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r.owner.call("PUT", "/api/v1/users/"+itoa(r.bo.id), `{"perms":["ask","auto_approve","approve"]}`, 200)
	if b := askOf(t, keyCall("POST", "/api/v1/asks", `{"id":"tmdb:900002"}`, "jellyfin:"+jfBo, 201)); b.AskedBy.ID != r.bo.id || b.State == "asked" {
		t.Fatalf("bo's ask through the key: by %d, state %q; want bo, approved", b.AskedBy.ID, b.State)
	}
	keyCall("DELETE", "/api/v1/asks/"+itoa(own.ID), "", "jellyfin:"+jfBo, 204)
	if e := last(domain.EventAskWithdrawn, "bo removed "); e.Data["via_key"] != "Jellyfin plugin" || !strings.HasSuffix(e.Summary, mark) {
		t.Fatalf("remove Event %q %v", e.Summary, e.Data)
	}
	r.asks.Wait()
}
