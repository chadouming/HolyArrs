package subtitles

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/container"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/subtext"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/transcribe"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/builtin"
)

// KindTranscribe makes a subtitle from the audio of one File.
const KindTranscribe = "subtitle_transcribe"

// Event kinds of a transcript.
const (
	EventTranscribed      = "subtitle_transcribed"
	EventTranscribeFailed = "subtitle_transcribe_failed"
)

const (
	// scoreTranscribed ranks a transcript under every other machine-made
	// subtitle, so anything a site has replaces it.
	scoreTranscribed = 20
	// transcribeTries is how often a transcript that failed for a passing
	// reason is tried by itself; transcribeHold is the wait between tries.
	transcribeTries = 3
	transcribeHold  = 24 * time.Hour
)

// Errors of a transcript.
var (
	ErrHasText       = errors.New("the file already has a text subtitle")
	ErrNoTranscriber = errors.New("subtitles cannot be made from audio here: it needs the HolyArrs image that holds the speech programs, and the speech and voice-detection models downloaded in Settings > Subtitles")
	errTooFewLines   = errors.New("too few lines were heard to make a subtitle")
)

// Transcriber says whether subtitles can be made from audio here.
type Transcriber struct {
	Available bool   `json:"available"` // the programs are in this image
	Ready     bool   `json:"ready"`     // and both models are downloaded
	Model     string `json:"model"`     // the speech model's name
}

// speechModel resolves the speech model to use: explicit setting if valid,
// default speech model if downloaded, any downloaded speech model on disk,
// otherwise the default speech model.
func (s *Service) speechModel(ctx context.Context) (builtin.Model, bool) {
	if s.Store != nil {
		set := ReadSettings(ctx, s.Store.Read())
		if set.TranscribeModel != "" {
			if m, ok := builtin.ByID(set.TranscribeModel); ok && m.Kind == builtin.KindSpeech {
				return m, true
			}
		}
	}
	def, ok := builtin.ByKind(builtin.KindSpeech)
	if ok {
		if _, err := s.modelFor(def.ID); err == nil {
			return def, true
		}
	}
	for _, m := range builtin.AllByKind(builtin.KindSpeech) {
		if _, err := s.modelFor(m.ID); err == nil {
			return m, true
		}
	}
	return def, ok
}

// Transcriber reports whether the speech programs and models are there.
func (s *Service) Transcriber(ctx context.Context) Transcriber {
	var t Transcriber
	if s.Runners != nil {
		_, t.Available = transcribe.Find()
	}
	speech, ok1 := s.speechModel(ctx)
	vad, ok2 := builtin.ByKind(builtin.KindVAD)
	t.Model = speech.Name
	if !t.Available || !ok1 || !ok2 {
		return t
	}
	_, err1 := s.modelFor(speech.ID)
	_, err2 := s.modelFor(vad.ID)
	t.Ready = err1 == nil && err2 == nil
	return t
}

// TranscribeResult is what a transcript did.
type TranscribeResult struct {
	Skipped  string `json:"skipped,omitempty"`
	Language string `json:"language,omitempty"`
	Path     string `json:"path,omitempty"`
	Lines    int    `json:"lines"`
}

// hasText reports whether a File has a text subtitle in any language, beside
// it or inside it. Picture tracks do not count: nothing can be read from them.
func hasText(st Status) bool {
	for _, h := range st.Haves {
		switch h.Place {
		case "beside":
			if textFormat(h.Path) != "" {
				return true
			}
		case "built_in":
			if !h.Picture && container.TextCodec(h.Codec) {
				return true
			}
		}
	}
	return false
}

// wantsTranscript reports whether a search that saved nothing should ask for
// a transcript: the switch is on, the File has no text subtitle in any
// language, the transcriber is ready, and the File is not held back. It does
// not matter why the sites gave nothing.
func (s *Service) wantsTranscript(ctx context.Context, sc *searchCtx) bool {
	return sc.set.TranscribeMissing && !sc.opt.Show && !hasText(sc.status) &&
		s.Transcriber(ctx).Ready && !s.transcriptHeld(ctx, sc.fc.file.ID)
}

// QueueTranscribe asks for a transcript of one File. Each File has at most
// one live transcript Job.
func (s *Service) QueueTranscribe(ctx context.Context, fileID int64, byHand bool) error {
	if s.Enqueue == nil {
		return errors.New("no job queue")
	}
	return s.Enqueue(ctx, KindTranscribe, &domain.Subject{Type: "file", ID: fileID}, map[string]any{"by_hand": byHand}, fmt.Sprintf("%s:%d", KindTranscribe, fileID))
}

// transcribeFailed is a transcript that ran and failed. The Job is not tried
// again on its own: the hold on the File decides when the next try is.
type transcribeFailed struct{ why string }

func (e *transcribeFailed) Error() string { return e.why }
func (e *transcribeFailed) Final() bool   { return true }
func (e *transcribeFailed) Code() string  { return EventTranscribeFailed }

func (s *Service) handleTranscribe(ctx context.Context, r *jobs.Run) error {
	if r.Job.Subject == nil || r.Job.Subject.Type != "file" {
		return errors.New("subtitle_transcribe needs a file subject")
	}
	byHand, _ := r.Job.Params["by_hand"].(bool)
	fileID := r.Job.Subject.ID
	res, err := s.Transcribe(jobProgress(ctx, r), fileID, byHand, jobActor(r))
	switch {
	case err == nil:
		r.SetResult(resultMap(res))
		return nil
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrNotMain), errors.Is(err, ErrMusic), errors.Is(err, ErrHasText):
		r.SetResult(map[string]any{"skipped": err.Error()})
		return nil
	case ctx.Err() != nil:
		return ctx.Err() // stopped: not a failure of the File
	}
	// Nothing was saved. An outcome that will not change is remembered so the
	// File is not transcribed again on every search; a passing failure is
	// tried again after a day.
	final := transcribe.Final(err) || errors.Is(err, errTooFewLines)
	s.noteTranscript(ctx, fileID, final, err.Error())
	if final {
		r.SetResult(map[string]any{"skipped": err.Error()})
		return nil
	}
	return &transcribeFailed{why: err.Error()}
}

var deviceNumber = regexp.MustCompile(`([0-9]+)$`)

// Transcribe makes a subtitle from the audio of a File that has no text
// subtitle in any language, and saves it beside the video in the language
// that is spoken. It then asks for a search, so the translator can make the
// other wanted languages from it by the owner's own rules.
func (s *Service) Transcribe(ctx context.Context, fileID int64, byHand bool, by domain.Actor) (TranscribeResult, error) {
	doneJob := s.speech.enter()
	defer doneJob()
	res := TranscribeResult{}
	fc, err := s.load(ctx, fileID)
	if err != nil {
		return res, err
	}
	if !s.Transcriber(ctx).Ready {
		return res, ErrNoTranscriber
	}
	st, err := s.look(ctx, fc, by)
	if err != nil {
		return res, err
	}
	if hasText(st) {
		return res, ErrHasText
	}
	progs, _ := transcribe.Find()
	speech, _ := s.speechModel(ctx)
	vad, _ := builtin.ByKind(builtin.KindVAD)
	speechFile, err := s.modelFor(speech.ID)
	if err != nil {
		return res, err
	}
	vadFile, err := s.modelFor(vad.ID)
	if err != nil {
		return res, err
	}
	tracks, err := transcribe.Tracks(ctx, progs, fc.video())
	if err != nil {
		return res, s.transcribeFail(ctx, fc, err, byHand, by)
	}
	// Tags are folded to language codes so they can be compared with what is
	// wanted; a tag that is not a language reads as none.
	for i := range tracks {
		tracks[i].Language = baseOf(NormLanguage(tracks[i].Language))
	}
	var wanted []string
	for _, w := range st.Wish.Wants {
		wanted = append(wanted, baseOf(w.Language))
	}
	original := ""
	if fc.title.OriginalLanguage != nil {
		original = baseOf(NormLanguage(*fc.title.OriginalLanguage))
	}
	track, _ := transcribe.Pick(tracks, wanted, original)
	// The terms are those of the language the track says it is in; with no
	// tag, of the first wanted language.
	termsLang := track.Language
	if termsLang == "" && len(wanted) > 0 {
		termsLang = wanted[0]
	}
	set := ReadSettings(ctx, s.Store.Read())
	devs := s.Devices(ctx)
	cards := s.speechCards(ctx)
	atOnce := effectiveTranscribeAtOnce(set.TranscribeAtOnce, cards, devs, speech.NeedBytes)
	// A place on a card, with its speech server: the model stays loaded there
	// between this file's calls and for the files that follow.
	device, server, release, err := s.speech.take(ctx, cards, atOnce)
	if err != nil {
		return res, err
	}
	defer release()
	var extraSlots []transcribe.Slot
	releaseExtra := func() {}
	if device >= 0 && atOnce*len(cards) > 1 {
		extraSlots, releaseExtra = s.speech.takeExtra(cards, atOnce, len(cards)*atOnce-1)
		defer releaseExtra()
	}
	if s.Runners != nil && device >= 0 {
		seenCards := map[int]bool{device: true}
		for _, sl := range extraSlots {
			seenCards[sl.Device] = true
		}
		for card := range seenCards {
			if tightVRAM(cardMemoryMB(devs, card)) {
				s.Runners.StopIdle(fmt.Sprintf("Vulkan%d", card))
			}
		}
	}
	out, err := transcribe.Run(ctx, fc.video(), track, track.Language, transcribe.Options{
		Programs: progs, Model: speechFile.Path, VAD: vadFile.Path, Device: device, Threads: speechThreads(atOnce),
		Server: server, ExtraSlots: extraSlots, Fast: set.TranscribeFast,
		Prompt: s.promptFor(ctx, fc, termsLang), Log: func(line string) { log.Print(line) },
		CacheDir: filepath.Join(s.ModelsDir, ".cache"), // the folder the translator's runner uses
		// The last few percent are the saving of the file.
		Progress: func(p float64) { tellProgress(ctx, p*0.97) },
	})
	releaseExtra()
	release()
	doneJob()
	if err != nil {
		return res, s.transcribeFail(ctx, fc, err, byHand, by)
	}
	lang := NormLanguage(out.Language)
	if lang == "" {
		return res, s.transcribeFail(ctx, fc, fmt.Errorf("%w: %q is not a language HolyArrs knows", transcribe.ErrLanguage, out.Language), byHand, by)
	}
	raw := make([]subtext.Cue, len(out.Cues))
	for i, c := range out.Cues {
		raw[i] = subtext.Cue{StartMS: c.StartMS, EndMS: c.EndMS, Text: c.Text}
	}
	cues := subtext.Shape(raw)
	if subtext.TooFew(cues, out.Seconds) {
		return res, s.transcribeFail(ctx, fc, errTooFewLines, byHand, by)
	}
	// A subtitle may have landed while the audio was read.
	if st, err = s.look(ctx, fc, by); err != nil {
		return res, err
	}
	if hasText(st) {
		res.Skipped = "a subtitle arrived while transcribing"
		return res, nil
	}
	s.translateForeignCues(ctx, fc, lang, cues)
	// A transcript is a machine-made subtitle: marked so, it gives way to
	// one a person made wherever those do.
	made, name, score := "ai_translated", speech.Name, scoreTranscribed
	row := Subtitle{FileID: fc.file.ID, Language: lang, Place: "beside", SiteName: &name, Score: &score, ScoreParts: []Part{},
		Origin: "fetched", State: "present", Sync: map[string]any{}, Made: &made, Transcribed: true}
	p, err := s.save(ctx, fc, &row, subtext.WriteSRT(cues), subtext.SRT, ReadSettings(ctx, s.Store.Read()), nil, by)
	if err != nil {
		return res, err
	}
	res.Language, res.Path, res.Lines = lang, p, len(cues)
	s.clearTranscript(ctx, fc.file.ID)
	s.forgetTerms()
	titleID, slotID := fc.title.ID, fc.file.SlotIDs[0]
	s.event(ctx, &domain.Event{Actor: by, Kind: EventTranscribed, TitleID: &titleID, SlotID: &slotID, GroupKey: "subtitles:" + itoa(fc.file.ID),
		Data:    map[string]any{"file_id": fc.file.ID, "language": lang, "path": p, "lines": len(cues), "model": name, "by_hand": byHand},
		Summary: fmt.Sprintf("Made the %s subtitle from the audio (%d lines)", LanguageName(lang), len(cues))})
	// The other wanted languages are the translator's to make, by its rules.
	if s.Enqueue != nil {
		_ = s.Queue(ctx, fc.file.ID, nil)
	}
	return res, nil
}

// transcribeFail records a transcript that saved nothing and passes its
// error on.
func (s *Service) transcribeFail(ctx context.Context, fc *fileCtx, err error, byHand bool, by domain.Actor) error {
	if ctx.Err() != nil {
		return err
	}
	titleID, slotID := fc.title.ID, fc.file.SlotIDs[0]
	s.event(ctx, &domain.Event{Actor: by, Kind: EventTranscribeFailed, TitleID: &titleID, SlotID: &slotID, GroupKey: "subtitles:" + itoa(fc.file.ID),
		Data:    map[string]any{"file_id": fc.file.ID, "reason": err.Error(), "by_hand": byHand},
		Summary: "Could not make a subtitle from the audio: " + err.Error()})
	return err
}

// speechThreads is how many CPU threads one transcript uses. With several at
// once they share the cores the translator's default would give one.
func speechThreads(atOnce int) int {
	return max(builtin.DefaultThreads()/max(atOnce, 1), 2)
}
