package releasename

import (
	"slices"
	"testing"
)

// Some anime groups write the running number right after the title, with no
// space around the dash: "[Moozzi2] ... Ougon no Kaze-20 [BD ...]". It is the
// episode, and the [group] in front is the group, not part of the title.
func TestAnimeNumberRightAfterTheTitle(t *testing.T) {
	for _, c := range []struct {
		name, title, group string
		abs                []int
	}{
		{"[Moozzi2] JoJo no Kimyou na Bouken Ougon no Kaze-20 [BD 1920x1080 x265-10Bit 2Audio]", "JoJo no Kimyou na Bouken Ougon no Kaze", "Moozzi2", []int{20}},
		{"[Moozzi2] JoJo no Kimyou na Bouken Ougon no Kaze-20 (BD 1920x1080 x265-10Bit 2Audio)", "JoJo no Kimyou na Bouken Ougon no Kaze", "Moozzi2", []int{20}},
		{"[Moozzi2] Ougon no Kaze-05v2 [BD 1080p]", "Ougon no Kaze", "Moozzi2", []int{5}},
		// The last episode of a cour is marked END, and a second number in
		// brackets counts across the season.
		{"[Moozzi2] JoJo no Kimyou na Bouken Ougon no Kaze-39 END [BD 1920x1080 x265-10Bit 2Audio]", "JoJo no Kimyou na Bouken Ougon no Kaze", "Moozzi2", []int{39}},
		{"[Moozzi2] JoJo no Kimyou na Bouken Stardust Crusaders Egypt Hen-24 END [ 48 ] [BD 1920x1080 x265-10Bit 2Audio]", "JoJo no Kimyou na Bouken Stardust Crusaders Egypt Hen", "Moozzi2", []int{24}},
		{"[Moozzi2] JoJo no Kimyou na Bouken Stardust Crusaders Egypt Hen-23 [ 47 ] [BD 1920x1080 x265-10Bit 2Audio]", "JoJo no Kimyou na Bouken Stardust Crusaders Egypt Hen", "Moozzi2", []int{23}},
		{"[Moozzi2] JoJo no Kimyou na Bouken-26 END [BD 1920x1080 x265-10Bit Flac]", "JoJo no Kimyou na Bouken", "Moozzi2", []int{26}},
		// The spaced form reads as before.
		{"[Moozzi2] JoJo no Kimyou na Bouken Ougon no Kaze - 20 [BD 1920x1080 x265-10Bit 2Audio]", "JoJo no Kimyou na Bouken Ougon no Kaze", "Moozzi2", []int{20}},
	} {
		r := Parse(c.name, Hints{})
		if r.Title.Value != c.title || r.Group.Value != c.group || r.Show == nil || !slices.Equal(r.Show.Absolute, c.abs) || r.Show.Conf != Sure {
			t.Errorf("%s:\n title %q group %q show %+v", c.name, r.Title.Value, r.Group.Value, r.Show)
		}
		if r.Resolution.Value != 1080 {
			t.Errorf("%s: resolution %d", c.name, r.Resolution.Value)
		}
	}
}

// A number that belongs to the title stays there: a one-word title before
// the dash ("R-15"), a year, a single digit, or a name with no [group].
func TestTightDashNumberThatIsNotAnEpisode(t *testing.T) {
	for name, title := range map[string]string{
		"[Group] R-15 [BD 1080p]":               "R-15",
		"[Group] Catch-22 [BD 1080p]":           "Catch-22",
		"[Group] Some Show-2019 [BD 1080p]":     "Some Show-2019",
		"[Group] Kimi to Boku-2 [BD 1080p]":     "Kimi to Boku-2",
		"Some Movie Area-51 2019 1080p WEB-GRP": "Some Movie Area-51",
	} {
		r := Parse(name, Hints{})
		if r.Show != nil || r.Title.Value != title {
			t.Errorf("%s: title %q show %+v", name, r.Title.Value, r.Show)
		}
	}
}

// A [group] in front of a name with no episode number (an anime movie, a
// whole show) is still the group.
func TestLeadingGroupWithoutEpisodeNumber(t *testing.T) {
	for _, c := range []struct{ name, title, group string }{
		{"[Moozzi2] Kimi no Na wa [BD 1920x1080 x265-10Bit 2Audio]", "Kimi no Na wa", "Moozzi2"},
		{"[Lab Fansub] Quiet Harbour 2019 [BD 1080p]", "Quiet Harbour", "Lab Fansub"},
		// A group at the end wins, and tags in front are no group.
		{"[Site] Quiet.Harbour.2019.1080p.WEB.x264-GRP", "Quiet Harbour", "GRP"},
	} {
		r := Parse(c.name, Hints{})
		if r.Title.Value != c.title || r.Group.Value != c.group || r.Show != nil {
			t.Errorf("%s: title %q group %q show %+v", c.name, r.Title.Value, r.Group.Value, r.Show)
		}
	}
	if r := Parse("[1080p] Quiet Harbour 2019 WEB x264", Hints{}); r.Group.Value != "" {
		t.Errorf("tags in front: group %q", r.Group.Value)
	}
}

// Season packs and batches keep reading as before: a range of episodes, or
// a number right after a season mark, is no running number.
func TestTightDashLeavesSeasonPacksAlone(t *testing.T) {
	for _, name := range []string{
		"[Group] Show Title Season 2 01-12 [BD 1080p]",
		"[Group] Show Title S2 01-12 [BD 1080p]",
		"[Group] Show Title Season 1-12 [BD]",
		"[Group] Show Title S2-12 [1080p]",
	} {
		r := Parse(name, Hints{})
		if r.Title.Value != "Show Title" || r.Show == nil || len(r.Show.Absolute) != 0 {
			t.Errorf("%s: title %q show %+v", name, r.Title.Value, r.Show)
		}
	}
}

// A number that is part of the title, or follows a word that numbers
// something else than episodes, is no running number either.
func TestTightDashNumberAfterAWordThatIsNotATitle(t *testing.T) {
	for _, name := range []string{
		"[Group] Show Title 2nd Season 01-13 [BD]", // a season pack, as before
		"[Group] Space Battleship Yamato-2199 [BD 1080p]",
		"[Group] Mobile Suit Gundam-0083 [BD]",
		"[Group] Detective Conan Movie-25 [BD]",
		"[Group] Show Title OVA-02 [BD]",
		"[Group] Show Title NCOP-01 [BD]",
		"[Group] Show Title Part-02 [BD]",
		"[Group] Show Title EP-12 [BD]",
		"[Group] Show Title Vol-03 [BD]",
	} {
		if r := Parse(name, Hints{}); r.Show != nil && len(r.Show.Absolute) > 0 {
			t.Errorf("%s: read as running number %v of %q", name, r.Show.Absolute, r.Title.Value)
		}
	}
}
