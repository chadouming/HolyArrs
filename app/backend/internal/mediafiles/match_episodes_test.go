package mediafiles

import (
	"slices"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/releasename"
)

func epLabels(rs []EpisodeRef) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Label()
	}
	return out
}

// A TMDB season that holds two parts: release names number the second part
// as season 2, and those names find the stored season 1 episodes.
func TestMatchEpisodesReleaseNumbers(t *testing.T) {
	var eps []EpisodeRef
	for e := 1; e <= 15; e++ {
		eps = append(eps, EpisodeRef{ItemID: int64(e), Season: 0, Episode: e})
	}
	for e := 1; e <= 25; e++ {
		r := EpisodeRef{ItemID: int64(100 + e), Season: 1, Episode: e, Absolute: e}
		if e > 12 {
			r.AltSeason, r.AltEpisode = 2, e-12
		}
		eps = append(eps, r)
	}
	run := func(from, to int) []string {
		var out []string
		for e := from; e <= to; e++ {
			out = append(out, EpisodeRef{Season: 1, Episode: e}.Label())
		}
		return out
	}
	for _, c := range []struct {
		name string
		want []string
		why  string
	}{
		{"Mashle S02E05 1080p WEB-DL", []string{"S01E17"}, ""},
		{"[SubsPlease] Mashle S2 - 05 (1080p)", []string{"S01E17"}, ""},
		{"[Erai-raws] Mashle 2nd Season - 05 [1080p]", []string{"S01E17"}, ""},
		{"Mashle S01E05 1080p WEB-DL", []string{"S01E05"}, ""},
		{"Mashle S01E17 1080p WEB-DL", []string{"S01E17"}, ""},
		{"Mashle S01 1080p WEB-DL", run(1, 12), ""},
		{"Mashle S02 1080p WEB-DL", run(13, 25), ""},
		{"Mashle S01-S02 1080p WEB-DL", run(1, 25), ""},
		{"[SubsPlease] Mashle - 17 (1080p)", []string{"S01E17"}, ""},
		{"Mashle S03E01 1080p WEB-DL", nil, "the show has no episode S03E01"},
	} {
		got, why := MatchEpisodes(releasename.Parse(c.name, releasename.Hints{Show: true}), "absolute", eps, nil)
		if !slices.Equal(epLabels(got), c.want) || why != c.why {
			t.Errorf("%s: %v %q, want %v %q", c.name, epLabels(got), why, c.want, c.why)
		}
	}
}

func TestMatchEpisodesSeasonalNumbering(t *testing.T) {
	// Resetting metadata: S01E01..E52 (Abs 1..52), S02E01..E52 (Abs 53..104).
	var resetting []EpisodeRef
	for e := 1; e <= 52; e++ {
		resetting = append(resetting, EpisodeRef{ItemID: int64(100 + e), Season: 1, Episode: e, Absolute: e})
	}
	for e := 1; e <= 52; e++ {
		resetting = append(resetting, EpisodeRef{ItemID: int64(200 + e), Season: 2, Episode: e, Absolute: 52 + e})
	}

	// Resetting via AltSeason: TMDB S01E01..E104 split into AltSeason 2 (1..52).
	var altResetting []EpisodeRef
	for e := 1; e <= 104; e++ {
		r := EpisodeRef{ItemID: int64(e), Season: 1, Episode: e, Absolute: e}
		if e > 52 {
			r.AltSeason, r.AltEpisode = 2, e-52
		}
		altResetting = append(altResetting, r)
	}

	// Continuous metadata: S01E01..E52, S02E53..E104.
	var continuous []EpisodeRef
	for e := 1; e <= 52; e++ {
		continuous = append(continuous, EpisodeRef{ItemID: int64(100 + e), Season: 1, Episode: e, Absolute: e})
	}
	for e := 53; e <= 104; e++ {
		continuous = append(continuous, EpisodeRef{ItemID: int64(200 + e), Season: 2, Episode: e, Absolute: e})
	}

	// Continuous metadata without Absolute populated (relies on prevMax + 1).
	continuousNoAbs := slices.Clone(continuous)
	for i := range continuousNoAbs {
		continuousNoAbs[i].Absolute = 0
	}

	// Continuous via AltSeason: TMDB S01E01..E104 with AltSeason 2 keeping 53..104.
	var altContinuous []EpisodeRef
	for e := 1; e <= 104; e++ {
		r := EpisodeRef{ItemID: int64(e), Season: 1, Episode: e, Absolute: e}
		if e > 52 {
			r.AltSeason, r.AltEpisode = 2, e
		}
		altContinuous = append(altContinuous, r)
	}

	// Missing E01 in S01 (even with Absolute starting at 2).
	var missingS1E1 []EpisodeRef
	for e := 2; e <= 10; e++ {
		missingS1E1 = append(missingS1E1, EpisodeRef{ItemID: int64(e), Season: 1, Episode: e, Absolute: e})
	}

	// Missing E01 in S02 (S01 ended at 12, S02 starts at E02 with Abs 14).
	var missingS2E1 []EpisodeRef
	for e := 1; e <= 12; e++ {
		missingS2E1 = append(missingS2E1, EpisodeRef{ItemID: int64(100 + e), Season: 1, Episode: e, Absolute: e})
	}
	for e := 2; e <= 12; e++ {
		missingS2E1 = append(missingS2E1, EpisodeRef{ItemID: int64(200 + e), Season: 2, Episode: e, Absolute: 12 + e - 1})
	}

	// Precedence fixture: S01 has 4 eps, S02 has E01..E10 (Abs 5..14), so S02E05
	// matches direct S02E05 (Abs 9), not S02E01 (Abs 5).
	var precedence []EpisodeRef
	for e := 1; e <= 4; e++ {
		precedence = append(precedence, EpisodeRef{ItemID: int64(100 + e), Season: 1, Episode: e, Absolute: e})
	}
	for e := 1; e <= 10; e++ {
		precedence = append(precedence, EpisodeRef{ItemID: int64(200 + e), Season: 2, Episode: e, Absolute: 4 + e})
	}

	for _, c := range []struct {
		name string
		rel  string
		eps  []EpisodeRef
		want []string
		why  string
	}{
		{"dir1 single", "Naruto.S02E53.1080p.WEB-DL", resetting, []string{"S02E01"}, ""},
		{"dir1 multi", "Naruto.S02E53-E54.1080p.WEB-DL", resetting, []string{"S02E01", "S02E02"}, ""},
		{"dir1 alt single", "Naruto.S02E53.1080p.WEB-DL", altResetting, []string{"S01E53"}, ""},
		{"dir1 alt multi", "Naruto.S02E53-E54.1080p.WEB-DL", altResetting, []string{"S01E53", "S01E54"}, ""},
		{"dir2 single", "Naruto.S02E01.1080p.WEB-DL", continuous, []string{"S02E53"}, ""},
		{"dir2 multi", "Naruto.S02E01-E02.1080p.WEB-DL", continuous, []string{"S02E53", "S02E54"}, ""},
		{"dir2 direct", "Naruto.S02E53.1080p.WEB-DL", continuous, []string{"S02E53"}, ""},
		{"dir2 no absolute", "Naruto.S02E01.1080p.WEB-DL", continuousNoAbs, []string{"S02E53"}, ""},
		{"dir2 alt single", "Naruto.S02E01.1080p.WEB-DL", altContinuous, []string{"S01E53"}, ""},
		{"cross season rejected", "Naruto.S03E53.1080p.WEB-DL", resetting, nil, "the show has no episode S03E53"},
		{"missing s01e01 rejected", "Show.S01E01.1080p.WEB-DL", missingS1E1, nil, "the show has no episode S01E01"},
		{"missing s02e01 rejected", "Show.S02E01.1080p.WEB-DL", missingS2E1, nil, "the show has no episode S02E01"},
		{"mixed boundary rejected", "Naruto.S02E52-E53.1080p.WEB-DL", resetting, nil, "the show has no episode S02E53"},
		{"special rejected", "Naruto.S00E53.1080p.WEB-DL", resetting, nil, "the show has no episode S00E53"},
		{"direct beats fallback", "Show.S02E05.1080p.WEB-DL", precedence, []string{"S02E05"}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, why := MatchEpisodes(releasename.Parse(c.rel, releasename.Hints{Show: true}), "aired", c.eps, nil)
			if !slices.Equal(epLabels(got), c.want) || why != c.why {
				t.Errorf("%s: got %v %q, want %v %q", c.rel, epLabels(got), why, c.want, c.why)
			}
		})
	}
}
