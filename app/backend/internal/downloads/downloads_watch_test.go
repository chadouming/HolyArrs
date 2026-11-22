package downloads

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"

	"github.com/HolyArrs/holyarrs/app/backend/internal/accounts"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/webserver"
	"github.com/HolyArrs/holyarrs/app/backend/internal/metadata"
)

// viewAs reads the Downloads rows as one person, past the sign-in.
func viewAs(t *testing.T, x *dl41, who webserver.Caller) []DownloadRow {
	t.Helper()
	return viewAt(t, x, who, "/api/v1/downloads/view")
}

// viewAt is viewAs at a given address (?asked=1 for the Requests page).
func viewAt(t *testing.T, x *dl41, who webserver.Caller, url string) []DownloadRow {
	t.Helper()
	a := &Web{Store: x.s, Queue: x.q, Live: webserver.NewLiveHub(webserver.LiveOptions{Heartbeat: time.Second}), Metadata: metadata.Lab()}
	r := httptest.NewRequest("GET", url, nil)
	r = r.WithContext(webserver.ContextWith(r.Context(), who))
	w := httptest.NewRecorder()
	a.DownloadsView(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Downloads []DownloadRow `json:"downloads"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out.Downloads
}

// Everyone signed in may open Downloads; someone who may not manage the
// Library sees only the downloads for titles they asked for, with no actions.
func TestDownloadsViewOwnAsksOnly(t *testing.T) {
	t.Parallel()
	x := seed41(t, mediafiles.SlotDownloading, "downloading")
	exec41(t, x.s, `INSERT INTO users(id, created_at, created_by, updated_at, updated_by, name, role, password_hash) VALUES
		(1, 't', '{}', 't', '{}', 'owner', 'owner', 'x'), (2, 't', '{}', 't', '{}', 'ana', 'member', 'x'), (3, 't', '{}', 't', '{}', 'bo', 'member', 'x')`)
	exec41(t, x.s, `INSERT INTO asks (id, created_at, created_by, updated_at, updated_by, kind, tmdb_id, name, state, title_id, asked_by, asked_at)
		SELECT 1, 't', '{}', 't', '{}', 'movie', '1', 'Salt Choir', 'approved', t.id, 2, 't' FROM titles t LIMIT 1`)
	exec41(t, x.s, `INSERT INTO ask_people (ask_id, user_id, asked_at) VALUES (1, 2, 't')`)

	if got := accounts.RoutePerms["GET /api/v1/downloads/view"]; !(webserver.Caller{Role: webserver.RoleMember, Perms: []webserver.Perm{webserver.PermAsk}}).Has(got) {
		t.Fatalf("a member cannot open Downloads (%q)", got)
	}
	member := func(id int64) webserver.Caller {
		return webserver.Caller{UserID: id, Role: webserver.RoleMember, Perms: []webserver.Perm{webserver.PermAsk}, Via: "session"}
	}
	mine := viewAs(t, x, member(2))
	if len(mine) != 1 || mine[0].CanAct {
		t.Fatalf("asker rows %+v", mine)
	}
	if other := viewAs(t, x, member(3)); len(other) != 0 {
		t.Fatalf("someone else's download shown: %+v", other)
	}
	admin := viewAs(t, x, webserver.Caller{UserID: 1, Role: webserver.RoleAdmin, Perms: []webserver.Perm{webserver.PermManageLibrary}, Via: "session"})
	if len(admin) != 1 || !admin[0].CanAct {
		t.Fatalf("admin rows %+v", admin)
	}
	// Someone who approves (but does not run the Library) reads every
	// approved ask's downloads on the Requests page, as plain rows with no actions.
	approver := webserver.Caller{UserID: 3, Role: webserver.RoleMember, Perms: []webserver.Perm{webserver.PermAsk, webserver.PermApprove}, Via: "session"}
	if rows := viewAt(t, x, approver, "/api/v1/downloads/view?asked=1"); len(rows) != 1 || rows[0].CanAct {
		t.Fatalf("approver's Requests rows %+v", rows)
	}
	if rows := viewAs(t, x, approver); len(rows) != 0 {
		t.Fatalf("approver's own Downloads page shows someone else's download: %+v", rows)
	}
	// Every action and the swarm stay with people who manage the Library.
	for _, p := range []string{"POST /api/v1/downloads/{id}/mark-failed", "POST /api/v1/downloads/{id}/remove", "POST /api/v1/downloads/{id}/pause",
		"POST /api/v1/downloads/{id}/resume", "POST /api/v1/downloads/{id}/top", "POST /api/v1/downloads/{id}/recheck", "POST /api/v1/downloads/pause-all",
		"POST /api/v1/downloads/resume-all", "GET /api/v1/downloads/{id}/swarm", "DELETE /api/v1/turned-down/{id}", "POST /api/v1/needs-decision/{id}/decide"} {
		if member(2).Has(accounts.RoutePerms[p]) {
			t.Errorf("a member may use %s", p)
		}
	}
}

// A failed copy whose Slot moved on (searching again, or another copy on
// its way) is no problem any more; one with nothing left still is.
func TestDownloadsViewFailedReplaced(t *testing.T) {
	t.Parallel()
	x := seed41(t, mediafiles.SlotSearching, "failed")
	owner := webserver.Caller{UserID: 1, Role: webserver.RoleOwner, Via: "session"}
	if r := viewAs(t, x, owner); len(r) != 1 || r[0].Phase != "failed" || !r[0].Replaced {
		t.Fatalf("searching again: %+v", r)
	}
	exec41(t, x.s, "UPDATE slots SET state = 'needs_a_decision'")
	if r := viewAs(t, x, owner); len(r) != 1 || r[0].Replaced {
		t.Fatalf("nothing left: %+v", r)
	}
	exec41(t, x.s, "UPDATE slots SET state = 'failed'")
	if r := viewAs(t, x, owner); r[0].Replaced {
		t.Fatalf("just failed: %+v", r)
	}
}

// A Slot that went through several failed copies has one problem, not one
// per copy: only its newest download counts, the older ones were replaced.
func TestDownloadsViewOlderFailuresReplaced(t *testing.T) {
	t.Parallel()
	x := seed41(t, mediafiles.SlotNeedsADecision, "vanished")
	exec41(t, x.s, `INSERT INTO downloads (created_at, created_by, updated_at, updated_by, downloader_id, release_id, slot_ids, external_id, state, started_by)
		SELECT created_at, created_by, updated_at, updated_by, downloader_id, release_id, slot_ids, external_id || '-again', 'failed', started_by FROM downloads`)
	owner := webserver.Caller{UserID: 1, Role: webserver.RoleOwner, Via: "session"}
	r := viewAs(t, x, owner) // newest first
	if len(r) != 2 || r[0].Phase != "failed" || r[1].Phase != "vanished" {
		t.Fatalf("rows %+v", r)
	}
	if r[0].Replaced {
		t.Fatalf("the newest failure is still the Slot's problem: %+v", r[0])
	}
	if !r[1].Replaced {
		t.Fatalf("an older failure was replaced by the newer download: %+v", r[1])
	}
}

// A download that was imported is done. When a later download for the same
// Slot fails and the Slot waits for a decision, that is the later download's
// problem: the imported one is not blocked.
func TestDownloadsViewImportedStaysDoneAfterLaterFailure(t *testing.T) {
	t.Parallel()
	x := seed41(t, mediafiles.SlotNeedsADecision, "finished")
	owner := webserver.Caller{UserID: 1, Role: webserver.RoleOwner, Via: "session"}
	if r := viewAs(t, x, owner); len(r) != 1 || r[0].Phase != "needs_a_decision" {
		t.Fatalf("its own import waits for a decision: %+v", r)
	}
	exec41(t, x.s, `INSERT INTO downloads (created_at, created_by, updated_at, updated_by, downloader_id, release_id, slot_ids, external_id, state, started_by)
		SELECT created_at, created_by, updated_at, updated_by, downloader_id, release_id, slot_ids, external_id || '-again', 'vanished', started_by FROM downloads`)
	r := viewAs(t, x, owner) // newest first
	if len(r) != 2 || r[0].Phase != "vanished" || r[0].Replaced {
		t.Fatalf("the later failure is the Slot's problem: %+v", r)
	}
	if r[1].Phase != "done" {
		t.Fatalf("the imported download shows %q, want done", r[1].Phase)
	}
}
