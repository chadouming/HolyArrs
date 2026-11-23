package transcode

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

// Profile deletion guard errors.
var (
	ErrLastProfile    = errors.New("cannot delete the only transcode profile")
	ErrDefaultProfile = errors.New("cannot delete the default profile; mark another profile as default first")
)

// ProfileConfig defines video/audio/subtitle/VMAF encoding rules for a profile.
type ProfileConfig struct {
	TargetCodec          string   `json:"target_codec"`
	PreferredEncoder     string   `json:"preferred_encoder"`
	HWPreset             string   `json:"hw_preset"`
	TenBit               bool     `json:"ten_bit"`
	Normalize1080p       bool     `json:"normalize_1080p"`
	MaxHeight            int      `json:"max_height"`
	ScaleMode            string   `json:"scale_mode"`
	Pillarbox43          bool     `json:"pillarbox_4_3"`
	HDRMode              string   `json:"hdr_mode"`
	VMAFEnabled          bool     `json:"vmaf_enabled"`
	VMAFMinScore         float64  `json:"vmaf_min_score"`
	VMAFSamples          int      `json:"vmaf_samples"`
	VMAFSampleSeconds    int      `json:"vmaf_sample_seconds"`
	VMAFFPS              int      `json:"vmaf_fps"`
	QMin                 int      `json:"q_min"`
	QMax                 int      `json:"q_max"`
	MaxIterations        int      `json:"max_iterations"`
	CPUPreset            int      `json:"cpu_preset"`
	CPUCRF               int      `json:"cpu_crf"`
	MaxBitrateKbps       int      `json:"max_bitrate_kbps"`
	CopyIfLarger         bool     `json:"copy_if_larger"`
	SkipTags             []string `json:"skip_tags"`
	WriteTag             string   `json:"write_tag"`
	KeepOriginalHolding  bool     `json:"keep_original_in_holding"`
	AllowedLanguages     []string `json:"allowed_languages"`
	PreferVFQ            bool     `json:"prefer_vfq"`
	AudioCodec           string   `json:"audio_codec"`
	AudioBitrateKbps     int      `json:"audio_bitrate_kbps"`
	AudioChannels        int      `json:"audio_channels"`
	KeepAnimeAttachments bool     `json:"keep_anime_attachments"`
	ApplyEDLCuts         bool     `json:"apply_edl_cuts"`
	RequireAudioStream   bool     `json:"require_audio_stream"`
	MinFileSizeMB        int      `json:"min_file_size_mb"`
}

// Profile is one named transcoding profile.
type Profile struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	IsDefault   bool   `json:"is_default"`
	ProfileConfig
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ProfileInput is the request payload for creating or updating a profile.
// Supports both flat profile fields and a nested config object.
type ProfileInput struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	IsDefault   bool            `json:"is_default"`
	Config      *ProfileConfig  `json:"config"`
	Raw         json.RawMessage `json:"-"`
}

// UnmarshalJSON preserves raw JSON so flat or nested profile fields can overlay defaults cleanly.
func (p *ProfileInput) UnmarshalJSON(data []byte) error {
	type alias ProfileInput
	var aux alias
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*p = ProfileInput(aux)
	p.Raw = append([]byte(nil), data...)
	return nil
}

func applyProfileInputConfig(base ProfileConfig, in ProfileInput) ProfileConfig {
	if in.Config != nil {
		prevSkip := append([]string(nil), base.SkipTags...)
		prevWrite := base.WriteTag
		base = *in.Config
		if len(in.Raw) > 0 {
			var outer map[string]json.RawMessage
			if json.Unmarshal(in.Raw, &outer) == nil {
				if cfgRaw, ok := outer["config"]; ok {
					var cfgObj map[string]json.RawMessage
					if json.Unmarshal(cfgRaw, &cfgObj) == nil {
						if _, has := cfgObj["skip_tags"]; !has && base.SkipTags == nil {
							base.SkipTags = prevSkip
						}
						if _, has := cfgObj["write_tag"]; !has && base.WriteTag == "" {
							base.WriteTag = prevWrite
						}
						applyLegacyTagOverlay(cfgObj, &base.SkipTags, &base.WriteTag)
					}
				}
			}
		}
		return base
	}
	if len(in.Raw) > 0 {
		_ = json.Unmarshal(in.Raw, &base)
		var obj map[string]json.RawMessage
		if json.Unmarshal(in.Raw, &obj) == nil {
			applyLegacyTagOverlay(obj, &base.SkipTags, &base.WriteTag)
		}
	}
	return base
}

// LibraryAssignment describes a library folder's transcode enablement and profile.
type LibraryAssignment struct {
	LibraryFolderID int64  `json:"library_folder_id"`
	Label           string `json:"label"`
	Path            string `json:"path"`
	Kind            string `json:"kind"`
	Enabled         bool   `json:"enabled"`
	ProfileID       *int64 `json:"profile_id"`
	ProfileName     string `json:"profile_name"`
}

// DefaultProfileConfig returns the built-in Alchemist favorite profile settings.
func DefaultProfileConfig() ProfileConfig {
	return ProfileConfig{
		TargetCodec:          "av1",
		PreferredEncoder:     "auto",
		HWPreset:             "medium",
		TenBit:               true,
		Normalize1080p:       true,
		MaxHeight:            1080,
		ScaleMode:            "normalize",
		Pillarbox43:          true,
		HDRMode:              "preserve",
		VMAFEnabled:          true,
		VMAFMinScore:         93.0,
		VMAFSamples:          4,
		VMAFSampleSeconds:    20,
		VMAFFPS:              12,
		QMin:                 15,
		QMax:                 25,
		MaxIterations:        5,
		CPUPreset:            4,
		CPUCRF:               24,
		MaxBitrateKbps:       10000,
		CopyIfLarger:         false,
		SkipTags:             DefaultSkipTags(),
		WriteTag:             DefaultWriteTag,
		KeepOriginalHolding:  false,
		AllowedLanguages:     []string{"eng", "fra", "jpn", "und"},
		PreferVFQ:            true,
		AudioCodec:           "aac",
		AudioBitrateKbps:     192,
		AudioChannels:        2,
		KeepAnimeAttachments: true,
		ApplyEDLCuts:         true,
		RequireAudioStream:   true,
		MinFileSizeMB:        0,
	}
}

// ApplySettingsOverlay syncs overlapping global settings fields onto a ProfileConfig.
func (c *ProfileConfig) ApplySettingsOverlay(st Settings) {
	c.PreferredEncoder = st.PreferredEncoder
	c.HWPreset = st.HWPreset
	c.Normalize1080p = st.Normalize1080p
	if !st.Normalize1080p {
		c.ScaleMode = "none"
	} else if c.ScaleMode == "none" {
		c.ScaleMode = "normalize"
		if c.MaxHeight == 0 {
			c.MaxHeight = 1080
		}
	}
	c.Pillarbox43 = st.Pillarbox43
	c.HDRMode = st.HDRMode
	c.VMAFEnabled = st.VMAFEnabled
	c.VMAFMinScore = st.VMAFMinScore
	c.VMAFSamples = st.VMAFSamples
	c.VMAFSampleSeconds = st.VMAFSampleSeconds
	c.VMAFFPS = st.VMAFFPS
	c.QMin = st.QMin
	c.QMax = st.QMax
	c.MaxIterations = st.MaxIterations
	c.CPUPreset = st.SVTAV1Preset
	c.CPUCRF = st.SVTAV1CRF
	c.MaxBitrateKbps = st.MaxBitrateKbps
	c.CopyIfLarger = st.CopyIfLarger
	c.SkipTags = append([]string(nil), st.SkipTags...)
	c.WriteTag = st.WriteTag
	c.KeepOriginalHolding = st.KeepOriginalHolding
	c.AllowedLanguages = append([]string(nil), st.AllowedLanguages...)
	c.PreferVFQ = st.PreferVFQ
	c.AudioBitrateKbps = st.AudioBitrateKbps
	c.AudioChannels = st.AudioChannels
	c.KeepAnimeAttachments = st.KeepAnimeAttachments
	c.ApplyEDLCuts = st.ApplyEDLCuts
	c.RequireAudioStream = st.RequireAudioStream
	c.MinFileSizeMB = st.MinFileSizeMB
}

// Validate normalizes and validates a ProfileConfig.
func (c *ProfileConfig) Validate() error {
	c.TargetCodec = strings.TrimSpace(strings.ToLower(c.TargetCodec))
	if c.TargetCodec == "" {
		c.TargetCodec = "av1"
	}
	switch c.TargetCodec {
	case "av1", "hevc", "h264":
	default:
		return fmt.Errorf("target_codec must be av1, hevc, or h264")
	}
	c.PreferredEncoder = strings.TrimSpace(strings.ToLower(c.PreferredEncoder))
	if c.PreferredEncoder == "" {
		c.PreferredEncoder = "auto"
	}
	if !isSupportedEncoder(c.PreferredEncoder) {
		return fmt.Errorf("preferred_encoder is not supported: %s", c.PreferredEncoder)
	}
	hp, err := normalizeHWPreset(c.HWPreset)
	if err != nil {
		return err
	}
	c.HWPreset = hp
	c.ScaleMode = strings.TrimSpace(strings.ToLower(c.ScaleMode))
	if c.ScaleMode == "" {
		if c.Normalize1080p {
			c.ScaleMode = "normalize"
		} else {
			c.ScaleMode = "none"
		}
	}
	switch c.ScaleMode {
	case "normalize", "downscale_only", "none":
	default:
		return fmt.Errorf("scale_mode must be normalize, downscale_only, or none")
	}
	if c.MaxHeight < 0 || c.MaxHeight > 4320 {
		return fmt.Errorf("max_height must be between 0 and 4320")
	}
	if c.ScaleMode != "none" && c.MaxHeight == 0 {
		c.MaxHeight = 1080
	}
	c.HDRMode = strings.TrimSpace(strings.ToLower(c.HDRMode))
	if c.HDRMode == "" {
		c.HDRMode = "preserve"
	}
	if c.HDRMode != "preserve" && c.HDRMode != "tonemap_sdr" {
		return fmt.Errorf("hdr_mode must be preserve or tonemap_sdr")
	}
	if c.VMAFMinScore < 50 || c.VMAFMinScore > 100 {
		return fmt.Errorf("vmaf_min_score must be between 50 and 100")
	}
	if c.VMAFSamples < 1 || c.VMAFSamples > 12 {
		return fmt.Errorf("vmaf_samples must be between 1 and 12")
	}
	if c.VMAFSampleSeconds < 3 || c.VMAFSampleSeconds > 120 {
		return fmt.Errorf("vmaf_sample_seconds must be between 3 and 120")
	}
	if c.VMAFFPS < 1 || c.VMAFFPS > 60 {
		return fmt.Errorf("vmaf_fps must be between 1 and 60")
	}
	if c.QMin < 1 || c.QMin > 63 || c.QMax < 1 || c.QMax > 63 || c.QMin > c.QMax {
		return fmt.Errorf("q_min and q_max must be between 1 and 63 with q_min <= q_max")
	}
	if c.MaxIterations < 1 || c.MaxIterations > 12 {
		return fmt.Errorf("max_iterations must be between 1 and 12")
	}
	if c.CPUPreset < 0 || c.CPUPreset > 13 {
		return fmt.Errorf("cpu_preset must be between 0 and 13")
	}
	if c.CPUCRF < 0 || c.CPUCRF > 63 {
		return fmt.Errorf("cpu_crf must be between 0 and 63")
	}
	if c.MaxBitrateKbps < 100 || c.MaxBitrateKbps > 100000 {
		return fmt.Errorf("max_bitrate_kbps must be between 100 and 100000")
	}
	c.AudioCodec = strings.TrimSpace(strings.ToLower(c.AudioCodec))
	if c.AudioCodec == "" {
		c.AudioCodec = "aac"
	}
	switch c.AudioCodec {
	case "aac", "opus", "copy":
	default:
		return fmt.Errorf("audio_codec must be aac, opus, or copy")
	}
	if c.AudioBitrateKbps < 32 || c.AudioBitrateKbps > 1536 {
		return fmt.Errorf("audio_bitrate_kbps must be between 32 and 1536")
	}
	if c.AudioChannels < 1 || c.AudioChannels > 8 {
		return fmt.Errorf("audio_channels must be between 1 and 8")
	}
	if c.MinFileSizeMB < 0 {
		return fmt.Errorf("min_file_size_mb must be >= 0")
	}
	c.AllowedLanguages = cleanLanguages(c.AllowedLanguages)
	cleanedTags, err := cleanSkipTags(c.SkipTags)
	if err != nil {
		return err
	}
	c.SkipTags = cleanedTags
	writeTag, err := validateWriteTag(c.WriteTag)
	if err != nil {
		return err
	}
	c.WriteTag = writeTag
	return nil
}

func rowToProfile(r profileRow) Profile {
	cfg := DefaultProfileConfig()
	if r.ConfigJSON != "" {
		_ = json.Unmarshal([]byte(r.ConfigJSON), &cfg)
		var obj map[string]json.RawMessage
		if json.Unmarshal([]byte(r.ConfigJSON), &obj) == nil {
			applyLegacyTagOverlay(obj, &cfg.SkipTags, &cfg.WriteTag)
		}
	}
	if hp, err := normalizeHWPreset(cfg.HWPreset); err == nil {
		cfg.HWPreset = hp
	} else {
		cfg.HWPreset = "medium"
	}
	if cleaned, err := cleanSkipTags(cfg.SkipTags); err == nil {
		cfg.SkipTags = cleaned
	} else {
		cfg.SkipTags = DefaultSkipTags()
	}
	if wt, err := validateWriteTag(cfg.WriteTag); err == nil {
		cfg.WriteTag = wt
	} else {
		cfg.WriteTag = DefaultWriteTag
	}
	if cfg.AllowedLanguages == nil {
		cfg.AllowedLanguages = []string{}
	}
	return Profile{
		ID:            r.ID,
		Name:          r.Name,
		Description:   r.Description,
		IsDefault:     r.IsDefault,
		ProfileConfig: cfg,
		CreatedAt:     r.CreatedAt,
		UpdatedAt:     r.UpdatedAt,
	}
}

// EnsureDefaultProfile guarantees at least one built-in Default profile exists.
func (s *Service) EnsureDefaultProfile(ctx context.Context) error {
	if count, err := countProfiles(ctx, s.Store.Read()); err == nil && count > 0 {
		return nil
	}
	return s.Store.Write(ctx, func(tx *sql.Tx) error {
		return ensureDefaultProfileTx(ctx, tx)
	})
}

// ListProfiles returns all profiles and per-library-folder assignments.
func (s *Service) ListProfiles(ctx context.Context) ([]Profile, []LibraryAssignment, error) {
	if err := s.EnsureDefaultProfile(ctx); err != nil {
		return nil, nil, err
	}
	db := s.Store.Read()
	pRows, err := listProfileRows(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	profiles := make([]Profile, 0, len(pRows))
	byID := make(map[int64]Profile, len(pRows))
	var defaultProf Profile
	for _, r := range pRows {
		p := rowToProfile(r)
		profiles = append(profiles, p)
		byID[p.ID] = p
		if p.IsDefault || defaultProf.ID == 0 {
			defaultProf = p
		}
	}
	folders, err := mediafiles.ListTranscodeLibraryFolders(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	assigns, err := listLibraryProfileRows(ctx, db)
	if err != nil {
		return nil, nil, err
	}
	libOut := make([]LibraryAssignment, 0, len(folders))
	for _, lf := range folders {
		la := LibraryAssignment{
			LibraryFolderID: lf.ID,
			Label:           lf.Label,
			Path:            lf.Path,
			Kind:            lf.Kind,
			Enabled:         true,
			ProfileID:       nil,
			ProfileName:     defaultProf.Name,
		}
		if row, ok := assigns[lf.ID]; ok {
			la.Enabled = row.Enabled
			if row.ProfileID != nil {
				if p, exists := byID[*row.ProfileID]; exists {
					pid := p.ID
					la.ProfileID = &pid
					la.ProfileName = p.Name
				}
			}
		}
		libOut = append(libOut, la)
	}
	return profiles, libOut, nil
}

// ResolveFolderProfile returns whether transcoding is enabled for libraryFolderID and the effective Profile.
func (s *Service) ResolveFolderProfile(ctx context.Context, libraryFolderID int64) (enabled bool, prof Profile, err error) {
	if err = s.EnsureDefaultProfile(ctx); err != nil {
		return false, prof, err
	}
	db := s.Store.Read()
	pRows, err := listProfileRows(ctx, db)
	if err != nil {
		return false, prof, err
	}
	byID := make(map[int64]Profile, len(pRows))
	for _, r := range pRows {
		p := rowToProfile(r)
		byID[p.ID] = p
		if p.IsDefault || prof.ID == 0 {
			prof = p
		}
	}
	enabled = true
	if libraryFolderID <= 0 {
		return enabled, prof, nil
	}
	assigns, err := listLibraryProfileRows(ctx, db)
	if err != nil {
		return enabled, prof, nil
	}
	if row, ok := assigns[libraryFolderID]; ok {
		enabled = row.Enabled
		if row.ProfileID != nil {
			if p, exists := byID[*row.ProfileID]; exists {
				prof = p
			}
		}
	}
	return enabled, prof, nil
}

// CreateProfile creates a new transcode profile.
func (s *Service) CreateProfile(ctx context.Context, in ProfileInput, by domain.Actor) (Profile, error) {
	if err := s.EnsureDefaultProfile(ctx); err != nil {
		return Profile{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 80 {
		return Profile{}, fmt.Errorf("profile name must be between 1 and 80 characters")
	}
	cfg := applyProfileInputConfig(DefaultProfileConfig(), in)
	if err := cfg.Validate(); err != nil {
		return Profile{}, err
	}
	b, _ := json.Marshal(cfg)
	row := &profileRow{
		Name:        name,
		Description: strings.TrimSpace(in.Description),
		IsDefault:   in.IsDefault,
		ConfigJSON:  string(b),
	}
	err := s.Store.Write(ctx, func(tx *sql.Tx) error {
		if err := insertProfileTx(ctx, tx, row, by); err != nil {
			return err
		}
		if row.IsDefault {
			return syncSettingsFromDefaultProfileTx(ctx, tx, cfg, by)
		}
		return nil
	})
	if err != nil {
		return Profile{}, err
	}
	fresh, err := getProfileRow(ctx, s.Store.Read(), row.ID)
	if err != nil {
		return Profile{}, err
	}
	s.notifyConcurrency()
	return rowToProfile(fresh), nil
}

// UpdateProfile updates an existing transcode profile.
func (s *Service) UpdateProfile(ctx context.Context, id int64, in ProfileInput, by domain.Actor) (Profile, error) {
	if err := s.EnsureDefaultProfile(ctx); err != nil {
		return Profile{}, err
	}
	cur, err := getProfileRow(ctx, s.Store.Read(), id)
	if err != nil {
		return Profile{}, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = cur.Name
	}
	if len(name) > 80 {
		return Profile{}, fmt.Errorf("profile name must be between 1 and 80 characters")
	}
	cfg := applyProfileInputConfig(rowToProfile(cur).ProfileConfig, in)
	if err := cfg.Validate(); err != nil {
		return Profile{}, err
	}
	b, _ := json.Marshal(cfg)
	cur.Name = name
	cur.Description = strings.TrimSpace(in.Description)
	if in.IsDefault {
		cur.IsDefault = true
	}
	cur.ConfigJSON = string(b)
	if err := s.Store.Write(ctx, func(tx *sql.Tx) error {
		if err := updateProfileTx(ctx, tx, &cur, by); err != nil {
			return err
		}
		if cur.IsDefault {
			return syncSettingsFromDefaultProfileTx(ctx, tx, cfg, by)
		}
		return nil
	}); err != nil {
		return Profile{}, err
	}
	fresh, err := getProfileRow(ctx, s.Store.Read(), id)
	if err != nil {
		return Profile{}, err
	}
	s.notifyConcurrency()
	return rowToProfile(fresh), nil
}

// DeleteProfile deletes a non-default profile.
func (s *Service) DeleteProfile(ctx context.Context, id int64) error {
	return s.Store.Write(ctx, func(tx *sql.Tx) error {
		return deleteProfileTx(ctx, tx, id)
	})
}

// SetLibraryFolderProfile assigns enablement and profile for a library folder.
func (s *Service) SetLibraryFolderProfile(ctx context.Context, folderID int64, enabled bool, profileID *int64, by domain.Actor) (LibraryAssignment, error) {
	if err := s.EnsureDefaultProfile(ctx); err != nil {
		return LibraryAssignment{}, err
	}
	folders, err := mediafiles.ListTranscodeLibraryFolders(ctx, s.Store.Read())
	if err != nil {
		return LibraryAssignment{}, err
	}
	found := false
	for _, lf := range folders {
		if lf.ID == folderID {
			found = true
			break
		}
	}
	if !found {
		return LibraryAssignment{}, database.ErrNotFound
	}
	if err := upsertLibraryProfile(ctx, s.Store, folderID, enabled, profileID, by); err != nil {
		return LibraryAssignment{}, err
	}
	s.notifyConcurrency()
	_, list, err := s.ListProfiles(ctx)
	if err != nil {
		return LibraryAssignment{}, err
	}
	for _, item := range list {
		if item.LibraryFolderID == folderID {
			return item, nil
		}
	}
	return LibraryAssignment{}, database.ErrNotFound
}
