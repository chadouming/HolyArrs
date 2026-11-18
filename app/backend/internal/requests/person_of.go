package requests

import (
	"context"
	"net/http"
	"strings"
	"unicode"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/webserver"
)

// lineSafe drops what would break or hide part of a History line: control
// characters, line breaks and invisible ones (zero-width, text direction).
// A key's label is typed by whoever made the key and ends up in such a line.
func lineSafe(s string) string {
	return strings.TrimSpace(strings.Map(func(c rune) rune {
		if unicode.IsControl(c) || unicode.Is(unicode.Cf, c) {
			return -1
		}
		return c
	}, s))
}

// PersonOf is the signed-in person as Requests sees them. Without sign-in
// (tests) it is user 1 with every right, named by ownerName (nil: no name).
func PersonOf(r *http.Request, kidMax func(*http.Request) string, ownerName func(context.Context) string) Person {
	who, ok := webserver.From(r.Context())
	if !ok {
		var name string
		if ownerName != nil {
			name = ownerName(r.Context())
		}
		return Person{UserID: 1, Name: name, AutoApprove: true, AutoApproveFourK: true, NoLimit: true, Owner: true}
	}
	p := Person{UserID: who.UserID, Name: who.Name, AutoApprove: who.Has(webserver.PermAutoApprove), AutoApproveFourK: who.Has(webserver.PermAutoApproveFourK), NoLimit: who.Has(webserver.PermApprove), Owner: who.Role == webserver.RoleOwner}
	if who.OnBehalf {
		p.ViaKey = lineSafe(who.KeyLabel)
	}
	if kidMax != nil {
		p.MaxRating = kidMax(r)
	}
	return p
}
