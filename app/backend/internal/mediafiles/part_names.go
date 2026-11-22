package mediafiles

import (
	"fmt"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/releasename"
)

// Where a part name comes from.
const (
	PartFromAnimeMap   = "anime_map"   // the metadata server's anime map: one AniList title
	PartFromSeasonName = "season_name" // the show's name with the season's own name
)

// PartName is a set of names that point at one part of a show: episode N
// of the part is episode Offset+N of Season, in the show's stored
// (aired) numbering. A Release named "<part name> - 20" is the part's
// episode 20, not the show's running number 20.
type PartName struct {
	Names  []string `json:"names"`
	Season int      `json:"season"`
	Offset int      `json:"offset"`
	Source string   `json:"source"`
}

// PartsFor are the part names a show's Releases are matched with: none
// for a movie or an artist, and none for a show that follows another
// episode order (a TMDB episode group, or absolute numbering), because the
// seasons stored for it are not the ones the part names count in.
func PartsFor(t Title) []PartName {
	if t.Kind != "show" || (t.EpisodeGroupID != nil && *t.EpisodeGroupID != "") {
		return nil
	}
	if t.EpisodeOrder != nil && *t.EpisodeOrder != "" && *t.EpisodeOrder != OrderAired {
		return nil
	}
	return t.PartNames
}

// findPart finds the part a Release title names, with the name that
// matched. The first part that has the name wins.
func findPart(parts []PartName, title string) (PartName, string, bool) {
	if len(parts) == 0 {
		return PartName{}, "", false
	}
	clean := releasename.CleanName(title)
	for _, p := range parts {
		for _, n := range p.Names {
			if releasename.SameName(n, clean) {
				return p, n, true
			}
		}
	}
	return PartName{}, "", false
}

// byPartName makes a show match say so when the Release named one of the
// show's parts, so the season it points at shows in the Release's reasons.
func byPartName(p releasename.Result, m Match) Match {
	if m.Title == nil {
		return m
	}
	if part, name, ok := findPart(m.Title.Parts, p.Title.Value); ok {
		m.Detail = fmt.Sprintf("%s by its part name %s (season %d)", m.Title.Name, name, part.Season)
	}
	return m
}

// partEpisode finds episode n of a part: episode Offset+n of its season.
// The count does not stop where the next part of the season starts: groups
// that name a whole season after its first part count straight through it
// ("Stone Ocean-25" where the second part starts after episode 12).
func partEpisode(part PartName, eps []EpisodeRef, n int) (EpisodeRef, bool) {
	if n < 1 {
		return EpisodeRef{}, false
	}
	for _, e := range eps {
		if e.Season == part.Season && e.Episode == part.Offset+n {
			return e, true
		}
	}
	return EpisodeRef{}, false
}

// namesPart says the Release is named after one of the show's parts.
func namesPart(p releasename.Result, t *TitleRef) bool {
	_, _, ok := findPart(t.Parts, p.Title.Value)
	return ok
}
