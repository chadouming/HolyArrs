package mediafiles

import (
	"fmt"
	"slices"
	"strings"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/releasename"
)

// EpisodeRef is one episode of a show as the matcher sees it.
type EpisodeRef struct {
	ItemID   int64
	Season   int
	Episode  int
	Absolute int    // 0: none
	AirDay   string // YYYY-MM-DD, "" when unknown
	// AltSeason and AltEpisode are how release names number the episode
	// when that differs (an anime part TMDB keeps inside one season); 0: none.
	AltSeason  int
	AltEpisode int
}

// Label names an episode the way people write it: S01E02.
func (e EpisodeRef) Label() string { return fmt.Sprintf("S%02dE%02d", e.Season, e.Episode) }

// Named is the season and episode release names use for the episode.
func (e EpisodeRef) Named() (int, int) {
	if e.AltSeason > 0 {
		return e.AltSeason, e.AltEpisode
	}
	return e.Season, e.Episode
}

// isPack says the Release holds whole seasons.
func isPack(p releasename.Result) bool {
	switch p.Kind.Value {
	case releasename.KindSeasonPack, releasename.KindMultiSeason, releasename.KindComplete:
		return true
	}
	return false
}

// IsPack says a show Release holds one or more whole seasons.
func IsPack(p releasename.Result) bool { return isPack(p) }

// MatchEpisodes finds the episodes of a show that a Release holds, using
// the show's episode order (aired, daily or absolute). It returns the
// episodes found and, when some numbers name no episode, why. parts are the
// show's part names (PartsFor; nil for none): a running number under a part's
// name counts inside that part.
func MatchEpisodes(p releasename.Result, order string, eps []EpisodeRef, parts []PartName) ([]EpisodeRef, string) {
	sh := p.Show
	if sh == nil {
		return nil, "the name gives no episode numbers"
	}
	if sh.Conf == releasename.Guess {
		return nil, strings.Join(p.Notes, "; ") + "; pick the episode by hand"
	}
	var out []EpisodeRef
	find := func(ok func(EpisodeRef) bool) {
		for _, e := range eps {
			if ok(e) {
				out = append(out, e)
			}
		}
	}
	switch p.Kind.Value {
	case releasename.KindSeasonPack, releasename.KindMultiSeason:
		// An episode sits in the season release names give it; the stored
		// season counts only when that finds nothing.
		find(func(e EpisodeRef) bool { s, _ := e.Named(); return slices.Contains(sh.Seasons, s) })
		if len(out) == 0 {
			find(func(e EpisodeRef) bool { return slices.Contains(sh.Seasons, e.Season) })
		}
		if len(out) == 0 {
			return nil, fmt.Sprintf("the show has no episodes in season %s", seasonList(sh.Seasons))
		}
		return out, ""
	case releasename.KindComplete:
		find(func(e EpisodeRef) bool { return e.Season > 0 })
		if len(out) == 0 {
			return nil, "the show has no episodes yet"
		}
		return out, ""
	case releasename.KindDaily:
		find(func(e EpisodeRef) bool { return e.AirDay == sh.AirDate })
		if len(out) == 0 {
			return nil, "no episode aired on " + sh.AirDate
		}
		return out, ""
	case releasename.KindAbsolute:
		// A Release named after one part of the show counts from that part's
		// start, inside its season, when every number fits. Else the numbers
		// are the show's running numbers, all of them: a range never mixes
		// the two.
		part, partName, inPart := findPart(parts, p.Title.Value)
		if inPart {
			for _, n := range sh.Absolute {
				e, ok := partEpisode(part, eps, n)
				if !ok {
					out = nil
					break
				}
				out = append(out, e)
			}
			if len(out) > 0 {
				return out, ""
			}
		}
		var missing []string
		for _, n := range sh.Absolute {
			i := slices.IndexFunc(eps, func(e EpisodeRef) bool { return e.Absolute == n })
			if i < 0 {
				missing = append(missing, fmt.Sprint(n))
				continue
			}
			out = append(out, eps[i])
		}
		if len(missing) > 0 && inPart {
			return nil, fmt.Sprintf("%s (season %d) has no episode %s, and no episode has that running number", partName, part.Season, strings.Join(missing, ", "))
		}
		if len(missing) > 0 {
			return nil, "no episode has the running number " + strings.Join(missing, ", ")
		}
		return out, ""
	}
	// Season and episode numbers (episodes, specials, mini-series parts).
	if len(sh.Seasons) == 0 || len(sh.Episodes) == 0 {
		return nil, "the name gives no episode numbers"
	}
	season := sh.Seasons[0]
	var missing []string
	for _, n := range sh.Episodes {
		// Release-name numbers first, as the old apps read scene numbers;
		// then the stored ones.
		i := slices.IndexFunc(eps, func(e EpisodeRef) bool { s, x := e.Named(); return s == season && x == n })
		if i < 0 {
			i = slices.IndexFunc(eps, func(e EpisodeRef) bool { return e.Season == season && e.Episode == n })
		}
		if i < 0 {
			missing = append(missing, fmt.Sprintf("S%02dE%02d", season, n))
			continue
		}
		out = append(out, eps[i])
	}
	if len(missing) > 0 {
		if fb := matchSeasonalFallback(p, eps); len(fb) > 0 {
			return fb, ""
		}
		return nil, "the show has no episode " + strings.Join(missing, ", ")
	}
	_ = order // every order keeps season and episode numbers; daily and absolute names are matched above
	return out, ""
}

// matchSeasonalFallback handles continuous vs. per-season-reset episode
// numbering within a single regular season when exact matching fails for the
// release. It never activates for specials or multi-episode mixes.
func matchSeasonalFallback(p releasename.Result, eps []EpisodeRef) []EpisodeRef {
	if p.Kind.Value != releasename.KindEpisode && p.Kind.Value != releasename.KindMultiEpisode {
		return nil
	}
	sh := p.Show
	if sh == nil || sh.Special || len(sh.Seasons) == 0 || len(sh.Episodes) == 0 {
		return nil
	}
	season := sh.Seasons[0]
	if season <= 0 {
		return nil
	}
	for _, n := range sh.Episodes {
		if n <= 0 {
			return nil
		}
	}
	views := [2]func(EpisodeRef) (int, int){
		EpisodeRef.Named,
		func(e EpisodeRef) (int, int) { return e.Season, e.Episode },
	}
	for _, view := range views {
		if out := matchSeasonAbsolute(eps, *sh, view); len(out) > 0 {
			return out
		}
	}
	for _, view := range views {
		if out := matchSeasonOffset(eps, *sh, view); len(out) > 0 {
			return out
		}
	}
	return nil
}

// matchSeasonAbsolute matches every requested episode n against e.Absolute
// within season (as seen through view) when metadata resets per season.
func matchSeasonAbsolute(eps []EpisodeRef, sh releasename.ShowNumbers, view func(EpisodeRef) (int, int)) []EpisodeRef {
	season := sh.Seasons[0]
	out := make([]EpisodeRef, 0, len(sh.Episodes))
	for _, n := range sh.Episodes {
		i := slices.IndexFunc(eps, func(e EpisodeRef) bool {
			s, _ := view(e)
			return s == season && e.Absolute > 0 && e.Absolute == n
		})
		if i < 0 {
			return nil
		}
		out = append(out, eps[i])
	}
	return out
}

// matchSeasonOffset maps per-season release numbers (1, 2, ...) onto a season
// whose metadata continues numbering from prior regular seasons (minEp > 1).
func matchSeasonOffset(eps []EpisodeRef, sh releasename.ShowNumbers, view func(EpisodeRef) (int, int)) []EpisodeRef {
	season := sh.Seasons[0]
	if season <= 1 {
		return nil
	}
	var (
		hasMin  bool
		minEp   int
		first   EpisodeRef
		prevMax int
	)
	for _, e := range eps {
		s, ep := view(e)
		if ep <= 0 {
			continue
		}
		if s == season && (!hasMin || ep < minEp) {
			hasMin, minEp, first = true, ep, e
		} else if s > 0 && s < season && ep > prevMax {
			prevMax = ep
		}
	}
	if !hasMin || minEp <= 1 {
		return nil
	}
	if first.Absolute != minEp && (prevMax <= 0 || minEp != prevMax+1) {
		return nil
	}
	out := make([]EpisodeRef, 0, len(sh.Episodes))
	for _, n := range sh.Episodes {
		want := minEp + n - 1
		i := slices.IndexFunc(eps, func(e EpisodeRef) bool {
			s, ep := view(e)
			return s == season && ep == want
		})
		if i < 0 {
			return nil
		}
		out = append(out, eps[i])
	}
	return out
}

func seasonList(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = fmt.Sprint(n)
	}
	return strings.Join(parts, ", ")
}
