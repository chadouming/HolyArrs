package accounts

import "strings"

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
