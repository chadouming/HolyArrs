package shows

import (
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/releasename"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
	"github.com/HolyArrs/holyarrs/app/backend/internal/metadata"
)

// maxSeasonPartNames caps the names kept for one named season.
const maxSeasonPartNames = 8

// A season name that says nothing but its number or kind, in the forms
// details sources use: "Season 2", "Season Two", "2nd Season", "1. Staffel",
// "2ª Temporada", "Сезон 2", "Book 2", "Season 1 Part 2", "Specials",
// "Miniseries", "2".
const (
	seasonWord  = `(?:season|series|staffel|saison|temporada|stagione|seizoen|sezon|sezona|säsong|sæson|sesong|kausi|сезон|シーズン|book|part|volume|vol|chapter|cour)`
	numberWord  = `(?:one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty)`
	ordinalWord = `(?:first|second|third|fourth|fifth|sixth|seventh|eighth|ninth|tenth)`
)

var rePlainSeason = regexp.MustCompile(`(?i)^(?:` +
	seasonWord + `\.?\s*(?:\d+|` + numberWord + `)(?:\s+(?:part|cour)\s*\d+)?` +
	`|(?:\d+\.?[ªºa-z]{0,2}|` + ordinalWord + `)\s+` + seasonWord +
	`|specials?|extras?|mini-?series|limited series|\d*)$`)

// plainSeasonName says whether a season's name tells it apart from no other
// season, so no Release would be named after it.
func plainSeasonName(name string) bool {
	return rePlainSeason.MatchString(strings.TrimSpace(name))
}

// latin says whether a name is written in Latin letters only: a name that
// mixes them with another script ("JOJO的奇妙冒险") counts as the other script.
func latin(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) && !unicode.Is(unicode.Latin, r) {
			return false
		}
	}
	return true
}

// sameAsAny says whether name is one of names, as release names compare.
func sameAsAny(names []string, name string) bool {
	clean := releasename.CleanName(name)
	for _, n := range names {
		if releasename.SameName(n, clean) {
			return true
		}
	}
	return false
}

// partNames lists the names that point at one part of a show. Each source
// adds its own; a new source is one more block here.
//
//  1. The anime map: one entry per AniList title, with the season it sits
//     in and the episode it starts after. When the map could not be read
//     this time (sh.AltStale), the entries it gave before (prev) are kept.
//     The show's own name is left out of a part past the show's start, or
//     every Release under the main name would count inside that part.
//  2. The season's own name: "<show name> <season name>" for the show's name
//     and its other names, for seasons whose name is their own. A season
//     name that already begins with one of the show's names is used as it
//     is. Another name of the show that ends in a season's name belongs to
//     that season only. Names the anime map lists beside the show (a
//     spin-off, the specials) are no name of the show, and a name in Latin
//     letters is not put before a season name in another script, or the
//     other way round. An entry that repeats an anime map name is left out.
func partNames(name string, aliases []string, sh metadata.Show, prev []mediafiles.PartName) []mediafiles.PartName {
	var out []mediafiles.PartName
	var mapNames []string
	addMapPart := func(p mediafiles.PartName) {
		var names []string
		for _, n := range p.Names {
			if (p.Season == 1 && p.Offset == 0) || !sameAsAny([]string{name}, n) {
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			return
		}
		p.Names = names
		out = append(out, p)
		mapNames = append(mapNames, names...)
	}
	if sh.AltStale {
		for _, p := range prev {
			if p.Source == mediafiles.PartFromAnimeMap {
				addMapPart(p)
			}
		}
	} else {
		for _, p := range sh.AnimeParts {
			addMapPart(mediafiles.PartName{Names: p.Names, Season: p.Season, Offset: p.Offset, Source: mediafiles.PartFromAnimeMap})
		}
	}
	var named []string // the clean names of the seasons that have one of their own
	for _, s := range sh.Seasons {
		if s.Number >= 1 && !plainSeasonName(s.Name) {
			named = append(named, releasename.CleanName(s.Name))
		}
	}
	// own are the show's names for the whole show; ofSeason the ones that
	// end in a season's name.
	own := []string{name}
	var ofSeason []string
	for _, a := range aliases {
		clean := releasename.CleanName(a)
		switch {
		case sameAsAny(mapNames, a) || sameAsAny(own, a) || sameAsAny(sh.AnimeSideNames, a):
		case slices.ContainsFunc(named, func(n string) bool { return n != "" && strings.HasSuffix(clean, " "+n) }):
			ofSeason = append(ofSeason, a)
		default:
			own = append(own, a)
		}
	}
	for _, s := range sh.Seasons {
		sname := strings.TrimSpace(s.Name)
		if s.Number < 1 || plainSeasonName(sname) || sameAsAny(own, sname) {
			continue
		}
		clean := releasename.CleanName(sname)
		p := mediafiles.PartName{Season: s.Number, Source: mediafiles.PartFromSeasonName}
		if slices.ContainsFunc(own, func(o string) bool { return strings.HasPrefix(clean, releasename.CleanName(o)+" ") }) {
			p.Names = []string{sname}
		} else {
			for _, o := range own {
				if latin(o) == latin(sname) {
					p.Names = append(p.Names, o+" "+sname)
				}
			}
			for _, a := range ofSeason {
				if strings.HasSuffix(releasename.CleanName(a), " "+clean) && !sameAsAny(p.Names, a) {
					p.Names = append(p.Names, a)
				}
			}
		}
		if len(p.Names) > maxSeasonPartNames {
			p.Names = p.Names[:maxSeasonPartNames]
		}
		if len(p.Names) > 0 && !slices.ContainsFunc(p.Names, func(n string) bool { return sameAsAny(mapNames, n) }) {
			out = append(out, p)
		}
	}
	return out
}
