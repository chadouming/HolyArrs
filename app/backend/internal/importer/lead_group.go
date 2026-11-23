package importer

import (
	"strings"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/releasename"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

// withoutLeadGroup reads a file name again without a leading "group-", the
// way scene files inside RAR sets are named
// ("bravery-the.agency.2024.s01e02.1080p.bluray.x264.mkv"). It is used only
// after the full name matched nothing, and the rest must still name a Title
// in lib, so a hyphenated title is never cut short.
func withoutLeadGroup(base string, h releasename.Hints, lib []mediafiles.TitleRef) (releasename.Result, mediafiles.Match, bool) {
	i := strings.IndexByte(base, '-')
	if i <= 0 || strings.ContainsAny(base[:i], ". _[]()") {
		return releasename.Result{}, mediafiles.Match{}, false
	}
	p := releasename.Parse(base[i+1:], h)
	m := mediafiles.MatchTitle(p, lib)
	if m.Title == nil {
		return releasename.Result{}, mediafiles.Match{}, false
	}
	if p.Group.Value == "" {
		p.Group = releasename.Field[string]{Value: base[:i], Conf: releasename.Likely}
	}
	return p, m, true
}

var sceneStopWords = map[string]bool{
	"the": true, "a": true, "an": true, "of": true, "and": true, "in": true, "on": true, "to": true,
}

// matchSceneAbbrev matches a scene-abbreviated filename (optionally prefixed
// with "group-") against lib, only when at least one context (folder name or
// release name) already corroborates the target title in lib.
func matchSceneAbbrev(base string, h releasename.Hints, lib []mediafiles.TitleRef, contexts ...string) (releasename.Result, mediafiles.Match, bool) {
	var corroborated []mediafiles.TitleRef
	for _, ctx := range contexts {
		if strings.TrimSpace(ctx) == "" {
			continue
		}
		if cm := mediafiles.MatchTitle(releasename.Parse(ctx, h), lib); cm.Title != nil {
			corroborated = append(corroborated, *cm.Title)
		}
	}
	if len(corroborated) == 0 {
		return releasename.Result{}, mediafiles.Match{}, false
	}

	parses := []releasename.Result{releasename.Parse(base, h)}
	if i := strings.IndexByte(base, '-'); i > 0 && !strings.ContainsAny(base[:i], ". _[]()") {
		gp := releasename.Parse(base[i+1:], h)
		if gp.Group.Value == "" {
			gp.Group = releasename.Field[string]{Value: base[:i], Conf: releasename.Likely}
		}
		parses = append(parses, gp)
	}

	for _, p := range parses {
		if h.Show {
			if !p.IsShow() || mediafiles.IsPack(p) {
				continue
			}
		} else if p.IsShow() {
			continue
		}
		candWords := asciiWords(p.Title.Value)
		if len(candWords) == 0 {
			continue
		}
		for i := range corroborated {
			ref := corroborated[i]
			if !h.Show && p.Year.Value > 0 && ref.Year > 0 {
				diff := ref.Year - p.Year.Value
				if diff < -1 || diff > 1 {
					continue
				}
			}
			if matchesSceneTitle(candWords, ref) {
				return p, mediafiles.Match{Title: &ref, Conf: releasename.Likely, Detail: ref.Name + " (scene abbreviation)"}, true
			}
		}
	}
	return releasename.Result{}, mediafiles.Match{}, false
}

func matchesSceneTitle(candWords []string, ref mediafiles.TitleRef) bool {
	if matchesWordList(candWords, asciiWords(ref.Name)) {
		return true
	}
	for _, alias := range ref.Aliases {
		if matchesWordList(candWords, asciiWords(alias)) {
			return true
		}
	}
	return false
}

func matchesWordList(candWords, titleWords []string) bool {
	if len(candWords) == 0 || len(titleWords) == 0 {
		return false
	}
	if len(candWords) == 1 {
		abbr := candWords[0]
		if len(abbr) < 2 {
			return false
		}
		if len(titleWords) == 1 {
			return len(abbr) >= 3 && strings.HasPrefix(titleWords[0], abbr)
		}
		if matchesWordPrefixes(abbr, titleWords) {
			return true
		}
		if sig := significantWords(titleWords); len(sig) >= 2 && matchesWordPrefixes(abbr, sig) {
			return true
		}
		return false
	}
	if len(candWords) != len(titleWords) {
		return false
	}
	for j := range candWords {
		if candWords[j] == "" || !strings.HasPrefix(titleWords[j], candWords[j]) {
			return false
		}
	}
	return true
}

func matchesWordPrefixes(abbr string, words []string) bool {
	if len(words) == 0 || len(abbr) < len(words) {
		return false
	}
	n := len(abbr)
	dp := make([]bool, n+1)
	dp[0] = true
	for _, w := range words {
		next := make([]bool, n+1)
		for i := 0; i < n; i++ {
			if !dp[i] {
				continue
			}
			maxJ := i + len(w)
			if maxJ > n {
				maxJ = n
			}
			for j := i + 1; j <= maxJ; j++ {
				if abbr[j-1] != w[j-1-i] {
					break
				}
				next[j] = true
			}
		}
		dp = next
	}
	return dp[n]
}

func significantWords(words []string) []string {
	var out []string
	for _, w := range words {
		if !sceneStopWords[w] {
			out = append(out, w)
		}
	}
	return out
}

func asciiWords(s string) []string {
	var words []string
	start := -1
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		isAlnum := (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
		if isAlnum {
			if start < 0 {
				start = i
			}
		} else if start >= 0 {
			words = append(words, strings.ToLower(s[start:i]))
			start = -1
		}
	}
	if start >= 0 {
		words = append(words, strings.ToLower(s[start:]))
	}
	return words
}
