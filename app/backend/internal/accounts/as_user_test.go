package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/secrets"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/webserver"
)

const (
	jfAna       = "a1b2c3d4e5f60718293a4b5c6d7e8f90"
	jfAnaDashed = "A1B2C3D4-E5F6-0718-293A-4B5C6D7E8F90"
	jfNobody    = "ffffffffffffffffffffffffffffffff"
)

// linkJellyfin links the person named name to a Jellyfin user id, stored as given.
func linkJellyfin(t *testing.T, f *fixture, name, extID string) int64 {
	t.Helper()
	var id int64
	if err := f.s.WriteCore(context.Background(), func(tx *sql.Tx) error {
		if err := tx.QueryRow("SELECT id FROM users WHERE name = ?", name).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM user_links WHERE user_id = ? AND provider = 'jellyfin'", id); err != nil {
			return err
		}
		_, err := tx.Exec("INSERT INTO user_links(user_id, provider, external_id, external_name, created_at) VALUES (?, 'jellyfin', ?, ?, '2026-01-01T00:00:00Z')", id, extID, name)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func setUserState(t *testing.T, f *fixture, id int64, state string) {
	t.Helper()
	if err := f.s.WriteCore(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE users SET state = ? WHERE id = ?", state, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestParseAsUser(t *testing.T) {
	for _, c := range []struct {
		in, id string
		ok     bool
	}{
		{"jellyfin:" + jfAna, jfAna, true},
		{"jellyfin:" + jfAnaDashed, jfAna, true},
		{" jellyfin:" + jfAna + " ", jfAna, true},
		{"emby:" + jfAna, "", false},
		{"jellyfin:", "", false},
		{"jellyfin:not-a-guid", "", false},
		{"jellyfin:" + jfAna + "00", "", false},
		{jfAna, "", false},
		{"", "", false},
	} {
		p, id, ok := parseAsUser(c.in)
		if ok != c.ok || id != c.id || (ok && p != ProviderJellyfin) {
			t.Errorf("parseAsUser(%q) = %q, %q, %v", c.in, p, id, ok)
		}
	}
}

// AsUserRoutes is what the API file and the docs are checked against, so it
// must be exactly what the middleware honours.
func TestAsUserRoutesAreTheOnesHonoured(t *testing.T) {
	if len(AsUserRoutes) != 5 {
		t.Fatalf("AsUserRoutes: %v", AsUserRoutes)
	}
	for _, route := range AsUserRoutes {
		method, path, _ := strings.Cut(route, " ")
		r := httptest.NewRequest(method, strings.ReplaceAll(path, "{id}", "7"), nil)
		if got := asUserPattern(r); got != route {
			t.Errorf("%s: asUserPattern = %q", route, got)
		}
		if _, ok := RoutePerms[route]; !ok {
			t.Errorf("%s is not a route the gate knows", route)
		}
	}
	for _, miss := range []string{"POST /api/v1/asks/remove", "POST /api/v1/asks/7/approve", "GET /api/v1/asks/7/parts", "HEAD /api/v1/asks", "GET /api/v1/titles"} {
		method, path, _ := strings.Cut(miss, " ")
		if got := asUserPattern(httptest.NewRequest(method, path, nil)); got != "" {
			t.Errorf("%s: asUserPattern = %q; want none", miss, got)
		}
	}
}

// A Jellyfin id matches its link whichever way either side spells it, and
// a turned-off person is told apart from no link at all.
func TestLinkedCaller(t *testing.T) {
	f := gated(t, Options{})
	own := ownerOf(t, f)
	join(t, f, invite(t, f, own, `{"role":"member"}`), "ana")
	ctx := context.Background()

	for _, stored := range []string{jfAnaDashed, jfAna} {
		id := linkJellyfin(t, f, "ana", stored)
		var who webserver.Caller
		_, state, err := linkedCaller(ctx, f.s.Read(), ProviderJellyfin, jfAna, &who)
		if err != nil || state != "active" || who.UserID != id || who.Name != "ana" || who.Role != webserver.RoleMember {
			t.Fatalf("stored %q: state %q err %v who %+v", stored, state, err, who)
		}
	}

	var who webserver.Caller
	if _, _, err := linkedCaller(ctx, f.s.Read(), ProviderJellyfin, jfNobody, &who); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("no link: err %v; want sql.ErrNoRows", err)
	}
	// The same id under another provider is another account.
	if _, _, err := linkedCaller(ctx, f.s.Read(), ProviderEmby, jfAna, &who); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("emby: err %v; want sql.ErrNoRows", err)
	}

	id := linkJellyfin(t, f, "ana", jfAna)
	setUserState(t, f, id, "disabled")
	if _, state, err := linkedCaller(ctx, f.s.Read(), ProviderJellyfin, jfAna, &who); err != nil || state != "disabled" {
		t.Fatalf("turned off: state %q err %v", state, err)
	}
}

// asUserFixture is a gated server whose request routes answer with the
// caller they ran as, so a test can see who a call counted as.
func asUserFixture(t *testing.T) *fixture {
	t.Helper()
	s, err := database.Open(t.TempDir(), database.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	v, err := secrets.Open(context.Background(), s, make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	c := newClock()
	a := New(s, Options{Now: c.now, Vault: v, fastHash: true})
	t.Cleanup(a.Close)
	echo := func(w http.ResponseWriter, r *http.Request) {
		who, _ := webserver.From(r.Context())
		_ = json.NewEncoder(w).Encode(map[string]any{"user_id": who.UserID, "name": who.Name, "key_id": who.KeyID, "key_label": who.KeyLabel,
			"on_behalf": who.OnBehalf, "scopes": who.Scopes, "max_rating": who.MaxRating, "via": who.Via})
	}
	m := http.NewServeMux()
	for _, p := range []string{"POST /api/v1/asks", "GET /api/v1/asks", "GET /api/v1/asks/limit", "GET /api/v1/asks/{id}", "DELETE /api/v1/asks/{id}",
		"POST /api/v1/asks/remove", "POST /api/v1/asks/{id}/approve", "GET /api/v1/asks/{id}/parts", "GET /api/v1/settings/asks", "GET /api/v1/titles"} {
		m.HandleFunc(p, echo)
	}
	a.Routes(m)
	srv := httptest.NewServer(a.Middleware(webserver.RecordRouteHits(Gate(m))))
	t.Cleanup(srv.Close)
	return &fixture{srv: srv, a: a, s: s, clock: c}
}

type echoed struct {
	UserID    int64    `json:"user_id"`
	Name      string   `json:"name"`
	KeyID     int64    `json:"key_id"`
	KeyLabel  string   `json:"key_label"`
	OnBehalf  bool     `json:"on_behalf"`
	Scopes    []string `json:"scopes"`
	MaxRating string   `json:"max_rating"`
	Via       string   `json:"via"`
	Error     string   `json:"error"`
	Code      string   `json:"code"`
}

// makeKey makes an API key as p and returns the key and its id.
func makeKey(t *testing.T, f *fixture, p person, body string) (string, int64) {
	t.Helper()
	resp, out := p.do(t, f, "POST", "/api/v1/auth/keys", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("new API key: %d %s", resp.StatusCode, out)
	}
	var v struct {
		ID  int64  `json:"id"`
		Key string `json:"key"`
	}
	jsonOf(t, out, &v)
	return v.Key, v.ID
}

// keyCall sends one call with an API key and each asUser value as its own
// X-HolyArrs-User header line.
func keyCall(t *testing.T, f *fixture, key, method, path string, asUser ...string) (int, echoed) {
	t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+path, nil)
	if key != "" {
		req.Header.Set(APIKeyHeader, key)
	}
	for _, v := range asUser {
		req.Header.Add(AsUserHeader, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var e echoed
	_ = json.NewDecoder(resp.Body).Decode(&e)
	return resp.StatusCode, e
}

func TestAsUserHeader(t *testing.T) {
	f := asUserFixture(t)
	own := ownerOf(t, f)
	// A member makes keys only with the settings permission.
	ana := join(t, f, invite(t, f, own, `{"role":"member","perms":["ask","settings"]}`), "ana")
	bo := join(t, f, invite(t, f, own, `{"role":"admin","perms":["ask","approve","settings"]}`), "bo")
	anaID := linkJellyfin(t, f, "ana", jfAnaDashed)
	const jfOwner = "00000000000000000000000000000001"
	ownerID := linkJellyfin(t, f, "luis", jfOwner)
	ownKey, ownKeyID := makeKey(t, f, own, `{"label":"Jellyfin plugin"}`)
	readKey, _ := makeKey(t, f, own, `{"label":"reader","scopes":["read"]}`)
	anaKey, _ := makeKey(t, f, ana, `{"label":"ana's"}`)
	boKey, _ := makeKey(t, f, bo, `{"label":"bo's"}`)
	asAna := "jellyfin:" + jfAna

	t.Run("counts as the linked person and keeps the key", func(t *testing.T) {
		code, e := keyCall(t, f, ownKey, "POST", "/api/v1/asks", asAna)
		if code != 200 || e.UserID != anaID || e.Name != "ana" || !e.OnBehalf || e.KeyID != ownKeyID || e.KeyLabel != "Jellyfin plugin" || e.Via != "api_key" || !slices.Equal(e.Scopes, []string{webserver.ScopeAll}) {
			t.Fatalf("%d %+v", code, e)
		}
	})
	t.Run("the id matches with dashes and upper case", func(t *testing.T) {
		if code, e := keyCall(t, f, ownKey, "POST", "/api/v1/asks", "jellyfin:"+jfAnaDashed); code != 200 || e.UserID != anaID {
			t.Fatalf("%d %+v", code, e)
		}
	})
	t.Run("a Kid keeps their age limit", func(t *testing.T) {
		setRating := func(v string) {
			if err := f.s.WriteCore(context.Background(), func(tx *sql.Tx) error {
				_, err := tx.Exec("UPDATE users SET max_rating = ? WHERE id = ?", v, anaID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		setRating("PG")
		defer setRating("")
		if code, e := keyCall(t, f, ownKey, "POST", "/api/v1/asks", asAna); code != 200 || e.MaxRating != "PG" {
			t.Fatalf("%d %+v", code, e)
		}
	})
	t.Run("the header adds no scope", func(t *testing.T) {
		plain, _ := keyCall(t, f, readKey, "POST", "/api/v1/asks")
		with, e := keyCall(t, f, readKey, "POST", "/api/v1/asks", asAna)
		// No code: the refusal is the route gate's, about scope, not the header's.
		if plain != http.StatusForbidden || with != plain || e.OnBehalf || e.Code != "" || !strings.Contains(e.Error, "scope") {
			t.Fatalf("read key: %d without, %d with the header (%+v)", plain, with, e)
		}
	})
	t.Run("a key without the route's scope learns nothing about links", func(t *testing.T) {
		// Linked or not, the answer is the route gate's scope refusal.
		for _, v := range []string{asAna, "jellyfin:" + jfNobody} {
			if code, e := keyCall(t, f, readKey, "POST", "/api/v1/asks", v); code != 403 || e.Code != "" || !strings.Contains(e.Error, "scope") {
				t.Errorf("%q: %d %+v", v, code, e)
			}
		}
	})
	t.Run("near misses of the request routes refuse it", func(t *testing.T) {
		for _, r := range [][2]string{{"GET", "/api/v1/asks/7/parts"}, {"GET", "/api/v1/asks/"}, {"GET", "/api/v1/asks/7/"}, {"PUT", "/api/v1/asks/7"}, {"GET", "/api/v1/asks/limit/"}} {
			if code, e := keyCall(t, f, ownKey, r[0], r[1], asAna); code != 400 || e.Code != "as_user_route" {
				t.Errorf("%s %s: %d %+v", r[0], r[1], code, e)
			}
		}
		// HEAD answers carry no body to read a code from.
		if code, _ := keyCall(t, f, ownKey, "HEAD", "/api/v1/asks", asAna); code != 400 {
			t.Errorf("HEAD /api/v1/asks: %d", code)
		}
	})
	t.Run("a locked person is refused", func(t *testing.T) {
		setUserState(t, f, anaID, "locked")
		defer setUserState(t, f, anaID, "active")
		if code, e := keyCall(t, f, ownKey, "POST", "/api/v1/asks", asAna); code != 403 || e.Code != "user_off" || e.UserID != 0 {
			t.Fatalf("%d %+v", code, e)
		}
	})
	t.Run("every request route takes it", func(t *testing.T) {
		for _, r := range [][2]string{{"POST", "/api/v1/asks"}, {"GET", "/api/v1/asks"}, {"GET", "/api/v1/asks/limit"}, {"GET", "/api/v1/asks/7"}, {"DELETE", "/api/v1/asks/7"}} {
			if code, e := keyCall(t, f, ownKey, r[0], r[1], asAna); code != 200 || e.UserID != anaID {
				t.Errorf("%s %s: %d %+v", r[0], r[1], code, e)
			}
		}
	})
	t.Run("other routes refuse it", func(t *testing.T) {
		for _, r := range [][2]string{{"POST", "/api/v1/asks/remove"}, {"POST", "/api/v1/asks/7/approve"}, {"GET", "/api/v1/settings/asks"}, {"GET", "/api/v1/titles"}, {"GET", "/api/v1/auth/me"}} {
			if code, e := keyCall(t, f, ownKey, r[0], r[1], asAna); code != 400 || e.Code != "as_user_route" || e.UserID != 0 {
				t.Errorf("%s %s: %d %+v", r[0], r[1], code, e)
			}
		}
	})
	t.Run("a member's key cannot name someone else", func(t *testing.T) {
		if code, e := keyCall(t, f, anaKey, "POST", "/api/v1/asks", "jellyfin:"+jfOwner); code != 403 || e.Code != "as_user_key" {
			t.Fatalf("%d %+v", code, e)
		}
	})
	t.Run("a value that is not jellyfin:<id> is refused", func(t *testing.T) {
		for _, v := range []string{"emby:" + jfAna, "jellyfin:", "jellyfin:xyz", jfAna} {
			if code, e := keyCall(t, f, ownKey, "POST", "/api/v1/asks", v); code != 400 || e.Code != "as_user_value" {
				t.Errorf("%q: %d %+v", v, code, e)
			}
		}
	})
	t.Run("an unlinked user is refused, never the key's owner", func(t *testing.T) {
		if code, e := keyCall(t, f, ownKey, "POST", "/api/v1/asks", "jellyfin:"+jfNobody); code != 404 || e.Code != "user_not_linked" || e.UserID != 0 {
			t.Fatalf("%d %+v", code, e)
		}
	})
	t.Run("an empty header is no header", func(t *testing.T) {
		for _, v := range []string{"", "   "} {
			if code, e := keyCall(t, f, ownKey, "POST", "/api/v1/asks", v); code != 200 || e.UserID != ownerID || e.OnBehalf {
				t.Errorf("%q: %d %+v", v, code, e)
			}
		}
	})
	t.Run("only the first header line counts", func(t *testing.T) {
		if code, e := keyCall(t, f, ownKey, "POST", "/api/v1/asks", asAna, "jellyfin:"+jfNobody); code != 200 || e.UserID != anaID {
			t.Fatalf("%d %+v", code, e)
		}
	})
	t.Run("an admin's key may name a person who may do no more than the admin", func(t *testing.T) {
		if code, e := keyCall(t, f, boKey, "POST", "/api/v1/asks", asAna); code != 200 || e.UserID != anaID || !e.OnBehalf {
			t.Fatalf("%d %+v", code, e)
		}
	})
	t.Run("an admin's key cannot name the owner", func(t *testing.T) {
		// Else the admin would ask, list and remove with the owner's rights.
		for _, r := range [][2]string{{"POST", "/api/v1/asks"}, {"DELETE", "/api/v1/asks/7"}} {
			if code, e := keyCall(t, f, boKey, r[0], r[1], "jellyfin:"+jfOwner); code != 403 || e.Code != "as_user_above" || e.UserID != 0 {
				t.Errorf("%s %s: %d %+v", r[0], r[1], code, e)
			}
		}
	})
	t.Run("a key cannot name a person with a permission its maker lacks", func(t *testing.T) {
		// cy may approve; dee, an admin who may not, cannot borrow that.
		join(t, f, invite(t, f, own, `{"role":"member","perms":["ask","approve"]}`), "cy")
		const jfCy = "00000000000000000000000000000c1c"
		linkJellyfin(t, f, "cy", jfCy)
		dee := join(t, f, invite(t, f, own, `{"role":"admin","perms":["ask","settings"]}`), "dee")
		deeKey, _ := makeKey(t, f, dee, `{"label":"dee's"}`)
		if code, e := keyCall(t, f, deeKey, "GET", "/api/v1/asks", "jellyfin:"+jfCy); code != 403 || e.Code != "as_user_above" {
			t.Fatalf("%d %+v", code, e)
		}
		if code, e := keyCall(t, f, ownKey, "GET", "/api/v1/asks", "jellyfin:"+jfCy); code != 200 || e.Name != "cy" {
			t.Fatalf("owner's key: %d %+v", code, e)
		}
	})
	t.Run("a key frozen until two-factor is on stays frozen with the header", func(t *testing.T) {
		f.a.requireAdmin2FA.Store(true)
		defer f.a.requireAdmin2FA.Store(false)
		plain, _ := keyCall(t, f, boKey, "GET", "/api/v1/asks")
		with, e := keyCall(t, f, boKey, "GET", "/api/v1/asks", asAna)
		if plain != 403 || with != 403 || e.UserID != 0 || !strings.Contains(e.Error, "two-factor") {
			t.Fatalf("bo's key: %d without, %d with the header (%+v)", plain, with, e)
		}
	})
	t.Run("one Jellyfin id linked to two people is refused", func(t *testing.T) {
		// ana's link is stored with dashes; the same id without them, on someone
		// else, is a second row the table allows. Neither person is picked.
		join(t, f, invite(t, f, own, `{"role":"member"}`), "eve")
		eveID := linkJellyfin(t, f, "eve", jfAna)
		defer func() {
			_ = f.s.WriteCore(context.Background(), func(tx *sql.Tx) error {
				_, err := tx.Exec("DELETE FROM user_links WHERE user_id = ?", eveID)
				return err
			})
		}()
		if code, e := keyCall(t, f, ownKey, "POST", "/api/v1/asks", asAna); code != 409 || e.Code != "user_link_unclear" || e.UserID != 0 {
			t.Fatalf("%d %+v", code, e)
		}
	})
	t.Run("a browser session ignores it", func(t *testing.T) {
		resp, out := call(t, own.c, "GET", f.srv.URL+"/api/v1/asks", "", map[string]string{AsUserHeader: asAna})
		var e echoed
		jsonOf(t, out, &e)
		if resp.StatusCode != 200 || e.UserID != ownerID || e.OnBehalf {
			t.Fatalf("%d %s", resp.StatusCode, out)
		}
	})
	t.Run("no key and no session is still a sign-in refusal", func(t *testing.T) {
		if code, _ := keyCall(t, f, "", "POST", "/api/v1/asks", asAna); code != 401 {
			t.Fatalf("%d", code)
		}
	})
	t.Run("a turned-off person is refused", func(t *testing.T) {
		setUserState(t, f, anaID, "disabled")
		if code, e := keyCall(t, f, ownKey, "POST", "/api/v1/asks", asAna); code != 403 || e.Code != "user_off" || e.UserID != 0 {
			t.Fatalf("%d %+v", code, e)
		}
	})
}
