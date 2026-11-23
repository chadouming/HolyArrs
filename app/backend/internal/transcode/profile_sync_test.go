package transcode

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
)

func TestBidirectionalDefaultProfileSettingsSync(t *testing.T) {
	f := newTestFix(t)
	ctx := context.Background()

	// Set non-default global-only fields first to confirm profile sync preserves them.
	initSet := doReq(t, f.mux, "PUT", "/api/v1/transcode/settings", map[string]any{
		"auto_transcode":           true,
		"concurrent_jobs":          4,
		"gpu_devices":              []string{"/dev/dri/renderD129"},
		"selected_gpus":            []string{"/dev/dri/renderD129"},
		"allow_cpu_fallback":       false,
		"scratch_dir":              "/tmp/tc-scratch",
		"keep_original_in_holding": true,
	})
	if initSet.Code != http.StatusOK {
		t.Fatalf("PUT initial settings %d: %s", initSet.Code, initSet.Body.String())
	}

	// Create a non-default custom profile that must remain untouched by Settings updates.
	customRR := doReq(t, f.mux, "POST", "/api/v1/transcode/profiles", map[string]any{
		"name":           "Custom Archive",
		"vmaf_min_score": 91.5,
		"scale_mode":     "none",
	})
	if customRR.Code != http.StatusCreated {
		t.Fatalf("POST custom profile %d: %s", customRR.Code, customRR.Body.String())
	}
	var customProf Profile
	if err := json.Unmarshal(customRR.Body.Bytes(), &customProf); err != nil {
		t.Fatal(err)
	}

	// Find Default profile ID.
	profs, _, err := f.svc.ListProfiles(ctx)
	if err != nil || len(profs) < 2 {
		t.Fatalf("ListProfiles: %v len=%d", err, len(profs))
	}
	var defProf Profile
	for _, p := range profs {
		if p.IsDefault {
			defProf = p
			break
		}
	}
	if defProf.ID == 0 {
		t.Fatal("expected a default profile")
	}

	// 1. PUT Default profile with vmaf_min_score=95 updates GET /settings to 95.
	defCfg := defProf.ProfileConfig
	defCfg.VMAFMinScore = 95.0
	updDef := doReq(t, f.mux, "PUT", "/api/v1/transcode/profiles/"+strconv.FormatInt(defProf.ID, 10), ProfileInput{
		Name:   defProf.Name,
		Config: &defCfg,
	})
	if updDef.Code != http.StatusOK {
		t.Fatalf("PUT default profile %d: %s", updDef.Code, updDef.Body.String())
	}

	getSet := doReq(t, f.mux, "GET", "/api/v1/transcode/settings", nil)
	if getSet.Code != http.StatusOK {
		t.Fatalf("GET settings %d: %s", getSet.Code, getSet.Body.String())
	}
	var stAfterProf Settings
	if err := json.Unmarshal(getSet.Body.Bytes(), &stAfterProf); err != nil {
		t.Fatal(err)
	}
	if stAfterProf.VMAFMinScore != 95.0 {
		t.Fatalf("expected Settings.VMAFMinScore=95 after Default profile update, got %.1f", stAfterProf.VMAFMinScore)
	}
	if !stAfterProf.AutoTranscode || stAfterProf.ConcurrentJobs != 4 || stAfterProf.AllowCPUFallback || stAfterProf.ScratchDir != "/tmp/tc-scratch" || !slices.Equal(stAfterProf.SelectedGPUs, []string{"/dev/dri/renderD129"}) {
		t.Fatalf("expected global-only settings preserved, got %+v", stAfterProf)
	}

	// 2. Subsequent PUT /settings changing keep_original_in_holding=false and concurrent_jobs=3
	// must keep Default profile at vmaf_min_score=95.
	updSet := doReq(t, f.mux, "PUT", "/api/v1/transcode/settings", map[string]any{
		"keep_original_in_holding": false,
		"concurrent_jobs":          3,
	})
	if updSet.Code != http.StatusOK {
		t.Fatalf("PUT settings %d: %s", updSet.Code, updSet.Body.String())
	}
	profsAfterSet, _, err := f.svc.ListProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range profsAfterSet {
		if p.ID == defProf.ID {
			if p.VMAFMinScore != 95.0 || p.KeepOriginalHolding {
				t.Fatalf("expected Default profile to retain VMAFMinScore=95 and sync KeepOriginalHolding=false, got %+v", p)
			}
		}
		if p.ID == customProf.ID {
			if p.VMAFMinScore != 91.5 || p.ScaleMode != "none" {
				t.Fatalf("expected non-default custom profile untouched, got %+v", p)
			}
		}
	}

	// 3. Promoting a custom profile (vmaf_min_score=96, scale_mode="downscale_only") to is_default=true
	// updates Settings while preserving global-only fields.
	promoteRR := doReq(t, f.mux, "PUT", "/api/v1/transcode/profiles/"+strconv.FormatInt(customProf.ID, 10), map[string]any{
		"name":           customProf.Name,
		"is_default":     true,
		"vmaf_min_score": 96.0,
		"scale_mode":     "downscale_only",
	})
	if promoteRR.Code != http.StatusOK {
		t.Fatalf("PUT promote profile %d: %s", promoteRR.Code, promoteRR.Body.String())
	}
	getAfterPromote := doReq(t, f.mux, "GET", "/api/v1/transcode/settings", nil)
	if getAfterPromote.Code != http.StatusOK {
		t.Fatalf("GET settings after promote %d: %s", getAfterPromote.Code, getAfterPromote.Body.String())
	}
	var stPromoted Settings
	if err := json.Unmarshal(getAfterPromote.Body.Bytes(), &stPromoted); err != nil {
		t.Fatal(err)
	}
	if stPromoted.VMAFMinScore != 96.0 || !stPromoted.Normalize1080p {
		t.Fatalf("expected Settings synced from promoted default (vmaf=96, normalize_1080p=true), got %+v", stPromoted)
	}
	if !stPromoted.AutoTranscode || stPromoted.ConcurrentJobs != 3 || stPromoted.AllowCPUFallback || stPromoted.ScratchDir != "/tmp/tc-scratch" || !slices.Equal(stPromoted.SelectedGPUs, []string{"/dev/dri/renderD129"}) {
		t.Fatalf("expected global-only settings preserved on promotion, got %+v", stPromoted)
	}

	// 4. Creating a new profile directly with is_default=true also syncs Settings.
	newDefRR := doReq(t, f.mux, "POST", "/api/v1/transcode/profiles", map[string]any{
		"name":           "Direct Default",
		"is_default":     true,
		"vmaf_min_score": 94.5,
		"scale_mode":     "none",
	})
	if newDefRR.Code != http.StatusCreated {
		t.Fatalf("POST new default profile %d: %s", newDefRR.Code, newDefRR.Body.String())
	}
	stCreated := ReadSettings(ctx, f.st.Read())
	if stCreated.VMAFMinScore != 94.5 || stCreated.Normalize1080p {
		t.Fatalf("expected Settings synced from created default (vmaf=94.5, normalize_1080p=false), got %+v", stCreated)
	}
	if stCreated.ConcurrentJobs != 3 || stCreated.ScratchDir != "/tmp/tc-scratch" {
		t.Fatalf("expected global-only fields preserved after created default, got %+v", stCreated)
	}
}

func TestEnsureDefaultProfileTx_OverlaysExistingSettings(t *testing.T) {
	st, err := database.Open(t.TempDir(), database.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	seed := DefaultSettings()
	seed.VMAFMinScore = 97.0
	seed.Normalize1080p = false
	b, _ := json.Marshal(seed)
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		return database.PutSetting(tx, settingsGroup, settingsKey, string(b), domain.System, time.Now().UTC())
	}); err != nil {
		t.Fatal(err)
	}

	svc := NewService(st, nil, nil)
	profs, _, err := svc.ListProfiles(ctx)
	if err != nil || len(profs) != 1 {
		t.Fatalf("ListProfiles: %v len=%d", err, len(profs))
	}
	if profs[0].VMAFMinScore != 97.0 || profs[0].ScaleMode != "none" || profs[0].Normalize1080p {
		t.Fatalf("expected seeded default profile to overlay existing transcode_settings, got %+v", profs[0])
	}
}
