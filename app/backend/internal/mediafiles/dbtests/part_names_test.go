package dbtests

import (
	"reflect"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

// A show keeps its part names: the names that point at one season, with
// the episode the part starts after and where the names came from.
func TestTitlePartNamesAreStored(t *testing.T) {
	s, _ := openTest(t, database.Options{})
	parts := []mediafiles.PartName{
		{Names: []string{"JoJo no Kimyou na Bouken: Ougon no Kaze", "JoJo's Bizarre Adventure: Golden Wind"}, Season: 4, Offset: 0, Source: mediafiles.PartFromAnimeMap},
		{Names: []string{"Quiet Harbour The Long Winter"}, Season: 2, Offset: 12, Source: mediafiles.PartFromSeasonName},
	}
	ti := &mediafiles.Title{Kind: "show", GateDriver: "none", Name: "JoJo's Bizarre Adventure", SortName: "jojo's bizarre adventure",
		MetadataSource: "tmdb", NewPartsPolicy: "all", ReleaseGate: "released", WhyAdded: "by_hand", Status: "running", State: "active", PartNames: parts}
	got := &mediafiles.Title{}
	roundTrip(t, s, "titles", ti, got)
	if !reflect.DeepEqual(got.PartNames, parts) {
		t.Fatalf("got %+v", got.PartNames)
	}
	none := &mediafiles.Title{Kind: "show", GateDriver: "none", Name: "No Parts", SortName: "no parts",
		MetadataSource: "tmdb", NewPartsPolicy: "all", ReleaseGate: "released", WhyAdded: "by_hand", Status: "running", State: "active"}
	got = &mediafiles.Title{}
	roundTrip(t, s, "titles", none, got)
	if len(got.PartNames) != 0 {
		t.Fatalf("a show with none got %+v", got.PartNames)
	}
}
