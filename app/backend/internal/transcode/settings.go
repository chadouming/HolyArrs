package transcode

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
)

const (
	settingsGroup = "what_to_pick"
	settingsKey   = "transcode_settings"

	// DefaultWriteTag is the default container comment tag written to transcoded files.
	DefaultWriteTag = "HolyArrs"
)

// DefaultSkipTags returns the default tag substrings that mark a file as already transcoded.
func DefaultSkipTags() []string {
	return []string{"HolyArrs", "Created by FileFlows"}
}

// Settings holds global transcoding options and default pipeline parameters.
type Settings struct {
	AutoTranscode        bool     `json:"auto_transcode"`
	ConcurrentJobs       int      `json:"concurrent_jobs"`
	PreferredEncoder     string   `json:"preferred_encoder"`
	HWPreset             string   `json:"hw_preset"`
	GPUDevices           string   `json:"gpu_devices"`
	SelectedGPUs         []string `json:"selected_gpus"`
	AllowCPUFallback     bool     `json:"allow_cpu_fallback"`
	SkipTags             []string `json:"skip_tags"`
	WriteTag             string   `json:"write_tag"`
	KeepOriginalHolding  bool     `json:"keep_original_in_holding"`
	CopyIfLarger         bool     `json:"copy_if_larger"`
	Normalize1080p       bool     `json:"normalize_1080p"`
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
	SVTAV1Preset         int      `json:"svtav1_preset"`
	SVTAV1CRF            int      `json:"svtav1_crf"`
	MaxBitrateKbps       int      `json:"max_bitrate_kbps"`
	AllowedLanguages     []string `json:"allowed_languages"`
	PreferVFQ            bool     `json:"prefer_vfq"`
	AudioBitrateKbps     int      `json:"audio_bitrate_kbps"`
	AudioChannels        int      `json:"audio_channels"`
	KeepAnimeAttachments bool     `json:"keep_anime_attachments"`
	ApplyEDLCuts         bool     `json:"apply_edl_cuts"`
	RequireAudioStream   bool     `json:"require_audio_stream"`
	MinFileSizeMB        int      `json:"min_file_size_mb"`
	ScratchDir           string   `json:"scratch_dir"`
}

// DefaultSettings returns the default transcode configuration matching Alchemist.
func DefaultSettings() Settings {
	return Settings{
		AutoTranscode:        false,
		ConcurrentJobs:       2,
		PreferredEncoder:     "auto",
		HWPreset:             "medium",
		GPUDevices:           "auto",
		SelectedGPUs:         []string{},
		AllowCPUFallback:     true,
		SkipTags:             DefaultSkipTags(),
		WriteTag:             DefaultWriteTag,
		KeepOriginalHolding:  false,
		CopyIfLarger:         false,
		Normalize1080p:       true,
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
		SVTAV1Preset:         4,
		SVTAV1CRF:            24,
		MaxBitrateKbps:       10000,
		AllowedLanguages:     []string{"eng", "fra", "jpn", "und"},
		PreferVFQ:            true,
		AudioBitrateKbps:     192,
		AudioChannels:        2,
		KeepAnimeAttachments: true,
		ApplyEDLCuts:         true,
		RequireAudioStream:   true,
		MinFileSizeMB:        0,
		ScratchDir:           "",
	}
}

// ApplyProfileOverlay syncs overlapping Default profile fields onto Settings while preserving global-only options.
func (s *Settings) ApplyProfileOverlay(cfg ProfileConfig) {
	s.PreferredEncoder = cfg.PreferredEncoder
	s.HWPreset = cfg.HWPreset
	s.Normalize1080p = cfg.ScaleMode != "none"
	s.Pillarbox43 = cfg.Pillarbox43
	s.HDRMode = cfg.HDRMode
	s.VMAFEnabled = cfg.VMAFEnabled
	s.VMAFMinScore = cfg.VMAFMinScore
	s.VMAFSamples = cfg.VMAFSamples
	s.VMAFSampleSeconds = cfg.VMAFSampleSeconds
	s.VMAFFPS = cfg.VMAFFPS
	s.QMin = cfg.QMin
	s.QMax = cfg.QMax
	s.MaxIterations = cfg.MaxIterations
	s.SVTAV1Preset = cfg.CPUPreset
	s.SVTAV1CRF = cfg.CPUCRF
	s.MaxBitrateKbps = cfg.MaxBitrateKbps
	s.CopyIfLarger = cfg.CopyIfLarger
	s.SkipTags = append([]string{}, cfg.SkipTags...)
	s.WriteTag = cfg.WriteTag
	s.KeepOriginalHolding = cfg.KeepOriginalHolding
	s.AllowedLanguages = append([]string{}, cfg.AllowedLanguages...)
	s.PreferVFQ = cfg.PreferVFQ
	s.AudioBitrateKbps = cfg.AudioBitrateKbps
	s.AudioChannels = cfg.AudioChannels
	s.KeepAnimeAttachments = cfg.KeepAnimeAttachments
	s.ApplyEDLCuts = cfg.ApplyEDLCuts
	s.RequireAudioStream = cfg.RequireAudioStream
	s.MinFileSizeMB = cfg.MinFileSizeMB
}

// Validate checks and normalizes Settings values.
func (s *Settings) Validate() error {
	if s.ConcurrentJobs < 1 || s.ConcurrentJobs > 16 {
		return fmt.Errorf("concurrent_jobs must be between 1 and 16")
	}
	s.PreferredEncoder = strings.TrimSpace(strings.ToLower(s.PreferredEncoder))
	if s.PreferredEncoder == "" {
		s.PreferredEncoder = "auto"
	}
	if !isSupportedEncoder(s.PreferredEncoder) {
		return fmt.Errorf("preferred_encoder is not supported: %s", s.PreferredEncoder)
	}
	hp, err := normalizeHWPreset(s.HWPreset)
	if err != nil {
		return err
	}
	s.HWPreset = hp
	s.GPUDevices = strings.TrimSpace(s.GPUDevices)
	if s.GPUDevices == "" {
		s.GPUDevices = "auto"
	}
	s.SelectedGPUs = cleanStringSlice(s.SelectedGPUs)
	s.HDRMode = strings.TrimSpace(strings.ToLower(s.HDRMode))
	if s.HDRMode == "" {
		s.HDRMode = "preserve"
	}
	if s.HDRMode != "preserve" && s.HDRMode != "tonemap_sdr" {
		return fmt.Errorf("hdr_mode must be preserve or tonemap_sdr")
	}
	if s.VMAFMinScore < 50 || s.VMAFMinScore > 100 {
		return fmt.Errorf("vmaf_min_score must be between 50 and 100")
	}
	if s.VMAFSamples < 1 || s.VMAFSamples > 12 {
		return fmt.Errorf("vmaf_samples must be between 1 and 12")
	}
	if s.VMAFSampleSeconds < 3 || s.VMAFSampleSeconds > 120 {
		return fmt.Errorf("vmaf_sample_seconds must be between 3 and 120")
	}
	if s.VMAFFPS < 1 || s.VMAFFPS > 60 {
		return fmt.Errorf("vmaf_fps must be between 1 and 60")
	}
	if s.QMin < 1 || s.QMin > 63 || s.QMax < 1 || s.QMax > 63 || s.QMin > s.QMax {
		return fmt.Errorf("q_min and q_max must be between 1 and 63 with q_min <= q_max")
	}
	if s.MaxIterations < 1 || s.MaxIterations > 12 {
		return fmt.Errorf("max_iterations must be between 1 and 12")
	}
	if s.SVTAV1Preset < 0 || s.SVTAV1Preset > 13 {
		return fmt.Errorf("svtav1_preset must be between 0 and 13")
	}
	if s.SVTAV1CRF < 0 || s.SVTAV1CRF > 63 {
		return fmt.Errorf("svtav1_crf must be between 0 and 63")
	}
	if s.MaxBitrateKbps < 100 || s.MaxBitrateKbps > 100000 {
		return fmt.Errorf("max_bitrate_kbps must be between 100 and 100000")
	}
	if s.AudioBitrateKbps < 32 || s.AudioBitrateKbps > 1536 {
		return fmt.Errorf("audio_bitrate_kbps must be between 32 and 1536")
	}
	if s.AudioChannels < 1 || s.AudioChannels > 8 {
		return fmt.Errorf("audio_channels must be between 1 and 8")
	}
	if s.MinFileSizeMB < 0 {
		return fmt.Errorf("min_file_size_mb must be >= 0")
	}
	s.ScratchDir = strings.TrimSpace(s.ScratchDir)
	s.AllowedLanguages = cleanLanguages(s.AllowedLanguages)
	cleanedTags, err := cleanSkipTags(s.SkipTags)
	if err != nil {
		return err
	}
	s.SkipTags = cleanedTags
	writeTag, err := validateWriteTag(s.WriteTag)
	if err != nil {
		return err
	}
	s.WriteTag = writeTag
	return nil
}

func cleanSkipTags(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, raw := range in {
		if strings.ContainsAny(raw, "\r\n") {
			return nil, fmt.Errorf("skip_tags entries must be at most 200 characters without line breaks")
		}
		tag := strings.TrimSpace(raw)
		if tag == "" {
			continue
		}
		if len(tag) > 200 {
			return nil, fmt.Errorf("skip_tags entries must be at most 200 characters without line breaks")
		}
		key := strings.ToLower(tag)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, tag)
		if len(out) > 32 {
			return nil, fmt.Errorf("skip_tags supports at most 32 entries")
		}
	}
	return out, nil
}

func validateWriteTag(raw string) (string, error) {
	if strings.ContainsAny(raw, "\r\n") {
		return "", fmt.Errorf("write_tag must be at most 200 characters without line breaks")
	}
	tag := strings.TrimSpace(raw)
	if len(tag) > 200 {
		return "", fmt.Errorf("write_tag must be at most 200 characters without line breaks")
	}
	return tag, nil
}

func applyLegacyTagOverlay(obj map[string]json.RawMessage, skipTags *[]string, writeTag *string) {
	_, hasSkipTags := obj["skip_tags"]
	_, hasWriteTag := obj["write_tag"]
	ffRaw, hasLegacy := obj["fileflows_tag_parity"]
	if !hasLegacy {
		return
	}
	var parity bool
	if json.Unmarshal(ffRaw, &parity) != nil {
		return
	}
	if !hasSkipTags {
		if !parity {
			*skipTags = []string{}
		} else if len(*skipTags) == 0 {
			*skipTags = DefaultSkipTags()
		}
	}
	if !hasWriteTag {
		if !parity {
			*writeTag = ""
		} else if *writeTag == "" {
			*writeTag = DefaultWriteTag
		}
	}
}

func cleanStringSlice(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		v := strings.TrimSpace(raw)
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func cleanLanguages(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		if norm, _, ok := NormalizeLanguage(raw); ok && !seen[norm] {
			seen[norm] = true
			out = append(out, norm)
		}
	}
	return out
}

func isSupportedEncoder(enc string) bool {
	switch enc {
	case "auto",
		"av1_qsv", "av1_vaapi", "av1_nvenc", "av1_vulkan", "libsvtav1",
		"hevc_qsv", "hevc_vaapi", "hevc_nvenc", "hevc_vulkan", "libx265",
		"h264_qsv", "h264_vaapi", "h264_nvenc", "h264_vulkan", "libx264":
		return true
	}
	return false
}

// decodeSettingsOverlay decodes JSON onto an existing Settings, accepting
// gpu_devices as either a string or an array of strings.
func decodeSettingsOverlay(raw []byte, dst *Settings) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return err
	}
	if gRaw, ok := obj["gpu_devices"]; ok {
		var s string
		if json.Unmarshal(gRaw, &s) == nil {
			dst.GPUDevices = s
		} else {
			var arr []string
			if err := json.Unmarshal(gRaw, &arr); err != nil {
				return err
			}
			cleaned := cleanStringSlice(arr)
			if len(cleaned) == 0 {
				dst.GPUDevices = "auto"
			} else {
				dst.GPUDevices = strings.Join(cleaned, ",")
			}
		}
		delete(obj, "gpu_devices")
	}
	rest, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(rest, dst); err != nil {
		return err
	}
	applyLegacyTagOverlay(obj, &dst.SkipTags, &dst.WriteTag)
	return nil
}

// ReadSettings loads Settings from the database, falling back to defaults.
func ReadSettings(ctx context.Context, db database.Querier) Settings {
	st := DefaultSettings()
	raw := database.SettingOr(ctx, db, settingsGroup, settingsKey, "")
	if raw == "" {
		return st
	}
	_ = decodeSettingsOverlay([]byte(raw), &st)
	if st.AllowedLanguages == nil {
		st.AllowedLanguages = []string{}
	}
	if st.SelectedGPUs == nil {
		st.SelectedGPUs = []string{}
	}
	if st.SkipTags == nil {
		st.SkipTags = []string{}
	}
	if st.Validate() != nil {
		return DefaultSettings()
	}
	return st
}

// SaveSettings validates and persists Settings in the settings table.
func SaveSettings(ctx context.Context, store *database.Store, st Settings, by domain.Actor) error {
	if err := st.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return store.Write(ctx, func(tx *sql.Tx) error {
		if err := database.PutSetting(tx, settingsGroup, settingsKey, string(b), by, time.Now().UTC()); err != nil {
			return err
		}
		return syncDefaultProfileFromSettingsTx(ctx, tx, st, by)
	})
}
