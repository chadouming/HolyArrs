package accounts

import (
	"context"
	"database/sql"
	"errors"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/webserver"
)

// errLinkUnclear means one outside account is linked to two people.
var errLinkUnclear = errors.New("the outside account is linked to more than one person")

// linkedCaller reads the person linked to an outside account into who and
// returns their stored permissions and state; sql.ErrNoRows when the account
// is linked to no one. extID is in parseAsUser's form, and the stored id is
// compared in that form too. A turned-off person is returned, not hidden,
// so the caller can say why the request is refused.
//
// The table keeps ids as they were written, so the same id with and without
// dashes can sit on two people. That is errLinkUnclear, never a pick: the
// request would land under whichever row came first.
func linkedCaller(ctx context.Context, q conn, provider, extID string, who *webserver.Caller) (perms, state string, err error) {
	rows, err := q.QueryContext(ctx, `SELECT `+userCols+`, u.state FROM user_links l JOIN users u ON u.id = l.user_id
		WHERE l.provider = ? AND replace(lower(l.external_id), '-', '') = ? LIMIT 2`, provider, extID)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		if n++; n > 1 {
			return "", "", errLinkUnclear
		}
		if err := rows.Scan(&who.UserID, &who.Name, &who.Role, &perms, &who.TwoFactor, &who.MaxRating, &who.RatingCountry, &state); err != nil {
			return "", "", err
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	if n == 0 {
		return "", "", sql.ErrNoRows
	}
	return perms, state, nil
}
