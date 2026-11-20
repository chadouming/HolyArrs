package subtitles

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/importer"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/container"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/subtext"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/transcribe"
)

// Limits of a title's names and terms.
const (
	// MaxGatheredTerms is the most terms gathered from a title's subtitles.
	MaxGatheredTerms = 80
	// maxOwnTerms and maxTermRunes bound the owner's own list.
	maxOwnTerms  = 200
	maxTermRunes = 40
	// maxTermSources is the most subtitles read for one title: enough to
	// tell a show's words from one episode's, without reading every file.
	maxTermSources = 12
	// The settings table takes a fixed set of groups; the lists live in the
	// subtitle one under a key of their own.
	termsGroup  = "what_to_pick"
	termsPrefix = "subtitle_terms:"
	termsKept   = 10 * time.Minute
)

// TitleTerms are the names and terms the transcriber is told for one title
// and language.
type TitleTerms struct {
	Own      []string `json:"own"`      // the owner's list, in their order
	Gathered []string `json:"gathered"` // from the title's subtitles, most used first
	Removed  []string `json:"removed"`  // gathered terms the owner took out
}

type keptTerms struct {
	terms []string
	at    time.Time
}

func termsKey(titleID int64, language string) string {
	return fmt.Sprintf("%d:%s", titleID, language)
}

// cleanTerms makes a list fit to pass on: one line each, no leading dash,
// not too long, no term twice whatever its capitals.
func cleanTerms(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.Join(strings.FieldsFunc(t, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
		t = strings.TrimSpace(strings.TrimLeft(t, "-–— "))
		if t == "" || utf8.RuneCountInString(t) > maxTermRunes || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		if out = append(out, t); len(out) == maxOwnTerms {
			break
		}
	}
	return out
}

func (s *Service) storedTerms(ctx context.Context, titleID int64, language string) (own, removed []string) {
	var v struct {
		Own     []string `json:"own"`
		Removed []string `json:"removed"`
	}
	_ = json.Unmarshal([]byte(database.SettingOr(ctx, s.Store.Read(), termsGroup, termsPrefix+termsKey(titleID, language), "{}")), &v)
	return cleanTerms(v.Own), cleanTerms(v.Removed)
}

// Terms reads a title's lists for one language.
func (s *Service) Terms(ctx context.Context, titleID int64, language string) (TitleTerms, error) {
	own, removed := s.storedTerms(ctx, titleID, language)
	gathered, err := s.gatherTerms(ctx, titleID, language)
	out := TitleTerms{Own: own, Removed: removed, Gathered: []string{}}
	gone := map[string]bool{}
	for _, r := range removed {
		gone[strings.ToLower(r)] = true
	}
	for _, g := range gathered {
		if !gone[strings.ToLower(g)] {
			out.Gathered = append(out.Gathered, g)
		}
	}
	return out, err
}

// SaveTerms stores the owner's own list and the gathered terms they took out.
func (s *Service) SaveTerms(ctx context.Context, titleID int64, language string, own, removed []string, by domain.Actor) (TitleTerms, error) {
	if language = NormLanguage(language); language == "" {
		return TitleTerms{}, ErrBadLanguage
	}
	b, _ := json.Marshal(map[string][]string{"own": cleanTerms(own), "removed": cleanTerms(removed)})
	if err := s.Store.Write(ctx, func(tx *sql.Tx) error {
		return database.PutSetting(tx, termsGroup, termsPrefix+termsKey(titleID, language), string(b), by, time.Now())
	}); err != nil {
		return TitleTerms{}, err
	}
	return s.Terms(ctx, titleID, language)
}

// forgetTerms drops the gathered lists kept in memory.
func (s *Service) forgetTerms() {
	s.mu.Lock()
	s.gathered = nil
	s.mu.Unlock()
}

// gatherTerms reads the title's text subtitles in language that a person made
// (not transcribed, not translated, not a site's machine-made file) and ranks
// the terms in them by how many of those subtitles use each. A term counts
// when two subtitles use it, or when the title has one subtitle and it uses
// the term three times. The list is kept for a while: reading the files is
// the slow part.
func (s *Service) gatherTerms(ctx context.Context, titleID int64, language string) ([]string, error) {
	key := termsKey(titleID, language)
	s.mu.Lock()
	if k, ok := s.gathered[key]; ok && time.Since(k.at) < termsKept {
		s.mu.Unlock()
		return k.terms, nil
	}
	s.mu.Unlock()
	all, err := termSources(ctx, s.Store.Read(), titleID)
	if err != nil {
		return nil, err
	}
	type src = termSource
	var srcs []src
	for _, x := range all {
		if x.language != language && !LangMatches(language, x.language) {
			continue
		}
		if x.place == "built_in" && (x.track < 0 || !container.TextCodec(x.codec)) {
			continue
		}
		if x.place == "beside" && textFormat(x.path) == "" {
			continue
		}
		srcs = append(srcs, x)
	}
	if len(srcs) > maxTermSources { // spread over the title, not its first episodes
		picked := make([]src, 0, maxTermSources)
		for i := 0; i < maxTermSources; i++ {
			picked = append(picked, srcs[i*len(srcs)/maxTermSources])
		}
		srcs = picked
	}
	docs, uses := map[string]int{}, map[string]int{}
	shown := map[string]string{}
	read := 0
	for _, x := range srcs {
		var cues []subtext.Cue
		if x.place == "built_in" {
			cues, err = container.ExtractText(ctx, filepath.Join(x.lib, filepath.FromSlash(x.video)), x.track)
		} else {
			cues, err = readBeside(x.lib, x.path)
		}
		if err != nil || len(cues) == 0 {
			continue
		}
		read++
		var sb strings.Builder
		for _, c := range cues {
			sb.WriteString(c.Text)
			sb.WriteString("\n")
		}
		seen := map[string]bool{}
		for _, t := range termsOf(sb.String(), language) {
			k := strings.ToLower(t)
			uses[k]++
			if !seen[k] {
				seen[k] = true
				docs[k]++
			}
			if _, ok := shown[k]; !ok {
				shown[k] = t
			}
		}
	}
	var keys []string
	for k := range docs {
		if docs[k] >= 2 || (read == 1 && uses[k] >= 3) {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if docs[a] != docs[b] {
			return docs[a] > docs[b]
		}
		if uses[a] != uses[b] {
			return uses[a] > uses[b]
		}
		return a < b
	})
	if len(keys) > MaxGatheredTerms {
		keys = keys[:MaxGatheredTerms]
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = shown[k]
	}
	s.mu.Lock()
	if s.gathered == nil {
		s.gathered = map[string]keptTerms{}
	}
	s.gathered[key] = keptTerms{out, time.Now()}
	s.mu.Unlock()
	return out, nil
}

// readBeside reads the cues of a subtitle file in the Library folder.
func readBeside(lib, rel string) ([]subtext.Cue, error) {
	full := filepath.Join(lib, filepath.FromSlash(rel))
	if !importer.InsideReal(lib, full) {
		return nil, fmt.Errorf("%s is outside the Library folder", rel)
	}
	fi, err := os.Lstat(full)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > subtext.MaxText {
		return nil, fmt.Errorf("%s cannot be read as a subtitle", rel)
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	format := textFormat(rel)
	text, err := subtext.Clean(b, format)
	if err != nil {
		return nil, err
	}
	return subtext.ParseCues(text, format)
}

var (
	katakana = regexp.MustCompile(`[\p{Katakana}ー]{3,}`)
	tagText  = regexp.MustCompile(`<[^>]*>|\{[^}]*\}`)
	// small words that may sit inside a name: "Vale of Thorns", "Jean de Mar".
	joiners = map[string]bool{"of": true, "the": true, "de": true, "du": true, "la": true, "le": true, "von": true, "van": true, "al": true, "del": true, "da": true}
	// capitalised wherever they stand, and never a show's own term.
	plain = map[string]bool{"i": true, "i'm": true, "i'll": true, "i've": true, "i'd": true, "ok": true, "okay": true, "mr": true, "mrs": true, "ms": true, "dr": true,
		"monday": true, "tuesday": true, "wednesday": true, "thursday": true, "friday": true, "saturday": true, "sunday": true,
		"january": true, "february": true, "march": true, "april": true, "may": true, "june": true, "july": true, "august": true,
		"september": true, "october": true, "november": true, "december": true, "god": true}
)

// termsOf picks the names and invented words out of one subtitle's text. For
// a language written with capitals these are runs of up to four capitalised
// words that do not open a sentence, and such a run's own words; a word that
// the same text also writes without a capital is an ordinary word and is left
// out. For Japanese they are runs of katakana. Other scripts give nothing.
func termsOf(text, language string) []string {
	text = tagText.ReplaceAllString(text, " ")
	switch baseOf(language) {
	case "ja":
		return katakana.FindAllString(text, -1)
	case "zh", "ko", "th", "ar", "he", "fa", "hi", "bn", "ta":
		return nil
	}
	lower := map[string]bool{}
	type word struct {
		text  string
		first bool // opens a sentence or a line of dialogue
		ends  bool // a sentence ends after it
	}
	var words []word
	start := true
	for _, line := range strings.Split(text, "\n") {
		start = true // a subtitle line may open a sentence without saying so
		for _, raw := range strings.Fields(line) {
			if strings.Trim(raw, "-–—") == "" {
				start = true
				continue
			}
			w := strings.TrimFunc(raw, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
			ends := strings.ContainsAny(strings.TrimRight(raw, `"'”’)]`)[max(len(strings.TrimRight(raw, `"'”’)]`))-1, 0):], ".?!…:")
			if w != "" {
				if r, _ := utf8.DecodeRuneInString(w); unicode.IsLower(r) {
					lower[strings.ToLower(w)] = true
				}
				words = append(words, word{w, start, ends || strings.HasSuffix(raw, ",") && false})
			}
			start = ends
		}
	}
	isName := func(w word) bool {
		r, _ := utf8.DecodeRuneInString(w.text)
		return unicode.IsUpper(r) && !plain[strings.ToLower(w.text)] && utf8.RuneCountInString(w.text) > 1
	}
	var out []string
	add := func(t string) {
		if !lower[strings.ToLower(t)] && utf8.RuneCountInString(t) <= maxTermRunes {
			out = append(out, t)
		}
	}
	for i := 0; i < len(words); i++ {
		if words[i].first || !isName(words[i]) {
			continue
		}
		run := []string{words[i].text}
		j := i
		for len(run) < 4 && !words[j].ends && j+1 < len(words) {
			next := words[j+1]
			switch {
			case isName(next):
				run = append(run, next.text)
				j++
				continue
			case joiners[strings.ToLower(next.text)] && j+2 < len(words) && !next.ends && isName(words[j+2]) && len(run) < 3:
				run = append(run, next.text, words[j+2].text)
				j += 2
				continue
			}
			break
		}
		if len(run) > 1 {
			add(strings.Join(run, " "))
		}
		for _, w := range run {
			if !joiners[strings.ToLower(w)] {
				add(w)
			}
		}
		i = j
	}
	return out
}

// promptFor builds the text the transcriber is given for a file: the owner's
// own terms, then the gathered ones, then the title's and the episode's own
// names, until the room is used. The owner's terms always come first.
func (s *Service) promptFor(ctx context.Context, fc *fileCtx, language string) string {
	t, _ := s.Terms(ctx, fc.title.ID, language)
	list := append(append([]string{}, t.Own...), t.Gathered...)
	list = append(list, fc.title.Name)
	if fc.item.Name != nil {
		list = append(list, *fc.item.Name)
	}
	var parts []string
	room := transcribe.MaxPrompt
	seen := map[string]bool{}
	for _, term := range cleanTerms(list) {
		k := strings.ToLower(term)
		if seen[k] || len(term)+2 > room {
			continue
		}
		seen[k] = true
		parts = append(parts, term)
		room -= len(term) + 2
	}
	return strings.Join(parts, ", ")
}
