package transcode

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

// OverviewCounts reports aggregate counts across library files and queue states.
type OverviewCounts struct {
	Total    int `json:"total"`
	Done     int `json:"done"`
	Skipped  int `json:"skipped"`
	Pending  int `json:"pending"`
	Queued   int `json:"queued"`
	Running  int `json:"running"`
	Failed   int `json:"failed"`
	Unprobed int `json:"unprobed"`
}

// OverviewCoverage summarizes library conversion progress as percentage 0..100.
type OverviewCoverage struct {
	Total     int     `json:"total"`
	Completed int     `json:"completed"`
	Percent   float64 `json:"percent"`
}

// OverviewSavings summarizes total input, output, and saved bytes for done encodes.
type OverviewSavings struct {
	InputBytes  int64   `json:"input_bytes"`
	OutputBytes int64   `json:"output_bytes"`
	SavedBytes  int64   `json:"saved_bytes"`
	Percent     float64 `json:"percent"`
	FilesDone   int     `json:"files_done"`
}

// OverviewSpeed summarizes average historical and live encoding speed.
type OverviewSpeed struct {
	FPS           float64 `json:"fps"`
	RealtimeX     float64 `json:"realtime_x"`
	LiveFPS       float64 `json:"live_fps"`
	LiveRealtimeX float64 `json:"live_realtime_x"`
	Samples       int     `json:"samples"`
}

// OverviewJob represents an active or recent transcode queue job.
type OverviewJob struct {
	ID           int64      `json:"id"`
	Kind         string     `json:"kind"`
	State        string     `json:"state"`
	FileID       int64      `json:"file_id,omitempty"`
	TitleID      int64      `json:"title_id,omitempty"`
	Title        string     `json:"title,omitempty"`
	MediaKind    string     `json:"media_kind,omitempty"`
	Season       *int       `json:"season,omitempty"`
	Episode      *int       `json:"episode,omitempty"`
	RelativePath string     `json:"relative_path,omitempty"`
	Progress     float64    `json:"progress"`
	Phase        string     `json:"phase"`
	Note         string     `json:"note"`
	FPS          float64    `json:"fps"`
	Speed        float64    `json:"speed"`
	SelectedQ    *int       `json:"selected_q"`
	MeanVMAF     *float64   `json:"mean_vmaf"`
	Encoder      string     `json:"encoder"`
	GPUDevice    string     `json:"gpu_device"`
	InputBytes   int64      `json:"input_bytes"`
	ProfileName  string     `json:"profile_name,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// Overview is returned by GET /api/v1/transcode/overview.
type Overview struct {
	Paused           bool             `json:"paused"`
	AutoTranscode    bool             `json:"auto_transcode"`
	Ready            bool             `json:"ready"`
	FFmpegFound      bool             `json:"ffmpeg_found"`
	FFprobeFound     bool             `json:"ffprobe_found"`
	VMAFAvailable    bool             `json:"vmaf_available"`
	GPUs             []GPUDevice      `json:"gpus"`
	Encoders         []string         `json:"encoders"`
	PreferredEncoder string           `json:"preferred_encoder"`
	Counts           OverviewCounts   `json:"counts"`
	Coverage         OverviewCoverage `json:"coverage"`
	Savings          OverviewSavings  `json:"savings"`
	Speed            OverviewSpeed    `json:"speed"`
	ETASeconds       int64            `json:"eta_seconds,omitempty"`
	Running          map[string]int   `json:"running"`
	Queued           map[string]int   `json:"queued"`
	FailedCount      int              `json:"failed_count"`
	Jobs             []OverviewJob    `json:"jobs"`
}

// ItemRow describes one library video file and its transcode status.
type ItemRow struct {
	FileID          int64      `json:"file_id"`
	TitleID         int64      `json:"title_id"`
	Title           string     `json:"title"`
	MediaKind       string     `json:"media_kind"`
	Season          *int       `json:"season,omitempty"`
	Episode         *int       `json:"episode,omitempty"`
	EpisodeName     string     `json:"episode_name,omitempty"`
	RelativePath    string     `json:"relative_path"`
	State           string     `json:"state"`
	SkipReason      string     `json:"skip_reason,omitempty"`
	FileFlowsTagged bool       `json:"fileflows_tagged"`
	SourceCodec     string     `json:"source_codec"`
	SourceWidth     int        `json:"source_width"`
	SourceHeight    int        `json:"source_height"`
	SourceHDR       string     `json:"source_hdr"`
	TargetCodec     string     `json:"target_codec"`
	SelectedQ       *int       `json:"selected_q"`
	MeanVMAF        *float64   `json:"mean_vmaf"`
	GPUDevice       string     `json:"gpu_device"`
	InputBytes      int64      `json:"input_bytes"`
	OutputBytes     int64      `json:"output_bytes"`
	SavedBytes      int64      `json:"saved_bytes"`
	EncodeSeconds   float64    `json:"encode_seconds"`
	AvgFPS          float64    `json:"avg_fps"`
	AvgSpeed        float64    `json:"avg_speed"`
	EDLApplied      bool       `json:"edl_applied"`
	Tonemapped      bool       `json:"tonemapped"`
	ProfileName     string     `json:"profile_name,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	ProbedAt        *time.Time `json:"probed_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
}

// FailedJob describes one failed or cancelled transcode job.
type FailedJob struct {
	ID           int64      `json:"id"`
	Kind         string     `json:"kind"`
	State        string     `json:"state"`
	FileID       int64      `json:"file_id,omitempty"`
	TitleID      int64      `json:"title_id,omitempty"`
	Title        string     `json:"title,omitempty"`
	MediaKind    string     `json:"media_kind,omitempty"`
	Season       *int       `json:"season,omitempty"`
	Episode      *int       `json:"episode,omitempty"`
	RelativePath string     `json:"relative_path,omitempty"`
	Progress     float64    `json:"progress"`
	Note         string     `json:"note"`
	Error        string     `json:"error"`
	Attempts     int        `json:"attempts"`
	MaxAttempts  int        `json:"max_attempts"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

func roundPct(num, den int64) float64 {
	if den <= 0 {
		return 0
	}
	v := (float64(num) / float64(den)) * 100.0
	return math.Round(v*10.0) / 10.0
}

func (s *Service) folderProfileNameMap(ctx context.Context) (string, map[int64]string) {
	profiles, assigns, err := s.ListProfiles(ctx)
	if err != nil {
		return "Default", map[int64]string{}
	}
	defName := "Default"
	for _, p := range profiles {
		if p.IsDefault {
			defName = p.Name
			break
		}
	}
	out := make(map[int64]string, len(assigns))
	for _, a := range assigns {
		out[a.LibraryFolderID] = a.ProfileName
	}
	return defName, out
}

// Overview builds the summary payload for the Transcode dashboard.
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	if err := s.EnsureDefaultProfile(ctx); err != nil {
		return Overview{}, err
	}
	db := s.Store.Read()
	st := ReadSettings(ctx, db)
	hw := s.probeHardwareWithSettings(ctx, st)

	fileIDs, err := mediafiles.ListPresentMainVideoIDs(ctx, db)
	if err != nil {
		return Overview{}, err
	}
	agg, err := summarizeItemsOverview(ctx, db, fileIDs)
	if err != nil {
		return Overview{}, err
	}

	unprobed := max(0, len(fileIDs)-agg.MatchedRows)
	counts := OverviewCounts{
		Total:    len(fileIDs),
		Done:     agg.Done,
		Skipped:  agg.Skipped,
		Pending:  agg.Pending + unprobed,
		Failed:   agg.Failed,
		Unprobed: unprobed,
	}
	savings := OverviewSavings{
		FilesDone:   agg.Done,
		InputBytes:  agg.InputBytes,
		OutputBytes: agg.OutputBytes,
		SavedBytes:  agg.SavedBytes,
		Percent:     roundPct(agg.SavedBytes, agg.InputBytes),
	}

	completed := counts.Done + counts.Skipped
	coverage := OverviewCoverage{
		Total:     counts.Total,
		Completed: completed,
		Percent:   roundPct(int64(completed), int64(counts.Total)),
	}

	runningMap, _ := jobs.CountByState(ctx, db, overviewKinds, "running")
	queuedMap, _ := jobs.CountByState(ctx, db, overviewKinds, "queued")
	failedMap, _ := jobs.CountByState(ctx, db, overviewKinds, "failed")
	if runningMap == nil {
		runningMap = map[string]int{}
	}
	if queuedMap == nil {
		queuedMap = map[string]int{}
	}
	for _, v := range runningMap {
		counts.Running += v
	}
	for _, v := range queuedMap {
		counts.Queued += v
	}

	activeJobs, _ := jobs.ListActive(ctx, db, overviewKinds, 30)
	defProfName, folderProfiles := s.folderProfileNameMap(ctx)
	activeFileIDs := make([]int64, 0, len(activeJobs))
	for _, j := range activeJobs {
		if fid, ok := fileIDParam(j); ok {
			activeFileIDs = append(activeFileIDs, fid)
		}
	}
	summaries, _ := mediafiles.GetTranscodeFileSummariesByIDs(ctx, db, activeFileIDs)
	itemsMap, _ := listItemsByFileIDs(ctx, db, activeFileIDs)
	jobViews := make([]OverviewJob, 0, len(activeJobs))

	var liveFPSSum, liveSpeedSum float64
	var liveCount int
	for _, j := range activeJobs {
		oj := s.toOverviewJob(j, defProfName, folderProfiles, summaries, itemsMap)
		if j.State == "running" && (oj.FPS > 0 || oj.Speed > 0) {
			liveFPSSum += oj.FPS
			liveSpeedSum += oj.Speed
			liveCount++
		}
		jobViews = append(jobViews, oj)
	}

	speed := OverviewSpeed{Samples: agg.SpeedSamples}
	if agg.SpeedSamples > 0 {
		speed.FPS = math.Round((agg.FPSSum/float64(agg.SpeedSamples))*10.0) / 10.0
		speed.RealtimeX = math.Round((agg.SpeedSum/float64(agg.SpeedSamples))*100.0) / 100.0
	}
	if liveCount > 0 {
		speed.LiveFPS = math.Round(liveFPSSum*10.0) / 10.0
		speed.LiveRealtimeX = math.Round(liveSpeedSum*100.0) / 100.0
	}

	var etaSeconds int64
	if counts.Pending > 0 && len(fileIDs) > 0 {
		runtimes, _ := mediafiles.ListPresentMainVideoRuntimes(ctx, db)
		finished, _ := listFinishedFileIDs(ctx, db)
		recent, _ := recentDoneEncodeStats(ctx, db, 20)
		etaSeconds = computeBacklogETA(counts.Pending, fileIDs, runtimes, finished, recent, activeJobs, runningMap[KindFile], liveSpeedSum, liveCount, st.ConcurrentJobs)
	}

	return Overview{
		Paused:           s.Paused(),
		AutoTranscode:    st.AutoTranscode,
		Ready:            hw.Ready,
		FFmpegFound:      hw.FFmpegFound,
		FFprobeFound:     hw.FFprobeFound,
		VMAFAvailable:    hw.VMAFAvailable,
		GPUs:             hw.GPUs,
		Encoders:         hw.Encoders,
		PreferredEncoder: hw.PreferredEncoder,
		Counts:           counts,
		Coverage:         coverage,
		Savings:          savings,
		Speed:            speed,
		ETASeconds:       etaSeconds,
		Running:          runningMap,
		Queued:           queuedMap,
		FailedCount:      failedMap[KindFile] + failedMap[KindScan],
		Jobs:             jobViews,
	}, nil
}

func computeBacklogETA(pending int, fileIDs []int64, runtimes map[int64]float64, finished map[int64]bool, recent []doneEncodeSample, activeJobs []jobs.Job, runningFileJobs int, liveSpeedSum float64, liveCount, concurrentJobs int) int64 {
	if pending <= 0 || len(fileIDs) == 0 {
		return 0
	}
	rtSum, rtCount := 0.0, 0
	for _, fid := range fileIDs {
		if rt := runtimes[fid]; rt > 0 {
			rtSum += rt
			rtCount++
		}
	}
	avgKnownRuntime := 2700.0
	if rtCount > 0 {
		avgKnownRuntime = rtSum / float64(rtCount)
	}
	runningProg := map[int64]float64{}
	for _, j := range activeJobs {
		if fid, ok := fileIDParam(j); ok && j.State == "running" && j.Kind == KindFile {
			runningProg[fid] = min(1.0, max(0.0, j.Progress))
		}
	}
	remMediaSec, remFiles := 0.0, 0
	for _, fid := range fileIDs {
		if finished[fid] {
			continue
		}
		dur := runtimes[fid]
		if dur <= 0 {
			dur = avgKnownRuntime
		}
		if prog, ok := runningProg[fid]; ok {
			dur *= (1.0 - prog)
		}
		remMediaSec += dur
		remFiles++
	}
	if remFiles == 0 || remMediaSec <= 0 {
		return 0
	}
	recentEncodeSec, recentMediaSec, recentSpeedSum, recentSpeedCount := 0.0, 0.0, 0.0, 0
	for _, r := range recent {
		if r.EncodeSeconds <= 0 {
			continue
		}
		recentEncodeSec += r.EncodeSeconds
		dur := runtimes[r.FileID]
		if dur <= 0 && r.AvgSpeed > 0 {
			dur = r.EncodeSeconds * r.AvgSpeed
		}
		if dur <= 0 {
			dur = avgKnownRuntime
		}
		recentMediaSec += dur
		if r.AvgSpeed > 0 {
			recentSpeedSum += r.AvgSpeed
			recentSpeedCount++
		}
	}
	histPerWorkerX, overhead := 0.0, 1.0
	if recentEncodeSec > 0 {
		histPerWorkerX = recentMediaSec / recentEncodeSec
	}
	if recentSpeedCount > 0 && histPerWorkerX > 0 {
		overhead = min(2.5, max(1.0, (recentSpeedSum/float64(recentSpeedCount))/histPerWorkerX))
	}
	var aggX float64
	switch {
	case liveCount > 0 && liveSpeedSum > 0:
		scaledLive := liveSpeedSum
		if runningFileJobs > liveCount {
			scaledLive = liveSpeedSum * float64(runningFileJobs) / float64(liveCount)
		}
		aggX = scaledLive / overhead
	case histPerWorkerX > 0:
		aggX = histPerWorkerX * float64(min(max(runningFileJobs, concurrentJobs, 1), remFiles))
	default:
		return 0
	}
	if aggX <= 0 {
		return 0
	}
	return max(int64(1), int64(math.Round(remMediaSec/aggX)))
}

func (s *Service) toOverviewJob(j jobs.Job, defProfName string, folderProfiles map[int64]string, summaries map[int64]mediafiles.TranscodeFileSummary, itemsMap map[int64]ItemRecord) OverviewJob {
	note := ""
	if j.ProgressNote != nil {
		note = *j.ProgressNote
	}
	oj := OverviewJob{
		ID:        j.ID,
		Kind:      j.Kind,
		State:     j.State,
		Progress:  j.Progress,
		Phase:     note,
		Note:      note,
		StartedAt: j.StartedAt,
		CreatedAt: j.CreatedAt,
	}
	if fileID, ok := fileIDParam(j); ok {
		oj.FileID = fileID
		if meta, exists := summaries[fileID]; exists {
			oj.TitleID = meta.TitleID
			oj.Title = meta.Title
			oj.MediaKind = meta.MediaKind
			oj.Season = meta.Season
			oj.Episode = meta.Episode
			oj.RelativePath = meta.RelativePath
			oj.InputBytes = meta.Size
			if pName := folderProfiles[meta.LibraryFolderID]; pName != "" {
				oj.ProfileName = pName
			} else {
				oj.ProfileName = defProfName
			}
		}
		if rec, found := itemsMap[fileID]; found {
			oj.SelectedQ = rec.SelectedQ
			oj.MeanVMAF = rec.MeanVMAF
			oj.GPUDevice = rec.GPUDevice
			if rec.InputBytes > 0 {
				oj.InputBytes = rec.InputBytes
			}
		}
	}
	if ls, ok := s.getLiveStat(j.ID); ok {
		if ls.phase != "" {
			oj.Phase = ls.phase
		}
		oj.FPS = math.Round(ls.fps*10.0) / 10.0
		oj.Speed = math.Round(ls.speed*100.0) / 100.0
		if ls.encoder != "" {
			oj.Encoder = ls.encoder
		}
		if ls.gpuDevice != "" {
			oj.GPUDevice = ls.gpuDevice
		}
		if ls.selectedQ != nil {
			oj.SelectedQ = ls.selectedQ
		}
		if ls.meanVMAF != nil {
			oj.MeanVMAF = ls.meanVMAF
		}
		if ls.profileName != "" {
			oj.ProfileName = ls.profileName
		}
		if ls.inputBytes > 0 {
			oj.InputBytes = ls.inputBytes
		}
	}
	return oj
}

func clampPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = 50
	} else if limit > 200 {
		limit = 200
	}
	return limit, max(0, offset)
}

// ListLibraryItems returns filtered, paginated library files with transcode status.
func (s *Service) ListLibraryItems(ctx context.Context, stateFilter, query, codecFilter string, limit, offset int) ([]ItemRow, int, error) {
	if err := s.EnsureDefaultProfile(ctx); err != nil {
		return nil, 0, err
	}
	limit, offset = clampPage(limit, offset)

	profiles, assigns, err := s.ListProfiles(ctx)
	if err != nil {
		return nil, 0, err
	}
	profByID := make(map[int64]Profile, len(profiles))
	defProf := DefaultProfileConfig()
	defName := "Default"
	for _, p := range profiles {
		profByID[p.ID] = p
		if p.IsDefault {
			defProf = p.ProfileConfig
			defName = p.Name
		}
	}
	assignByFolder := make(map[int64]LibraryAssignment, len(assigns))
	for _, a := range assigns {
		assignByFolder[a.LibraryFolderID] = a
	}

	stateFilter = strings.ToLower(strings.TrimSpace(stateFilter))
	qLower := strings.ToLower(strings.TrimSpace(query))
	codecLower := strings.ToLower(strings.TrimSpace(codecFilter))
	db := s.Store.Read()

	buildRow := func(f mediafiles.TranscodeFileSummary, rec *ItemRecord) ItemRow {
		folderProfName := defName
		folderTarget := defProf.TargetCodec
		if a, ok := assignByFolder[f.LibraryFolderID]; ok {
			if a.ProfileName != "" {
				folderProfName = a.ProfileName
			}
			if a.ProfileID != nil {
				if p, exists := profByID[*a.ProfileID]; exists {
					folderTarget = p.TargetCodec
				}
			}
		}
		row := ItemRow{
			FileID:       f.FileID,
			TitleID:      f.TitleID,
			Title:        f.Title,
			MediaKind:    f.MediaKind,
			Season:       f.Season,
			Episode:      f.Episode,
			EpisodeName:  f.EpisodeName,
			RelativePath: f.RelativePath,
			State:        "pending",
			SourceCodec:  f.SourceCodec,
			TargetCodec:  folderTarget,
			InputBytes:   f.Size,
			ProfileName:  folderProfName,
		}
		if rec != nil {
			row.State = rec.State
			if rec.SkipReason != nil {
				row.SkipReason = *rec.SkipReason
			}
			row.FileFlowsTagged = rec.FileFlowsTagged
			if rec.SourceCodec != "" {
				row.SourceCodec = rec.SourceCodec
			}
			row.SourceWidth = rec.SourceWidth
			row.SourceHeight = rec.SourceHeight
			row.SourceHDR = rec.SourceHDR
			if rec.TargetCodec != "" {
				row.TargetCodec = rec.TargetCodec
			}
			row.SelectedQ = rec.SelectedQ
			row.MeanVMAF = rec.MeanVMAF
			row.GPUDevice = rec.GPUDevice
			if rec.InputBytes > 0 {
				row.InputBytes = rec.InputBytes
			}
			row.OutputBytes = rec.OutputBytes
			row.SavedBytes = rec.SavedBytes
			row.EncodeSeconds = rec.EncodeSeconds
			row.AvgFPS = rec.AvgFPS
			row.AvgSpeed = rec.AvgSpeed
			row.EDLApplied = rec.EDLApplied
			row.Tonemapped = rec.Tonemapped
			if rec.LastError != nil {
				row.LastError = *rec.LastError
			}
			row.ProbedAt = rec.ProbedAt
			row.CompletedAt = rec.CompletedAt
			if rec.ProfileID != nil {
				if p, exists := profByID[*rec.ProfileID]; exists {
					row.ProfileName = p.Name
				}
			}
		}
		return row
	}

	matchesFilter := func(row ItemRow) bool {
		if codecLower != "" {
			if !strings.EqualFold(row.SourceCodec, codecLower) && !strings.EqualFold(row.TargetCodec, codecLower) {
				return false
			}
		}
		if qLower != "" {
			if !strings.Contains(strings.ToLower(row.Title), qLower) &&
				!strings.Contains(strings.ToLower(row.RelativePath), qLower) &&
				!strings.Contains(strings.ToLower(row.EpisodeName), qLower) &&
				!strings.Contains(strings.ToLower(row.ProfileName), qLower) {
				return false
			}
		}
		return true
	}

	// Fast index-backed path for history/done/skipped/failed queries.
	switch stateFilter {
	case "done", "skipped", "history", "failed":
		unfiltered := qLower == "" && codecLower == ""
		var recs []ItemRecord
		var total int
		var err error
		if unfiltered {
			recs, total, err = listItemsByFilterPage(ctx, db, stateFilter, limit, offset)
		} else {
			recs, err = listItemsByFilterAll(ctx, db, stateFilter)
		}
		if err != nil {
			return nil, 0, err
		}
		fileIDs := make([]int64, 0, len(recs))
		for _, r := range recs {
			fileIDs = append(fileIDs, r.FileID)
		}
		summaries, err := mediafiles.GetTranscodeFileSummariesByIDs(ctx, db, fileIDs)
		if err != nil {
			return nil, 0, err
		}
		matched := make([]ItemRow, 0, len(recs))
		for i := range recs {
			sum, ok := summaries[recs[i].FileID]
			if !ok {
				continue
			}
			row := buildRow(sum, &recs[i])
			if !unfiltered && !matchesFilter(row) {
				continue
			}
			matched = append(matched, row)
		}
		if unfiltered {
			return matched, total, nil
		}
		total = len(matched)
		if offset >= total {
			return []ItemRow{}, total, nil
		}
		return matched[offset:min(offset+limit, total)], total, nil
	}

	files, err := mediafiles.ListPresentMainVideoScanRows(ctx, db)
	if err != nil {
		return nil, 0, err
	}
	itemsMap, err := listItemsByFileID(ctx, db)
	if err != nil {
		return nil, 0, err
	}

	matched := make([]ItemRow, 0, len(files))
	for _, f := range files {
		var recPtr *ItemRecord
		if rec, ok := itemsMap[f.FileID]; ok {
			recCopy := rec
			recPtr = &recCopy
		}
		row := buildRow(f, recPtr)
		if stateFilter != "" && stateFilter != "all" && row.State != stateFilter {
			continue
		}
		if !matchesFilter(row) {
			continue
		}
		matched = append(matched, row)
	}

	total := len(matched)
	if offset >= total {
		return []ItemRow{}, total, nil
	}
	end := min(offset+limit, total)
	return matched[offset:end], total, nil
}

// ListFailedJobs returns paginated failed or cancelled transcode jobs and counts.
func (s *Service) ListFailedJobs(ctx context.Context, stateFilter string, limit, offset int) ([]FailedJob, int, int, int, error) {
	limit, offset = clampPage(limit, offset)
	var states []string
	switch strings.ToLower(strings.TrimSpace(stateFilter)) {
	case "", "all":
		states = []string{"failed", "cancelled"}
	case "failed", "cancelled":
		states = []string{strings.ToLower(strings.TrimSpace(stateFilter))}
	default:
		return nil, 0, 0, 0, fmt.Errorf("state must be all, failed or cancelled")
	}

	db := s.Store.Read()
	list, total, err := jobs.ListByStates(ctx, db, overviewKinds, states, limit, offset)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	counts, err := jobs.CountByState(ctx, db, overviewKinds, "failed", "cancelled")
	if err != nil {
		return nil, 0, 0, 0, err
	}
	failedFileIDs := make([]int64, 0, len(list))
	for _, j := range list {
		if fid, ok := fileIDParam(j); ok {
			failedFileIDs = append(failedFileIDs, fid)
		}
	}
	summaries, _ := mediafiles.GetTranscodeFileSummariesByIDs(ctx, db, failedFileIDs)
	out := make([]FailedJob, 0, len(list))
	for _, j := range list {
		note := ""
		if j.ProgressNote != nil {
			note = *j.ProgressNote
		}
		errMsg := ""
		if j.Error != nil {
			errMsg = j.Error.Message
		}
		fj := FailedJob{
			ID:          j.ID,
			Kind:        j.Kind,
			State:       j.State,
			Progress:    j.Progress,
			Note:        note,
			Error:       errMsg,
			Attempts:    j.Attempt,
			MaxAttempts: j.MaxAttempts,
			CreatedAt:   j.CreatedAt,
			UpdatedAt:   j.UpdatedAt,
			FinishedAt:  j.EndedAt,
		}
		if fid, ok := fileIDParam(j); ok {
			fj.FileID = fid
			if meta, exists := summaries[fid]; exists {
				fj.TitleID = meta.TitleID
				fj.Title = meta.Title
				fj.MediaKind = meta.MediaKind
				fj.Season = meta.Season
				fj.Episode = meta.Episode
				fj.RelativePath = meta.RelativePath
			}
		}
		out = append(out, fj)
	}
	return out, total, counts["failed"], counts["cancelled"], nil
}

func isTranscodeJobKind(kind string) bool {
	return kind == KindFile || kind == KindScan
}

// StopJob cancels a queued or running transcode job.
func (s *Service) StopJob(ctx context.Context, id int64) error {
	if s.Jobs == nil {
		return errors.New("no job queue")
	}
	var j jobs.Job
	if err := database.Get(ctx, s.Store.Read(), "jobs", id, &j); err != nil {
		return err
	}
	if !isTranscodeJobKind(j.Kind) {
		return database.ErrNotFound
	}
	return s.Jobs.Cancel(ctx, id)
}

// RetryJob retries a failed or cancelled transcode job.
func (s *Service) RetryJob(ctx context.Context, id int64) (jobs.Job, error) {
	if s.Jobs == nil {
		return jobs.Job{}, errors.New("no job queue")
	}
	var j jobs.Job
	if err := database.Get(ctx, s.Store.Read(), "jobs", id, &j); err != nil {
		return jobs.Job{}, err
	}
	if !isTranscodeJobKind(j.Kind) {
		return jobs.Job{}, database.ErrNotFound
	}
	if fid, ok := fileIDParam(j); ok {
		clearItemError(ctx, s.Store, fid)
	}
	return s.Jobs.Retry(ctx, id)
}

// RetryFailedJobs retries up to 500 failed transcode jobs.
func (s *Service) RetryFailedJobs(ctx context.Context) (int, int, error) {
	if s.Jobs == nil {
		return 0, 0, errors.New("no job queue")
	}
	list, _, err := jobs.ListByStates(ctx, s.Store.Read(), overviewKinds, []string{"failed"}, 500, 0)
	if err != nil {
		return 0, 0, err
	}
	retried, skipped := 0, 0
	for _, j := range list {
		if fid, ok := fileIDParam(j); ok {
			clearItemError(ctx, s.Store, fid)
		}
		if _, err := s.Jobs.Retry(ctx, j.ID); err != nil {
			skipped++
			continue
		}
		retried++
	}
	return retried, skipped, nil
}
