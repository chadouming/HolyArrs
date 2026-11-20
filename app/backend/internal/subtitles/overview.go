package subtitles

import (
	"context"
	"net/http"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/webserver"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/builtin"
)

// overviewKinds are the job kinds the Subtitles page follows.
var overviewKinds = []string{KindTranscribe, KindTranslate, KindSearch, KindMissing, KindBetter, KindLook, KindModelDownload}

// overviewJobs is how many jobs the page lists; the counts cover more.
const (
	overviewDone  = 25
	overviewJobs  = 30
	overviewCount = 500
)

// OverviewJob is one subtitle job that runs or waits.
type OverviewJob struct {
	ID        int64      `json:"id"`
	Kind      string     `json:"kind"`
	State     string     `json:"state"` // running or queued
	Progress  float64    `json:"progress"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	FileID    int64      `json:"file_id,omitempty"`
	TitleID   int64      `json:"title_id,omitempty"`
	Name      string     `json:"name,omitempty"` // the movie or show
	Season    *int       `json:"season,omitempty"`
	Episode   *int       `json:"episode,omitempty"`
	Language  string     `json:"language,omitempty"` // of a translation
	Model     string     `json:"model,omitempty"`    // of a model download
}

// Overview is what the Subtitles page shows above its list: what is
// missing and why, whether the machines that can make a subtitle are ready,
// and the subtitle jobs that run or wait.
type Overview struct {
	MissingCounts
	Transcriber       Transcriber    `json:"transcriber"`
	TranscribeMissing bool           `json:"transcribe_missing"`
	TranslatorReady   bool           `json:"translator_ready"`
	TranslatorName    string         `json:"translator_name"`
	Done              []DoneRow      `json:"done"` // the newest subtitles HolyArrs saved
	DoneCounts        DoneCounts     `json:"done_counts"`
	Jobs              []OverviewJob  `json:"jobs"`
	Running           map[string]int `json:"running"` // by kind
	Queued            map[string]int `json:"queued"`  // by kind; a count of overviewCount means that many or more
}

// Overview reads the page's numbers and jobs.
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	db := s.Store.Read()
	o := Overview{Jobs: []OverviewJob{}, Running: map[string]int{}, Queued: map[string]int{}}
	var err error
	if o.MissingCounts, err = missingCounts(ctx, db); err != nil {
		return o, err
	}
	if o.Done, o.DoneCounts, err = Done(ctx, db, overviewDone); err != nil {
		return o, err
	}
	o.Transcriber = s.Transcriber(ctx)
	o.TranscribeMissing = ReadSettings(ctx, db).TranscribeMissing
	tr := s.Translator(ctx)
	o.TranslatorReady, o.TranslatorName = tr.Ready(), tr.Name()
	marks, args := "", []any{}
	for i, k := range overviewKinds {
		if i > 0 {
			marks += ", "
		}
		marks += "?"
		args = append(args, k)
	}
	js, err := database.Select[jobs.Job](ctx, db, "jobs",
		"kind IN ("+marks+") AND state IN ('running', 'queued') ORDER BY CASE state WHEN 'running' THEN 0 ELSE 1 END, run_after, id LIMIT ?", append(args, overviewCount)...)
	if err != nil {
		return o, err
	}
	for _, j := range js {
		if j.State == "running" {
			o.Running[j.Kind]++
		} else {
			o.Queued[j.Kind]++
		}
		if len(o.Jobs) >= overviewJobs {
			continue
		}
		v := OverviewJob{ID: j.ID, Kind: j.Kind, State: j.State, Progress: j.Progress, StartedAt: j.StartedAt}
		v.Language, _ = j.Params["language"].(string)
		if id, _ := j.Params["id"].(string); j.Kind == KindModelDownload && id != "" {
			v.Model = id
			if m, ok := modelName(id); ok {
				v.Model = m
			}
		}
		if j.Subject != nil && j.Subject.Type == "file" {
			v.FileID = j.Subject.ID
			if fc, err := s.load(ctx, v.FileID); err == nil {
				v.TitleID, v.Name = fc.title.ID, fc.title.Name
				v.Season, v.Episode = fc.item.Numbers.Season, fc.item.Numbers.Episode
			}
		}
		o.Jobs = append(o.Jobs, v)
	}
	return o, nil
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
