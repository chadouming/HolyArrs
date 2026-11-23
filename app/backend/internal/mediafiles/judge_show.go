package mediafiles

import (
	"fmt"
	"slices"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/releasename"
)

// matchShow picks the show among same-named candidates. Show names carry
// no year as a rule, so a single candidate is sure; a year in the name
// tells shows of the same name apart.
func matchShow(p releasename.Result, cands []*TitleRef) Match {
	if p.Year.Value > 0 {
		for _, t := range cands {
			if t.Year == p.Year.Value {
				return Match{Title: t, Conf: releasename.Sure, Detail: fmt.Sprintf("%s (%d) by clean name and year", t.Name, t.Year)}
			}
		}
	}
	if len(cands) == 1 {
		return Match{Title: cands[0], Conf: releasename.Sure, Detail: cands[0].Name + " by clean name"}
	}
	return Match{Conf: releasename.Unknown, Detail: fmt.Sprintf("%d shows are called %q and the name gives no year that tells them apart", len(cands), p.Title.Value)}
}

// yearInName says the year after a show's name is part of one of the
// show's names ("Space: 1999", or another name that ends in a year).
func yearInName(p releasename.Result, t *TitleRef) bool {
	if !p.IsShow() || p.Year.Value == 0 {
		return false
	}
	full := releasename.CleanName(fmt.Sprintf("%s %d", p.Title.Value, p.Year.Value))
	return releasename.SameName(t.Name, full) || slices.ContainsFunc(t.Aliases, func(a string) bool { return releasename.SameName(a, full) })
}

// MatchRelease is MatchTitle for a Release being chosen (search results and
// feeds), as the older apps tie a release to a show: a year right after a
// show's name must be the year the show began, unless the name with that
// year is one of the show's names. Any other year names another show of
// the same name. Files on disk and finished downloads keep MatchTitle,
// which allows any year. A year after the name of one of the show's parts is
// that part's year, so it is allowed too.
func MatchRelease(p releasename.Result, lib []TitleRef) Match {
	m := MatchTitle(p, lib)
	if t := m.Title; t != nil && p.IsShow() && p.Year.Value > 0 && t.Year > 0 && t.Year != p.Year.Value && !yearInName(p, t) && !namesPart(p, t) {
		return Match{Conf: releasename.Unknown, Detail: fmt.Sprintf("the name says %d; this show began in %d", p.Year.Value, t.Year)}
	}
	return m
}

// episodeOrder ranks two show Releases of equal quality, points and
// protocol: a season pack first; then, for anime (running numbers), more
// episodes; for other shows, fewer; then the lower first episode.
func episodeOrder(a, b releasename.Result) (int, string) {
	if !a.IsShow() || !b.IsShow() {
		return 0, ""
	}
	if pa, pb := isPack(a), isPack(b); pa != pb {
		if pa {
			return -1, "on season pack"
		}
		return 1, "on season pack"
	}
	na, nb := episodeCount(a), episodeCount(b)
	if na != nb {
		more := a.Kind.Value == releasename.KindAbsolute || b.Kind.Value == releasename.KindAbsolute
		if (na > nb) == more {
			return -1, "on episodes held"
		}
		return 1, "on episodes held"
	}
	if fa, fb := firstEpisode(a), firstEpisode(b); fa != fb {
		if fa < fb {
			return -1, "on episode number"
		}
		return 1, "on episode number"
	}
	return 0, ""
}

func episodeCount(p releasename.Result) int {
	if p.Show == nil {
		return 0
	}
	return max(len(p.Show.Episodes), len(p.Show.Absolute), 1)
}

func firstEpisode(p releasename.Result) int {
	switch {
	case p.Show == nil:
		return 0
	case len(p.Show.Episodes) > 0:
		return p.Show.Episodes[0]
	case len(p.Show.Absolute) > 0:
		return p.Show.Absolute[0]
	}
	return 0
}

// Gains says whether a Release would be a better copy than a File in
// place under the rules (pick-rules.md section 4); no File in place is
// always a gain. Used per episode for season packs.
func Gains(rel ReleaseFacts, rules PickRulesBody, cur *Current) bool {
	if cur == nil {
		return true
	}
	idx := stepIndex(rules, StepFor(rel.Parse))
	if idx < 0 || !rules.Steps[idx].Allowed {
		return false
	}
	var v Verdict
	v.better(rel, rules, cur, idx, sumPoints(rel.Parse, rules), 0)
	return !v.failed()
}

// taggedShows finds shows whose name, or an alias, matches once a trailing
// country or year mark is left off ("The Office (US)" for "The.Office.S01E01"),
// the way scene names leave the mark out. A release that names a year must
// agree with the show's year, so "Doctor.Who.1963" never takes "Doctor Who (2005)".
// A release naming another country never gets here: "office uk" is not "office".
func taggedShows(p releasename.Result, name string, lib []TitleRef) []*TitleRef {
	var cands []*TitleRef
	for i := range lib {
		t := &lib[i]
		if t.Kind != "show" {
			continue
		}
		for _, n := range append([]string{t.Name}, t.Aliases...) {
			base, _, year, ok := releasename.SplitNameTag(n)
			if !ok || !releasename.SameName(base, name) {
				continue
			}
			if year == 0 {
				year = t.Year
			}
			if p.Year.Value > 0 && year > 0 && year != p.Year.Value {
				continue
			}
			cands = append(cands, t)
			break
		}
	}
	return cands
}

// withTaggedShows adds the second try: shows whose name carries a country
// or year mark. It runs when the plain names found nothing, and also when
// the release names a year none of them has, so a plain-named original
// never takes a release of a year-marked remake.
func withTaggedShows(p releasename.Result, name string, lib []TitleRef, cands []*TitleRef) []*TitleRef {
	if len(cands) == 0 {
		return taggedShows(p, name, lib)
	}
	if p.Year.Value == 0 {
		return cands
	}
	for _, t := range cands {
		if t.Year == p.Year.Value {
			return cands
		}
	}
	var dated []*TitleRef
	for _, t := range taggedShows(p, name, lib) {
		if t.Year == p.Year.Value || tagYear(t, name) == p.Year.Value {
			dated = append(dated, t)
		}
	}
	if len(dated) > 0 {
		return dated
	}
	return cands
}

// tagYear is the year in a show's name mark that fits the base name; 0: none.
func tagYear(t *TitleRef, name string) int {
	for _, n := range append([]string{t.Name}, t.Aliases...) {
		if base, _, year, ok := releasename.SplitNameTag(n); ok && year > 0 && releasename.SameName(base, name) {
			return year
		}
	}
	return 0
}
