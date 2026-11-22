package metadata

import (
	"reflect"
	"testing"
)

// The anime map lists a show's parts: each AniList title that sits in a
// regular TMDB season, with that season, the episode it starts after and
// its names.
func TestAnimeMapPartNames(t *testing.T) {
	n := func(i int) *int { return &i }
	m := AnimeMap{
		Entries: []AnimeEntry{
			{AniList: 5, TMDBTV: 45790, TMDBSeason: n(4)},
			{AniList: 2, TMDBTV: 45790, TMDBSeason: n(1), TMDBOffset: 26},
			{AniList: 1, TMDBTV: 45790, TMDBSeason: n(1)},
			{AniList: 6, TMDBTV: 45790, TMDBSeason: n(2), Absolute: true}, // whole-show numbering: no part
			{AniList: 7, TMDBTV: 45790},                                   // no season
			{AniList: 8, TMDBTV: 45790, TMDBSeason: n(0)},                 // specials
			{AniList: 9, TMDBTV: 45790, TMDBSeason: n(3)},                 // no title known
			{AniList: 10, TMDBTV: 999, TMDBSeason: n(1)},                  // another show
		},
		Titles: []AnimeTitle{
			{AniList: 1, Romaji: "JoJo no Kimyou na Bouken (TV)", English: "JoJo's Bizarre Adventure (TV)"},
			{AniList: 2, Romaji: "JoJo no Kimyou na Bouken: Sentou Chouryuu", English: " "},
			{AniList: 5, Romaji: "JoJo no Kimyou na Bouken: Ougon no Kaze", English: "JoJo's Bizarre Adventure: Golden Wind", Native: "ジョジョの奇妙な冒険 黄金の風"},
			{AniList: 6, Romaji: "Absolute One"},
			{AniList: 7, Romaji: "No Season"},
			{AniList: 8, Romaji: "A Special"},
			{AniList: 10, Romaji: "Another Show"},
		},
	}
	want := []AnimePart{
		// AniList marks the first series "(TV)"; Releases leave the mark out.
		{Names: []string{"JoJo no Kimyou na Bouken (TV)", "JoJo's Bizarre Adventure (TV)", "JoJo no Kimyou na Bouken", "JoJo's Bizarre Adventure"}, Season: 1, Offset: 0},
		{Names: []string{"JoJo no Kimyou na Bouken: Sentou Chouryuu"}, Season: 1, Offset: 26},
		{Names: []string{"JoJo no Kimyou na Bouken: Ougon no Kaze", "JoJo's Bizarre Adventure: Golden Wind", "ジョジョの奇妙な冒険 黄金の風"}, Season: 4, Offset: 0},
	}
	if got := m.PartNames("45790", map[int]bool{1: true, 2: true, 3: true, 4: true}); !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	if got := (AnimeMap{}).PartNames("45790", nil); len(got) != 0 {
		t.Fatalf("an empty map gave %+v", got)
	}
}

// A map that names a season the show no longer has is out of date: its
// parts are not trusted, like its release numbers.
func TestAnimeMapPartNamesOutOfDate(t *testing.T) {
	n := func(i int) *int { return &i }
	m := AnimeMap{
		Entries: []AnimeEntry{{AniList: 1, TMDBTV: 7, TMDBSeason: n(1)}, {AniList: 2, TMDBTV: 7, TMDBSeason: n(5)}, {AniList: 3, TMDBTV: 8, TMDBSeason: n(9)}},
		Titles:  []AnimeTitle{{AniList: 1, Romaji: "First"}, {AniList: 2, Romaji: "Fifth"}, {AniList: 3, Romaji: "Other Show"}},
	}
	if got := m.PartNames("7", map[int]bool{1: true, 2: true}); len(got) != 0 {
		t.Fatalf("season 5 is gone, yet got %+v", got)
	}
	// Another show's seasons do not count, and with season 5 there the parts are read.
	if got := m.PartNames("7", map[int]bool{1: true, 5: true}); len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
}

// The names in the map that belong to no part of the show (a spin-off, the
// specials, another show's entry) are its side names: they are other names
// to find the show by, but no name of the whole show.
func TestAnimeMapSideNames(t *testing.T) {
	n := func(i int) *int { return &i }
	m := AnimeMap{
		Entries: []AnimeEntry{
			{AniList: 1, TMDBTV: 7, TMDBSeason: n(1)},
			{AniList: 2, TMDBTV: 7, TMDBSeason: n(0)},                 // specials
			{AniList: 3, TMDBTV: 7},                                   // no season
			{AniList: 4, TMDBTV: 7, TMDBSeason: n(1), Absolute: true}, // the whole show
			{AniList: 5, TMDBTV: 8, TMDBSeason: n(1)},                 // another show
		},
		Titles: []AnimeTitle{{AniList: 1, Romaji: "Part One"}, {AniList: 2, Romaji: "Kishibe Rohan wa Ugokanai", English: "Thus Spoke Rohan Kishibe"},
			{AniList: 3, Romaji: "A Movie"}, {AniList: 4, Romaji: "Whole Show"}, {AniList: 5, Romaji: "Other Show"}, {AniList: 6, Romaji: "No Entry"}},
	}
	want := []string{"Kishibe Rohan wa Ugokanai", "Thus Spoke Rohan Kishibe", "A Movie", "Other Show", "No Entry"}
	if got := m.SideNames("7"); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
}
