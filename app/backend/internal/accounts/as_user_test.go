package accounts

import (
	"context"
	"database/sql"
	"errors"
	"testing"

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
