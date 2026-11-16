package accounts

import (
	"context"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/webserver"
)

// linkedCaller reads the person linked to an outside account into who and
// returns their stored permissions and state; sql.ErrNoRows when the account
// is linked to no one. extID is in parseAsUser's form, and the stored id is
// compared in that form too. A turned-off person is returned, not hidden,
// so the caller can say why the request is refused.
func linkedCaller(ctx context.Context, q conn, provider, extID string, who *webserver.Caller) (perms, state string, err error) {
	err = q.QueryRowContext(ctx, `SELECT `+userCols+`, u.state FROM user_links l JOIN users u ON u.id = l.user_id
		WHERE l.provider = ? AND replace(lower(l.external_id), '-', '') = ?`, provider, extID).
		Scan(&who.UserID, &who.Name, &who.Role, &perms, &who.TwoFactor, &who.MaxRating, &who.RatingCountry, &state)
	return perms, state, err
}
