package subtitles

import (
	"context"
	"database/sql"
	"math"
	"net/http"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/webserver"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/builtin"
)

// overviewKinds are the job kinds the Subtitles page follows.
var overviewKinds = []string{KindTranscribe, KindTranslate, KindSearch, KindMissing, KindBetter, KindLook, KindModelDownload, KindVerify}

// overviewJobs is how many jobs the page lists.
const (
	overviewDone = 25
	overviewJobs = 30
)

// OverviewJob is one subtitle job that runs, waits, failed, or was cancelled.
type OverviewJob struct {
	ID           int64      `json:"id"`
	Kind         string     `json:"kind"`
	State        string     `json:"state"`
	Progress     float64    `json:"progress"`
	ProgressNote string     `json:"progress_note,omitempty"`
	Phase        string     `json:"phase,omitempty"`
	Speed        float64    `json:"speed,omitempty"`
	SpeedUnit    string     `json:"speed_unit,omitempty"`
	Attempt      int        `json:"attempt,omitempty"`
	MaxAttempts  int        `json:"max_attempts,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
	Error        string     `json:"error,omitempty"`
	FileID       int64      `json:"file_id,omitempty"`
	TitleID      int64      `json:"title_id,omitempty"`
	Name         string     `json:"name,omitempty"` // the movie or show
	MediaKind    string     `json:"media_kind,omitempty"`
	Season       *int       `json:"season,omitempty"`
	Episode      *int       `json:"episode,omitempty"`
	RelativePath string     `json:"relative_path,omitempty"`
	Language     string     `json:"language,omitempty"` // of a translation
	Forced       bool       `json:"forced,omitempty"`
	Model        string     `json:"model,omitempty"` // of a model download
}

// SpeedStats summarizes live and historical processing throughput for one pipeline.
type SpeedStats struct {
	LiveSpeed         float64 `json:"live_speed,omitempty"`
	AvgSpeed          float64 `json:"avg_speed,omitempty"`
	Unit              string  `json:"unit"`
	Samples           int     `json:"samples"`
	TotalLines        int     `json:"total_lines,omitempty"`
	TotalAudioSeconds float64 `json:"total_audio_seconds,omitempty"`
}

// SubtitleStats holds speed statistics for speech transcription and translation.
type SubtitleStats struct {
	Speech    SpeedStats `json:"speech"`
	Translate SpeedStats `json:"translate"`
}

// Overview is what the Subtitles page shows above its list: what is
// missing and why, whether the machines that can make a subtitle are ready,
// and the subtitle jobs that run or wait.
type Overview struct {
	MissingCounts
	Library           LibraryProgress  `json:"library"`
	Stats             SubtitleStats    `json:"stats"`
	Integrity         IntegritySummary `json:"integrity"`
	Transcriber       Transcriber      `json:"transcriber"`
	TranscribeMissing bool             `json:"transcribe_missing"`
	TranslatorReady   bool             `json:"translator_ready"`
	TranslatorName    string           `json:"translator_name"`
	Paused            bool             `json:"paused"` // subtitle work is held in the queue
	Done              []DoneRow        `json:"done"`   // the newest subtitles HolyArrs saved
	DoneCounts        DoneCounts       `json:"done_counts"`
	SkippedCount      int              `json:"skipped_count"`
	Jobs              []OverviewJob    `json:"jobs"`
	Running           map[string]int   `json:"running"` // by kind
	Queued            map[string]int   `json:"queued"`  // by kind
	FailedCount       int              `json:"failed_count"`
	CancelledCount    int              `json:"cancelled_count"`
	ETASeconds        int64            `json:"eta_seconds,omitempty"`
}

func isSubtitleJobKind(kind string) bool {
	for _, k := range overviewKinds {
		if k == kind {
			return true
		}
	}
	return false
}

func jobFileIDs(list []jobs.Job) []int64 {
	out := make([]int64, 0, len(list))
	for _, j := range list {
		if j.Subject != nil && j.Subject.Type == "file" && j.Subject.ID > 0 {
			out = append(out, j.Subject.ID)
		}
	}
	return out
}

func (s *Service) toOverviewJob(j jobs.Job, summaries map[int64]mediafiles.FileSummary) OverviewJob {
	v := OverviewJob{
		ID:          j.ID,
		Kind:        j.Kind,
		State:       j.State,
		Progress:    j.Progress,
		Attempt:     j.Attempt,
		MaxAttempts: j.MaxAttempts,
		StartedAt:   j.StartedAt,
		EndedAt:     j.EndedAt,
	}
	if j.ProgressNote != nil {
		v.ProgressNote = *j.ProgressNote
		v.Phase = *j.ProgressNote
	}
	if j.Error != nil {
		v.Error = j.Error.Message
	}
	if j.State == "running" {
		if st, ok := s.getLiveStat(j.ID); ok {
			v.Speed, v.SpeedUnit = math.Round(st.speed*100)/100, st.unit
		}
	}
	v.Language, _ = j.Params["language"].(string)
	v.Forced, _ = j.Params["forced"].(bool)
	if id, _ := j.Params["id"].(string); j.Kind == KindModelDownload && id != "" {
		v.Model = id
		if m, ok := modelName(id); ok {
			v.Model = m
		}
	}
	if j.Subject != nil && j.Subject.Type == "file" {
		v.FileID = j.Subject.ID
		if meta, ok := summaries[v.FileID]; ok {
			v.TitleID = meta.TitleID
			v.Name = meta.Title
			v.MediaKind = meta.MediaKind
			v.Season = meta.Season
			v.Episode = meta.Episode
			v.RelativePath = meta.RelativePath
		}
	}
	return v
}

func (s *Service) computeStats(ctx context.Context, db *sql.DB) SubtitleStats {
	return SubtitleStats{
		Speech: s.computeSpeedStats(ctx, db, KindTranscribe, "x", func(res map[string]any) (num, den, audio float64, lines int, ok bool) {
			audio, _ = numFloat(res["audio_seconds"])
			if l, has := numFloat(res["lines"]); has && l > 0 {
				lines = int(l)
			}
			elapsed, _ := numFloat(res["elapsed_seconds"])
			if audio > 0 && elapsed > 0 {
				return audio, elapsed, audio, lines, true
			}
			if sp, has := numFloat(res["speed_x"]); has && sp > 0 {
				return sp, 1, audio, lines, true
			}
			return 0, 0, 0, 0, false
		}),
		Translate: s.computeSpeedStats(ctx, db, KindTranslate, "lines/s", func(res map[string]any) (num, den, audio float64, lines int, ok bool) {
			l, _ := numFloat(res["translated"])
			if l <= 0 {
				l, _ = numFloat(res["lines"])
			}
			if l > 0 {
				lines = int(l)
			}
			elapsed, _ := numFloat(res["elapsed_seconds"])
			if l > 0 && elapsed > 0 {
				return l, elapsed, 0, lines, true
			}
			if lps, has := numFloat(res["lines_per_sec"]); has && lps > 0 {
				return lps, 1, 0, lines, true
			}
			return 0, 0, 0, 0, false
		}),
	}
}

func (s *Service) computeSpeedStats(ctx context.Context, db *sql.DB, kind, unit string, extract func(map[string]any) (num, den, audio float64, lines int, ok bool)) SpeedStats {
	out := SpeedStats{Unit: unit}
	done, err := jobs.RecentDone(ctx, db, []string{kind}, 20)
	var totalNum, totalDen, totalAudio float64
	var histSamples, totalLines int
	if err == nil {
		for _, j := range done {
			if j.Result == nil {
				continue
			}
			if n, d, audio, lines, ok := extract(j.Result); ok {
				totalNum += n
				totalDen += d
				if audio > 0 {
					totalAudio += audio
				}
				if lines > 0 {
					totalLines += lines
				}
				histSamples++
			}
		}
	}
	out.Samples = histSamples
	if histSamples > 0 && totalDen > 0 {
		out.AvgSpeed = math.Round((totalNum/totalDen)*100) / 100
	}
	out.TotalLines = totalLines
	if totalAudio > 0 {
		out.TotalAudioSeconds = math.Round(totalAudio*10) / 10
	}
	if live := s.liveStatsForKind(kind); len(live) > 0 {
		var liveSum float64
		for _, sp := range live {
			liveSum += sp
		}
		out.LiveSpeed = math.Round(liveSum*100) / 100
	}
	return out
}

func numFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	}
	return 0, false
}

// Overview reads the page's numbers and jobs.
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	db := s.Store.Read()
	o := Overview{Jobs: []OverviewJob{}, Running: map[string]int{}, Queued: map[string]int{}, Paused: s.Paused()}
	var err error
	if o.MissingCounts, err = missingCounts(ctx, db); err != nil {
		return o, err
	}
	if o.Library, err = libraryProgress(ctx, db, o.MissingCounts); err != nil {
		return o, err
	}
	if o.Integrity, err = s.IntegritySummary(ctx); err != nil {
		return o, err
	}
	if o.Done, o.DoneCounts, err = Done(ctx, db, overviewDone); err != nil {
		return o, err
	}
	if o.SkippedCount, err = skippedCount(ctx, db); err != nil {
		return o, err
	}
	set := ReadSettings(ctx, db)
	o.Stats = s.computeStats(ctx, db)
	o.Transcriber = s.Transcriber(ctx)
	o.TranscribeMissing = set.TranscribeMissing
	tr := s.Translator(ctx)
	o.TranslatorReady, o.TranslatorName = tr.Ready(), tr.Name()

	runningMap, err := jobs.CountByKind(ctx, db, overviewKinds, "running")
	if err != nil {
		return o, err
	}
	queuedMap, err := jobs.CountByKind(ctx, db, overviewKinds, "queued")
	if err != nil {
		return o, err
	}
	counts, err := jobs.CountByState(ctx, db, overviewKinds, "failed", "cancelled")
	if err != nil {
		return o, err
	}
	o.Running = runningMap
	o.Queued = queuedMap
	o.FailedCount = counts["failed"]
	o.CancelledCount = counts["cancelled"]

	activeJobs, err := jobs.ListActive(ctx, db, overviewKinds, overviewJobs)
	if err != nil {
		return o, err
	}
	summaries, err := mediafiles.GetFileSummariesByIDs(ctx, db, jobFileIDs(activeJobs))
	if err != nil {
		return o, err
	}
	for _, j := range activeJobs {
		o.Jobs = append(o.Jobs, s.toOverviewJob(j, summaries))
	}

	speechRuntimes, err := s.actionableSpeechRuntimes(ctx, db, o, activeJobs)
	if err != nil {
		return o, err
	}
	maxSpeechWorkers := 1
	if o.Stats.Speech.LiveSpeed <= 0 && o.Stats.Speech.AvgSpeed > 0 {
		cards := s.speechCards(ctx)
		atOnce := min(max(set.TranscribeAtOnce, 1), MaxTranscribeAtOnce)
		maxSpeechWorkers = max(1, len(cards)*atOnce)
	}
	o.ETASeconds = computeBacklogETA(
		o,
		activeJobs,
		speechRuntimes,
		len(s.liveStatsForKind(KindTranscribe)),
		len(s.liveStatsForKind(KindTranslate)),
		maxSpeechWorkers,
	)
	return o, nil
}

func (s *Service) actionableSpeechRuntimes(ctx context.Context, db *sql.DB, o Overview, activeJobs []jobs.Job) (map[int64]float64, error) {
	runningSp := o.Running[KindTranscribe]
	queuedSp := o.Queued[KindTranscribe]
	autoSpeechEligible := o.Transcriber.Ready && o.TranscribeMissing

	if autoSpeechEligible {
		runtimes, err := pendingSpeechRuntimes(ctx, db)
		if err != nil {
			return nil, err
		}
		var missingIDs []int64
		for _, j := range activeJobs {
			if j.Kind == KindTranscribe && j.Subject != nil && j.Subject.Type == "file" && j.Subject.ID > 0 {
				if _, ok := runtimes[j.Subject.ID]; !ok {
					missingIDs = append(missingIDs, j.Subject.ID)
				}
			}
		}
		if len(missingIDs) > 0 {
			extra, err := mediafiles.GetFileRuntimesByIDs(ctx, db, missingIDs)
			if err != nil {
				return nil, err
			}
			for id, sec := range extra {
				runtimes[id] = sec
			}
		}
		return runtimes, nil
	}
	if runningSp+queuedSp > 0 {
		var activeIDs []int64
		for _, j := range activeJobs {
			if j.Kind == KindTranscribe && j.Subject != nil && j.Subject.Type == "file" && j.Subject.ID > 0 {
				activeIDs = append(activeIDs, j.Subject.ID)
			}
		}
		return mediafiles.GetFileRuntimesByIDs(ctx, db, activeIDs)
	}
	return nil, nil
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func computeBacklogETA(o Overview, activeJobs []jobs.Job, speechRuntimes map[int64]float64, liveSpeechWorkers, liveTranslateWorkers, maxSpeechWorkers int) int64 {
	runningSp := o.Running[KindTranscribe]
	queuedSp := o.Queued[KindTranscribe]
	autoSpeechEligible := o.Transcriber.Ready && o.TranscribeMissing

	speechCount := 0
	if autoSpeechEligible || runningSp+queuedSp > 0 {
		speechCount = max(len(speechRuntimes), runningSp+queuedSp)
	}

	var speechETA float64
	if speechCount > 0 {
		avgAudioSec := 2400.0
		if o.Stats.Speech.Samples > 0 && o.Stats.Speech.TotalAudioSeconds > 0 {
			avgAudioSec = o.Stats.Speech.TotalAudioSeconds / float64(o.Stats.Speech.Samples)
		} else {
			var sumPos float64
			var countPos int
			for _, sec := range speechRuntimes {
				if sec > 0 {
					sumPos += sec
					countPos++
				}
			}
			if countPos > 0 {
				avgAudioSec = sumPos / float64(countPos)
			}
		}

		var remAudioSec float64
		for _, sec := range speechRuntimes {
			if sec > 0 {
				remAudioSec += sec
			} else {
				remAudioSec += avgAudioSec
			}
		}
		if extra := (runningSp + queuedSp) - len(speechRuntimes); extra > 0 {
			remAudioSec += float64(extra) * avgAudioSec
		}
		for _, j := range activeJobs {
			if j.Kind != KindTranscribe || j.State != "running" {
				continue
			}
			prog := clampFloat(j.Progress, 0, 1)
			if prog <= 0 {
				continue
			}
			dur := avgAudioSec
			if j.Subject != nil && j.Subject.Type == "file" {
				if sec, ok := speechRuntimes[j.Subject.ID]; ok && sec > 0 {
					dur = sec
				}
			}
			remAudioSec -= prog * dur
		}
		if remAudioSec < 0 {
			remAudioSec = 0
		}

		var speechX float64
		if o.Stats.Speech.LiveSpeed > 0 {
			speechX = o.Stats.Speech.LiveSpeed
			if runningSp > liveSpeechWorkers && liveSpeechWorkers > 0 {
				speechX *= float64(runningSp) / float64(liveSpeechWorkers)
			}
		} else if o.Stats.Speech.AvgSpeed > 0 {
			workers := max(1, maxSpeechWorkers)
			if speechCount > 0 && workers > speechCount {
				workers = speechCount
			}
			speechX = o.Stats.Speech.AvgSpeed * float64(workers)
		}

		if remAudioSec > 0 && speechX > 0 {
			speechETA = remAudioSec / speechX
		}
	}

	runningTr := o.Running[KindTranslate]
	queuedTr := o.Queued[KindTranslate]
	directTr := 0
	if o.TranslatorReady {
		directTr = o.CanTranslate
	}
	postSpeechTr := 0
	if o.TranslatorReady && autoSpeechEligible {
		postSpeechTr = max(0, o.Missing-o.CanTranslate-speechCount)
	}
	remSubs := float64(max(runningTr+queuedTr, directTr+postSpeechTr))
	var runningProgSum float64
	for _, j := range activeJobs {
		if j.Kind == KindTranslate && j.State == "running" {
			runningProgSum += clampFloat(j.Progress, 0, 1)
		}
	}
	remSubs = max(0, remSubs-runningProgSum)

	avgLines := 500.0
	if o.Stats.Translate.Samples > 0 && o.Stats.Translate.TotalLines > 0 {
		avgLines = float64(o.Stats.Translate.TotalLines) / float64(o.Stats.Translate.Samples)
	}
	remLines := remSubs * avgLines

	var translateLPS float64
	if o.Stats.Translate.LiveSpeed > 0 {
		translateLPS = o.Stats.Translate.LiveSpeed
		if runningTr > liveTranslateWorkers && liveTranslateWorkers > 0 {
			translateLPS *= float64(runningTr) / float64(liveTranslateWorkers)
		}
	} else if o.Stats.Translate.AvgSpeed > 0 {
		translateLPS = o.Stats.Translate.AvgSpeed
	}

	var translateETA float64
	if remLines > 0 && translateLPS > 0 {
		translateETA = remLines / translateLPS
	}

	eta := max(speechETA, translateETA)
	if eta <= 0 {
		return 0
	}
	return max(int64(1), int64(math.Round(eta)))
}

func (a *Handlers) subtitlesOverview(w http.ResponseWriter, r *http.Request) {
	o, err := a.svc.Overview(r.Context())
	if err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	webserver.WriteJSON(w, http.StatusOK, o)
}

// modelName is a catalogue model's name for a person.
func modelName(id string) (string, bool) {
	m, ok := builtin.ByID(id)
	return m.Name, ok
}
