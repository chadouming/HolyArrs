package accounts

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/webserver"
)

// AsUserHeader names the person a request counts as: an owner or admin API
// key sends jellyfin:<Jellyfin user id> and the call runs as the HolyArrs
// person linked to that account. An app that asks for many people with one
// key (a Jellyfin plugin) files each request under the right person this way.
const AsUserHeader = "X-HolyArrs-User"

// parseAsUser reads an AsUserHeader value. Jellyfin writes its user ids with
// or without dashes and in either case, so the id comes back in one form:
// 32 lower-case hex characters.
func parseAsUser(v string) (provider, extID string, ok bool) {
	p, id, found := strings.Cut(strings.TrimSpace(v), ":")
	if !found || p != ProviderJellyfin {
		return "", "", false
	}
	id = strings.ToLower(strings.ReplaceAll(id, "-", ""))
	if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		return "", "", false
	}
	return p, id, true
}

// asUserRoute says whether a route honours AsUserHeader: making an ask,
// reading the weekly limit, listing asks, reading one and taking one back.
// The list is short on purpose: a key that could act as someone else on
// every route would be a way into settings and accounts.
func asUserRoute(r *http.Request) bool {
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/asks")
	if !ok {
		return false
	}
	switch rest {
	case "":
		return r.Method == http.MethodGet || r.Method == http.MethodPost
	case "/limit":
		return r.Method == http.MethodGet
	}
	id, ok := strings.CutPrefix(rest, "/")
	if !ok || id == "" || strings.Trim(id, "0123456789") != "" {
		return false
	}
	return r.Method == http.MethodGet || r.Method == http.MethodDelete
}

// outranks says whether key's maker may do everything who may: the owner
// always, and anyone else only for a person who is not the owner and holds
// no permission the maker lacks.
func outranks(key, who webserver.Caller) bool {
	if key.Role == webserver.RoleOwner {
		return true
	}
	if who.Role == webserver.RoleOwner {
		return false
	}
	for _, p := range who.Perms {
		if !key.Has(p) {
			return false
		}
	}
	return true
}

// refuseAsUser answers a refused AsUserHeader. code is stable, so the app
// that sent the header can tell the refusals apart without reading words.
func refuseAsUser(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

// asUser turns an accepted API key's caller into the person AsUserHeader
// names. Without the header the key's own caller comes back. A refusal is
// answered here and false returned: the request is never quietly counted as
// the key's owner, or a person's asks would land under the wrong name.
func (a *Auth) asUser(w http.ResponseWriter, r *http.Request, key webserver.Caller) (webserver.Caller, bool) {
	v := strings.TrimSpace(r.Header.Get(AsUserHeader))
	if v == "" {
		return key, true
	}
	if !asUserRoute(r) {
		refuseAsUser(w, http.StatusBadRequest, "as_user_route", AsUserHeader+" is not accepted on this route")
		return webserver.Caller{}, false
	}
	// A key frozen until its admin turns on two-factor sign-in stays frozen:
	// the route gate would judge the named person and let it through.
	if key.MustTwoFactor {
		fail(w, http.StatusForbidden, "turn on two-factor sign-in in My account first")
		return webserver.Caller{}, false
	}
	// Anyone else's key could spend another person's weekly limit.
	if key.Role != webserver.RoleOwner && key.Role != webserver.RoleAdmin {
		refuseAsUser(w, http.StatusForbidden, "as_user_key", "only a key made by the owner or an admin can ask for someone else")
		return webserver.Caller{}, false
	}
	provider, extID, ok := parseAsUser(v)
	if !ok {
		refuseAsUser(w, http.StatusBadRequest, "as_user_value", "send "+AsUserHeader+" as jellyfin:<Jellyfin user id>")
		return webserver.Caller{}, false
	}
	var who webserver.Caller
	perms, state, err := linkedCaller(r.Context(), a.s.Read(), provider, extID, &who)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		refuseAsUser(w, http.StatusNotFound, "user_not_linked", "this Jellyfin user is not linked to a HolyArrs person")
		return webserver.Caller{}, false
	case errors.Is(err, errLinkUnclear):
		refuseAsUser(w, http.StatusConflict, "user_link_unclear", "this Jellyfin user is linked to more than one HolyArrs person")
		return webserver.Caller{}, false
	case err != nil:
		log.Printf("auth: %s: %v", AsUserHeader, err)
		fail(w, http.StatusInternalServerError, "could not look up the linked person")
		return webserver.Caller{}, false
	case state != "active":
		refuseAsUser(w, http.StatusForbidden, "user_off", "the HolyArrs person linked to this Jellyfin user is turned off")
		return webserver.Caller{}, false
	}
	a.fill(&who, perms)
	// A key acts only as people who may do no more than its maker. Else an
	// admin's key naming the owner, or someone who approves when the admin
	// does not, would ask, list and remove with rights the admin lacks.
	if !outranks(key, who) {
		refuseAsUser(w, http.StatusForbidden, "as_user_above", "this key cannot ask for a person who may do more than its maker")
		return webserver.Caller{}, false
	}
	// The key still decides what may be called; only who it counts as changes.
	who.Via, who.KeyID, who.KeyLabel, who.Scopes, who.OnBehalf = key.Via, key.KeyID, key.KeyLabel, key.Scopes, true
	return who, true
}
