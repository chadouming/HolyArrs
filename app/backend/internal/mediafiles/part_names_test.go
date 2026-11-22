package mediafiles

import (
	"strings"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/releasename"
)

// jojo is a show with four seasons (26, 48, 39 and 39 episodes, running
// numbers 1 to 152) and three named parts; season 2 holds two of them.
func jojo() (TitleRef, []EpisodeRef) {
	var eps []EpisodeRef
	abs := 0
	for s, n := range []int{26, 48, 39, 39} {
		for e := 1; e <= n; e++ {
			abs++
			eps = append(eps, EpisodeRef{ItemID: int64(abs), Season: s + 1, Episode: e, Absolute: abs})
		}
	}
	parts := []PartName{
		{Names: []string{"JoJo no Kimyou na Bouken: Stardust Crusaders", "JoJo's Bizarre Adventure: Stardust Crusaders"}, Season: 2, Offset: 0, Source: PartFromAnimeMap},
		{Names: []string{"JoJo no Kimyou na Bouken: Stardust Crusaders - Egypt-hen"}, Season: 2, Offset: 24, Source: PartFromAnimeMap},
		{Names: []string{"JoJo no Kimyou na Bouken: Ougon no Kaze", "JoJo's Bizarre Adventure: Golden Wind"}, Season: 4, Offset: 0, Source: PartFromAnimeMap},
	}
	// The anime map's names are stored as plain other names too.
	aliases := []string{"JoJo no Kimyou na Bouken", "JoJo no Kimyou na Bouken: Stardust Crusaders", "JoJo no Kimyou na Bouken: Ougon no Kaze"}
	return TitleRef{ID: 4425, Name: "JoJo's Bizarre Adventure", Year: 2012, Kind: "show", Aliases: aliases, Parts: parts}, eps
}

func TestPartNameCountsTheNumberInsideThePart(t *testing.T) {
	show, eps := jojo()
	for _, c := range []struct {
		name            string
		season, episode int
	}{
		// The number is the part's own: Golden Wind 20 is season 4 episode 20.
		{"[Moozzi2] JoJo no Kimyou na Bouken Ougon no Kaze-20 [BD 1920x1080 x265-10Bit 2Audio]", 4, 20},
		{"[Moozzi2] JoJo no Kimyou na Bouken Ougon no Kaze - 20 [BD 1920x1080 x265-10Bit 2Audio]", 4, 20},
		{"[Group] JoJo's Bizarre Adventure - Golden Wind - 01 [1080p]", 4, 1},
		// Two parts in one season: the second starts after episode 24.
		{"[Group] JoJo no Kimyou na Bouken - Stardust Crusaders - 24 [1080p]", 2, 24},
		{"[Group] JoJo no Kimyou na Bouken - Stardust Crusaders - Egypt-hen - 01 [1080p]", 2, 25},
		{"[Group] JoJo no Kimyou na Bouken - Stardust Crusaders - Egypt-hen - 24 [1080p]", 2, 48},
		// A group that counts a whole season under its first part's name: past
		// the part's end the number goes on inside the season.
		{"[Group] JoJo no Kimyou na Bouken - Stardust Crusaders - 25 [1080p]", 2, 25},
		{"[Group] JoJo no Kimyou na Bouken - Stardust Crusaders - 48 [1080p]", 2, 48},
		// Past the season's end, a number that is a running number of the show is one.
		{"[Group] JoJo no Kimyou na Bouken - Stardust Crusaders - 49 [1080p]", 2, 23},
		{"[Group] JoJo no Kimyou na Bouken - Ougon no Kaze - 120 [1080p]", 4, 7},
		// The show's own name counts across the whole show, as before.
		{"[Group] JoJo's Bizarre Adventure - 20 [1080p]", 1, 20},
		{"[Group] JoJo no Kimyou na Bouken - 133 [1080p]", 4, 20},
	} {
		p := releasename.Parse(c.name, releasename.Hints{})
		m := MatchRelease(p, []TitleRef{show})
		if m.Title == nil || m.Conf != releasename.Sure {
			t.Errorf("%s: no show: %s", c.name, m.Detail)
			continue
		}
		got, why := MatchEpisodes(p, "aired", eps, show.Parts)
		if len(got) != 1 || got[0].Season != c.season || got[0].Episode != c.episode {
			t.Errorf("%s: got %+v (%s), want season %d episode %d", c.name, got, why, c.season, c.episode)
		}
	}
}

// A number that names no episode of the part and no running number is
// refused, and the reason names the part.
func TestPartNameNumberThatFitsNothingIsRefused(t *testing.T) {
	show, eps := jojo()
	p := releasename.Parse("[Group] JoJo no Kimyou na Bouken - Ougon no Kaze - 900 [1080p]", releasename.Hints{})
	got, why := MatchEpisodes(p, "aired", eps, show.Parts)
	if len(got) != 0 || !strings.Contains(why, "900") || !strings.Contains(why, "Ougon no Kaze") {
		t.Fatalf("got %+v, why %q", got, why)
	}
}

// With no part names the running number counts across the show, as before.
func TestNoPartNamesKeepsTheRunningNumber(t *testing.T) {
	show, eps := jojo()
	p := releasename.Parse("[Moozzi2] JoJo no Kimyou na Bouken Ougon no Kaze - 20 [BD 1080p]", releasename.Hints{})
	got, _ := MatchEpisodes(p, "aired", eps, nil)
	if len(got) != 1 || got[0].Season != 1 || got[0].Episode != 20 {
		t.Fatalf("got %+v", got)
	}
	show.Parts = nil
	if m := MatchRelease(p, []TitleRef{show}); m.Title == nil || strings.Contains(m.Detail, "part") {
		t.Fatalf("match %+v", m)
	}
}

// A part name is a name of the show even when it is not among its other
// names, and the match says which part the name points at.
func TestPartNameIsANameOfTheShow(t *testing.T) {
	show, _ := jojo()
	show.Aliases = nil
	p := releasename.Parse("[Group] JoJo's Bizarre Adventure - Golden Wind - 03 [1080p]", releasename.Hints{})
	m := MatchRelease(p, []TitleRef{show})
	if m.Title == nil || m.Conf != releasename.Sure {
		t.Fatalf("no match: %s", m.Detail)
	}
	if want := "JoJo's Bizarre Adventure by its part name JoJo's Bizarre Adventure: Golden Wind (season 4)"; m.Detail != want {
		t.Fatalf("detail %q, want %q", m.Detail, want)
	}
	// A season-and-episode Release with a part name finds the show too.
	p = releasename.Parse("JoJo's.Bizarre.Adventure.Golden.Wind.S04E03.1080p.WEB.x264-GRP", releasename.Hints{})
	if m := MatchRelease(p, []TitleRef{show}); m.Title == nil {
		t.Fatalf("season and episode with a part name: %s", m.Detail)
	}
}

// Part names only count for a show in TMDB's own aired order.
func TestPartsFor(t *testing.T) {
	parts := []PartName{{Names: []string{"A Part"}, Season: 2}}
	aired, absolute, group, none := "aired", "absolute", "abc", ""
	for _, c := range []struct {
		t    Title
		want bool
	}{
		{Title{Kind: "show", PartNames: parts}, true},
		{Title{Kind: "show", PartNames: parts, EpisodeOrder: &aired, EpisodeGroupID: &none}, true},
		{Title{Kind: "show", PartNames: parts, EpisodeOrder: &absolute}, false},
		{Title{Kind: "show", PartNames: parts, EpisodeGroupID: &group}, false},
		{Title{Kind: "movie", PartNames: parts}, false},
	} {
		if got := PartsFor(c.t); (len(got) > 0) != c.want {
			t.Errorf("%+v %v %v: got %v", c.t.Kind, c.t.EpisodeOrder, c.t.EpisodeGroupID, got)
		}
	}
}

// A range is read one way for all its numbers: inside the part's season when
// every number fits it, else as the show's running numbers. It never mixes
// the two.
func TestPartNameRangeIsReadOneWay(t *testing.T) {
	show, eps := jojo()
	for _, c := range []struct {
		name        string
		first, last [2]int // season, episode
		count       int
	}{
		{"[Group] JoJo no Kimyou na Bouken - Stardust Crusaders - Egypt-hen - 01-24 [1080p]", [2]int{2, 25}, [2]int{2, 48}, 24},
		{"[Group] JoJo no Kimyou na Bouken - Stardust Crusaders - 01-24 [1080p]", [2]int{2, 1}, [2]int{2, 24}, 24},
		// Past the part's end the numbers go on inside the season.
		{"[Group] JoJo no Kimyou na Bouken - Stardust Crusaders - 23-26 [1080p]", [2]int{2, 23}, [2]int{2, 26}, 4},
		{"[Group] JoJo no Kimyou na Bouken - Stardust Crusaders - 01-48 [1080p]", [2]int{2, 1}, [2]int{2, 48}, 48},
		// 49 is past the season: all four are running numbers.
		{"[Group] JoJo no Kimyou na Bouken - Stardust Crusaders - 47-50 [1080p]", [2]int{2, 21}, [2]int{2, 24}, 4},
	} {
		p := releasename.Parse(c.name, releasename.Hints{})
		got, why := MatchEpisodes(p, "aired", eps, show.Parts)
		if len(got) != c.count {
			t.Errorf("%s: %d episodes (%s), want %d", c.name, len(got), why, c.count)
			continue
		}
		seen := map[int64]bool{}
		for _, e := range got {
			if seen[e.ItemID] {
				t.Errorf("%s: %s twice", c.name, e.Label())
			}
			seen[e.ItemID] = true
		}
		f, l := got[0], got[len(got)-1]
		if [2]int{f.Season, f.Episode} != c.first || [2]int{l.Season, l.Episode} != c.last {
			t.Errorf("%s: from %s to %s", c.name, f.Label(), l.Label())
		}
	}
}

// A year after a part's name is that part's year, not the year the show
// began: the Release still belongs to the show.
func TestPartNameWithItsOwnYear(t *testing.T) {
	show, eps := jojo()
	p := releasename.Parse("[Group] JoJo's Bizarre Adventure Golden Wind (2018) - 03 [1080p]", releasename.Hints{})
	m := MatchRelease(p, []TitleRef{show})
	if m.Title == nil || m.Conf != releasename.Sure {
		t.Fatalf("no match: %s (parsed title %q year %d)", m.Detail, p.Title.Value, p.Year.Value)
	}
	if got, why := MatchEpisodes(p, "aired", eps, show.Parts); len(got) != 1 || got[0].Season != 4 || got[0].Episode != 3 {
		t.Fatalf("got %+v (%s)", got, why)
	}
	// Under the show's own name another year still names another show.
	p = releasename.Parse("JoJo's.Bizarre.Adventure.2018.S01E03.1080p.WEB.x264-GRP", releasename.Hints{})
	if m := MatchRelease(p, []TitleRef{show}); m.Title != nil {
		t.Fatalf("the show's own name with another year matched: %s", m.Detail)
	}
}

// A show in the Library that is called what another show's part is called
// takes the Release: its own name beats a part name.
func TestOwnNameBeatsAPartName(t *testing.T) {
	show, _ := jojo()
	show.Aliases = []string{"JoJo no Kimyou na Bouken"} // the part names are no plain other names here
	other := TitleRef{ID: 9, Name: "JoJo's Bizarre Adventure: Golden Wind", Year: 2018, Kind: "show"}
	p := releasename.Parse("[Group] JoJo's Bizarre Adventure - Golden Wind - 03 [1080p]", releasename.Hints{})
	for _, lib := range [][]TitleRef{{show, other}, {other, show}} {
		m := MatchRelease(p, lib)
		if m.Title == nil || m.Title.ID != 9 {
			t.Fatalf("got %+v", m)
		}
	}
	// Alone, the show with the part still takes it.
	if m := MatchRelease(p, []TitleRef{show}); m.Title == nil || m.Title.ID != 4425 {
		t.Fatalf("alone: %+v", m)
	}
}

// The first series is a part too (season 1, from its start): its name is
// often the show's own romaji name. A small number counts in season 1, and
// a number past season 1 is still the show's running number.
func TestFirstSeriesPartKeepsRunningNumbers(t *testing.T) {
	show, eps := jojo()
	show.Aliases = nil
	show.Parts = append([]PartName{{Names: []string{"JoJo no Kimyou na Bouken (TV)", "JoJo no Kimyou na Bouken"}, Season: 1, Source: PartFromAnimeMap}}, show.Parts...)
	for name, want := range map[string][2]int{
		"[Moozzi2] JoJo no Kimyou na Bouken-05 [BD 1920x1080 x265-10Bit Flac]":     {1, 5},
		"[Moozzi2] JoJo no Kimyou na Bouken-26 END [BD 1920x1080 x265-10Bit Flac]": {1, 26},
		"[Group] JoJo no Kimyou na Bouken - 133 [1080p]":                           {4, 20},
	} {
		p := releasename.Parse(name, releasename.Hints{})
		if m := MatchRelease(p, []TitleRef{show}); m.Title == nil {
			t.Errorf("%s: no show: %s", name, m.Detail)
			continue
		}
		got, why := MatchEpisodes(p, "aired", eps, show.Parts)
		if len(got) != 1 || [2]int{got[0].Season, got[0].Episode} != want {
			t.Errorf("%s: got %+v (%s), want %v", name, got, why, want)
		}
	}
}
