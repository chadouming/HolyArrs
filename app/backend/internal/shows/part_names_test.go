package shows

import (
	"reflect"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
	"github.com/HolyArrs/holyarrs/app/backend/internal/metadata"
)

// The anime map's parts become part names, in the same order.
func TestPartNamesFromAnimeMap(t *testing.T) {
	sh := metadata.Show{Name: "JoJo's Bizarre Adventure", AnimeParts: []metadata.AnimePart{
		{Names: []string{"JoJo no Kimyou na Bouken (TV)"}, Season: 1},
		{Names: []string{"JoJo no Kimyou na Bouken: Ougon no Kaze", "JoJo's Bizarre Adventure: Golden Wind"}, Season: 4},
	}}
	want := []mediafiles.PartName{
		{Names: []string{"JoJo no Kimyou na Bouken (TV)"}, Season: 1, Source: mediafiles.PartFromAnimeMap},
		{Names: []string{"JoJo no Kimyou na Bouken: Ougon no Kaze", "JoJo's Bizarre Adventure: Golden Wind"}, Season: 4, Source: mediafiles.PartFromAnimeMap},
	}
	if got := partNames(sh.Name, nil, sh, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

// A season with a name of its own is a part: the show's names followed by
// the season's name. Plain names and the show's own name give none.
func TestPartNamesFromSeasonNames(t *testing.T) {
	sh := metadata.Show{Name: "Quiet Harbour", Seasons: []metadata.Season{
		{Number: 0, Name: "Specials"}, {Number: 1, Name: "Season 1"}, {Number: 2, Name: "The Long Winter"}, {Number: 3, Name: "Quiet Harbour"},
	}}
	want := []mediafiles.PartName{
		{Names: []string{"Quiet Harbour The Long Winter", "Stille Haven The Long Winter"}, Season: 2, Source: mediafiles.PartFromSeasonName},
	}
	if got := partNames(sh.Name, []string{"Stille Haven"}, sh, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	// At most 8 names for one season.
	var many []string
	for _, a := range []string{"A1 Name", "B2 Name", "C3 Name", "D4 Name", "E5 Name", "F6 Name", "G7 Name", "H8 Name", "I9 Name"} {
		many = append(many, a)
	}
	if got := partNames(sh.Name, many, sh, nil); len(got) != 1 || len(got[0].Names) != 8 {
		t.Fatalf("got %+v", got)
	}
	if got := partNames("Plain", nil, metadata.Show{Name: "Plain", Seasons: []metadata.Season{{Number: 1, Name: "Season 1"}}}, nil); len(got) != 0 {
		t.Fatalf("plain seasons gave %+v", got)
	}
}

// The anime map wins: a season-name entry that repeats one of its names is
// dropped, and an alias that is itself a part name is not combined with a
// season's name.
func TestPartNamesSeasonNameYieldsToAnimeMap(t *testing.T) {
	sh := metadata.Show{Name: "JoJo's Bizarre Adventure",
		AnimeParts: []metadata.AnimePart{{Names: []string{"JoJo no Kimyou na Bouken: Ougon no Kaze", "JoJo's Bizarre Adventure: Golden Wind"}, Season: 4}},
		Seasons:    []metadata.Season{{Number: 3, Name: "Diamond Is Unbreakable"}, {Number: 4, Name: "Golden Wind"}},
	}
	aliases := []string{"JoJo no Kimyou na Bouken: Ougon no Kaze", "JoJo no Kimyou na Bouken"}
	want := []mediafiles.PartName{
		{Names: []string{"JoJo no Kimyou na Bouken: Ougon no Kaze", "JoJo's Bizarre Adventure: Golden Wind"}, Season: 4, Source: mediafiles.PartFromAnimeMap},
		{Names: []string{"JoJo's Bizarre Adventure Diamond Is Unbreakable", "JoJo no Kimyou na Bouken Diamond Is Unbreakable"}, Season: 3, Source: mediafiles.PartFromSeasonName},
	}
	if got := partNames(sh.Name, aliases, sh, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

// When the anime map could not be read this time, the parts it gave before
// are kept; season names are still read fresh.
func TestPartNamesKeepAnimeMapPartsWhenStale(t *testing.T) {
	prev := []mediafiles.PartName{
		{Names: []string{"Old Part"}, Season: 2, Offset: 12, Source: mediafiles.PartFromAnimeMap},
		{Names: []string{"Show Old Season Name"}, Season: 1, Source: mediafiles.PartFromSeasonName},
	}
	sh := metadata.Show{Name: "Show", AltStale: true, Seasons: []metadata.Season{{Number: 1, Name: "New Name"}}}
	want := []mediafiles.PartName{
		{Names: []string{"Old Part"}, Season: 2, Offset: 12, Source: mediafiles.PartFromAnimeMap},
		{Names: []string{"Show New Name"}, Season: 1, Source: mediafiles.PartFromSeasonName},
	}
	if got := partNames("Show", nil, sh, prev); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	sh.AltStale = false
	if got := partNames("Show", nil, sh, prev); len(got) != 1 || got[0].Source != mediafiles.PartFromSeasonName {
		t.Fatalf("a fresh read with no anime parts kept old ones: %+v", got)
	}
}

func TestPlainSeasonName(t *testing.T) {
	for name, want := range map[string]bool{
		"Season 2": true, "season 02": true, "Series 3": true, "Specials": true, "Book 2": true, "Part 2": true, "Volume 1": true,
		"2": true, "": true, " Season 4 ": true, "Staffel 2": true, "Saison 1": true, "Temporada 3": true,
		"Season Two": true, "Season Twelve": true, "1. Staffel": true, "2ª Temporada": true, "2nd Season": true, "Second Season": true,
		"Сезон 2": true, "シーズン2": true, "Sezon 2": true, "Miniseries": true, "Mini-Series": true, "Limited Series": true, "Season 1 Part 2": true,
		"Two Rivers": false, "Second Chances": false, "Series of Unfortunate Events": false,
		"Golden Wind": false, "The Long Winter": false, "Book of Boba": false, "Part of Your World": false,
	} {
		if got := plainSeasonName(name); got != want {
			t.Errorf("%q: %v, want %v", name, got, want)
		}
	}
}

// Adding a show and refreshing it both go through applyShow, which keeps
// the part names next to the other names.
func TestApplyShowSetsPartNames(t *testing.T) {
	ti := mediafiles.Title{Kind: "show", Aliases: []string{"Stille Haven"}}
	applyShow(&ti, metadata.Show{Name: "Quiet Harbour", Seasons: []metadata.Season{{Number: 2, Name: "The Long Winter"}}})
	want := []mediafiles.PartName{{Names: []string{"Quiet Harbour The Long Winter", "Stille Haven The Long Winter"}, Season: 2, Source: mediafiles.PartFromSeasonName}}
	if !reflect.DeepEqual(ti.PartNames, want) {
		t.Fatalf("got %+v", ti.PartNames)
	}
}

// A part that carries the show's own name but sits past the show's start is
// odd map data: the name is left out, or every Release of the show under its
// main name would count inside that part.
func TestPartNamesDropTheShowsOwnNameFromALaterPart(t *testing.T) {
	sh := metadata.Show{Name: "Quiet Harbour", AnimeParts: []metadata.AnimePart{
		{Names: []string{"Quiet Harbour", "Shizuka na Minato"}, Season: 1},
		{Names: []string{"Quiet Harbour", "Shizuka na Minato 3"}, Season: 3},
		{Names: []string{"Quiet Harbour"}, Season: 2, Offset: 12},
	}}
	want := []mediafiles.PartName{
		{Names: []string{"Quiet Harbour", "Shizuka na Minato"}, Season: 1, Source: mediafiles.PartFromAnimeMap},
		{Names: []string{"Shizuka na Minato 3"}, Season: 3, Source: mediafiles.PartFromAnimeMap},
	}
	if got := partNames(sh.Name, nil, sh, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

// A season whose name already begins with one of the show's names is used
// as it is: "Sword Art Online: Alicization", not the show's name twice.
func TestPartNamesSeasonNameThatHoldsTheShowsName(t *testing.T) {
	sh := metadata.Show{Name: "Sword Art Online", Seasons: []metadata.Season{{Number: 3, Name: "Sword Art Online: Alicization"}, {Number: 4, Name: "SAO Alicization War of Underworld"}}}
	want := []mediafiles.PartName{
		{Names: []string{"Sword Art Online: Alicization"}, Season: 3, Source: mediafiles.PartFromSeasonName},
		{Names: []string{"SAO Alicization War of Underworld"}, Season: 4, Source: mediafiles.PartFromSeasonName},
	}
	if got := partNames(sh.Name, []string{"SAO"}, sh, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

// Another name of the show that already names one season ("<show>: Stardust
// Crusaders") is not put in front of other seasons' names.
func TestPartNamesLeaveSeasonSpecificNamesOut(t *testing.T) {
	sh := metadata.Show{Name: "JoJo's Bizarre Adventure", Seasons: []metadata.Season{{Number: 2, Name: "Stardust Crusaders"}, {Number: 4, Name: "Golden Wind"}}}
	aliases := []string{"JoJo's Bizarre Adventure: Stardust Crusaders", "JoJo no Kimyou na Bouken", "JJBA Golden Wind"}
	want := []mediafiles.PartName{
		{Names: []string{"JoJo's Bizarre Adventure Stardust Crusaders", "JoJo no Kimyou na Bouken Stardust Crusaders"}, Season: 2, Source: mediafiles.PartFromSeasonName},
		{Names: []string{"JoJo's Bizarre Adventure Golden Wind", "JoJo no Kimyou na Bouken Golden Wind", "JJBA Golden Wind"}, Season: 4, Source: mediafiles.PartFromSeasonName},
	}
	if got := partNames(sh.Name, aliases, sh, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

// A season's name is put after the show's own names only: not after the
// name of a spin-off or special the anime map lists with the show, and not
// after a name written in Latin letters when the season's name is not, or the
// other way round.
func TestPartNamesSeasonNamesTakeOnlyTheShowsOwnNames(t *testing.T) {
	sh := metadata.Show{Name: "JoJo's Bizarre Adventure",
		AnimeSideNames: []string{"Kishibe Rohan wa Ugokanai", "Thus Spoke Rohan Kishibe", "岸辺露伴は動かない"},
		Seasons:        []metadata.Season{{Number: 6, Name: "STEEL BALL RUN"}, {Number: 7, Name: "黄金の風"}},
	}
	aliases := []string{"Kishibe Rohan wa Ugokanai", "Thus Spoke Rohan Kishibe", "岸辺露伴は動かない", "JoJo no Kimyou na Bouken",
		"ジョジョの奇妙な冒険", "Чудноватото приключение на Джоджо", "乔乔的奇妙冒险", "JOJO的奇妙冒险 石之海篇"}
	want := []mediafiles.PartName{
		{Names: []string{"JoJo's Bizarre Adventure STEEL BALL RUN", "JoJo no Kimyou na Bouken STEEL BALL RUN"}, Season: 6, Source: mediafiles.PartFromSeasonName},
		// A name that mixes Latin letters with another script counts as the other script.
		{Names: []string{"ジョジョの奇妙な冒険 黄金の風", "Чудноватото приключение на Джоджо 黄金の風", "乔乔的奇妙冒险 黄金の風", "JOJO的奇妙冒险 石之海篇 黄金の風"}, Season: 7, Source: mediafiles.PartFromSeasonName},
	}
	if got := partNames(sh.Name, aliases, sh, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}
