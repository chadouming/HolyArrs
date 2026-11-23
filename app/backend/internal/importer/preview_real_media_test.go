package importer

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/releasename"
	"github.com/HolyArrs/holyarrs/app/backend/internal/mediafiles"
)

func testEBMLSize(size int) []byte {
	switch {
	case size < 0x7f:
		return []byte{0x80 | byte(size)}
	case size < 0x3fff:
		return []byte{0x40 | byte(size>>8), byte(size)}
	case size < 0x1fffff:
		return []byte{0x20 | byte(size>>16), byte(size >> 8), byte(size)}
	default:
		return []byte{0x10 | byte(size>>24), byte(size >> 16), byte(size >> 8), byte(size)}
	}
}

func testMKVID(id uint32) []byte {
	switch {
	case id > 0xffffff:
		return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	case id > 0xffff:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	case id > 0xff:
		return []byte{byte(id >> 8), byte(id)}
	}
	return []byte{byte(id)}
}

func testMKVEl(id uint32, parts ...[]byte) []byte {
	payload := bytes.Join(parts, nil)
	rawID := testMKVID(id)
	size := testEBMLSize(len(payload))
	out := make([]byte, 0, len(rawID)+len(size)+len(payload))
	out = append(out, rawID...)
	out = append(out, size...)
	out = append(out, payload...)
	return out
}

func testMKVUint(id uint32, v uint64) []byte {
	b := binary.BigEndian.AppendUint64(nil, v)
	for len(b) > 1 && b[0] == 0 {
		b = b[1:]
	}
	return testMKVEl(id, b)
}

func writeTestMKV(t *testing.T, path string, durationSecs, minSize int) int64 {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	header := testMKVEl(0x1a45dfa3, testMKVEl(0x4282, []byte("matroska")))
	info := testMKVEl(0x1549a966,
		testMKVUint(0x2ad7b1, 1_000_000),
		testMKVEl(0x4489, binary.BigEndian.AppendUint64(nil, math.Float64bits(float64(durationSecs*1000)))),
	)
	video := testMKVEl(0xae,
		testMKVUint(0xd7, 1),
		testMKVUint(0x83, 1),
		testMKVEl(0x86, []byte("V_MPEG4/ISO/AVC")),
		testMKVEl(0xe0, testMKVUint(0xb0, 1920), testMKVUint(0xba, 1080)),
	)
	tracks := testMKVEl(0x1654ae6b, video)
	segParts := [][]byte{info, tracks}
	baseLen := len(header) + len(testMKVEl(0x18538067, segParts...))
	if minSize > baseLen+16 {
		segParts = append(segParts, testMKVEl(0xec, make([]byte, minSize-baseLen-8)))
	}
	data := append(header, testMKVEl(0x18538067, segParts...)...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return int64(len(data))
}

func tokyoGhoulSetup(t *testing.T, relFolder string, files map[string]struct {
	secs int
	size int
}) showWorld {
	t.Helper()
	s, err := database.Open(t.TempDir(), database.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	setFake(t, s, "import_size_floors", "true")
	setFake(t, s, "import_min_video_mb", "1")
	w := showWorld{s: s, dl: mediaTemp(t), lib: mediaTemp(t), slots: map[int]int64{}, folder: "Tokyo Ghoul (2014)"}
	var seen []domain.SeenFile
	for relPath, spec := range files {
		full := filepath.Join(w.dl, relFolder, filepath.FromSlash(relPath))
		sz := writeTestMKV(t, full, spec.secs, spec.size)
		seen = append(seen, domain.SeenFile{Name: relFolder + "/" + relPath, Size: sz})
	}
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		var pr int64
		_ = tx.QueryRow("SELECT id FROM pick_rules LIMIT 1").Scan(&pr)
		lf := &mediafiles.LibraryFolder{Path: w.lib, Kind: "show", Label: "Shows", Health: "ok", SameDiskAs: []int64{}}
		if _, err := database.Insert(tx, "library_folders", lf, domain.System); err != nil {
			return err
		}
		order := "aired"
		ti := &mediafiles.Title{Kind: "show", GateDriver: "none", Name: "Tokyo Ghoul", SortName: "tokyo ghoul", Year: ptr(2014), Aliases: []string{},
			SourceIDs: map[string]string{}, MetadataSource: "local", NewPartsPolicy: "all", ReleaseGate: "released", EpisodeOrder: &order,
			MusicTypes: []string{}, WhyAdded: "by_hand", Artwork: []string{}, Status: "ended", State: "active", KeepUpToDate: true}
		if _, err := database.Insert(tx, "titles", ti, domain.System); err != nil {
			return err
		}
		v := &mediafiles.Version{TitleID: ti.ID, Label: "Main", PickRulesID: pr, LibraryFolderID: lf.ID, FolderName: w.folder, IsMain: true, KeepUpToDate: false}
		if _, err := database.Insert(tx, "versions", v, domain.System); err != nil {
			return err
		}
		w.vID = v.ID
		season, num, name, runtime := 1, 9, "Birdcage", 24
		it := &mediafiles.Item{TitleID: ti.ID, Kind: "episode", Numbers: mediafiles.Numbers{Season: &season, Episode: &num}, Name: &name,
			Runtime: &runtime, KeepUpToDate: true, SourceIDs: map[string]string{}}
		if _, err := database.Insert(tx, "items", it, domain.System); err != nil {
			return err
		}
		sl := &mediafiles.Slot{ItemID: it.ID, VersionID: v.ID, State: mediafiles.SlotDownloading}
		if _, err := database.Insert(tx, "slots", sl, domain.System); err != nil {
			return err
		}
		w.slots[9] = sl.ID
		d := &domain.Downloader{Name: "q", Adapter: "qbittorrent", Protocol: "torrent", URL: "http://q", Category: "holyarrs", PathMap: []domain.PathMap{}, AfterImport: "leave", State: "ok"}
		if _, err := database.Insert(tx, "downloaders", d, domain.System); err != nil {
			return err
		}
		src := &domain.Source{Name: "Lab", Protocol: "torrent", Format: "torznab", URL: "http://x", Kinds: []string{}, Categories: map[string][]int{}, State: "enabled", Extra: map[string]string{}}
		if _, err := database.Insert(tx, "sources", src, domain.System); err != nil {
			return err
		}
		ids := []int64{sl.ID}
		r := &domain.Release{SourceID: src.ID, Name: relFolder, LinkRef: "http://x/1.torrent", Protocol: "torrent", Flags: []string{}, Parsed: map[string]any{},
			MatchedSlotIDs: ids, Verdict: map[string]any{}, FoundBy: "auto_search", State: "chosen"}
		if _, err := database.Insert(tx, "releases", r, domain.System); err != nil {
			return err
		}
		w.relID = r.ID
		dl := &domain.Download{DownloaderID: d.ID, ReleaseID: &r.ID, SlotIDs: ids, ExternalID: "tg09", Progress: 1, State: "finished",
			FilesSeen: seen, SavePath: w.dl, StartedBy: domain.System}
		if _, err := database.Insert(tx, "downloads", dl, domain.System); err != nil {
			return err
		}
		w.dlID = dl.ID
		if _, err := tx.Exec("UPDATE slots SET active_download_id = ? WHERE id = ?", dl.ID, sl.ID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// Exact ANiHLS regression: scene-abbreviated real episode beside smaller 60s preview.
func TestANiHLSSceneAbbreviationMatchesAndImportsRealEpisode(t *testing.T) {
	relFolder := "Tokyo Ghoul S01E09 1080p BluRay x264-ANiHLS"
	realName := "anihls-tokgho.s01e09.1080p.bluray.mkv"
	previewName := relFolder + ".mkv"

	ns := mediafiles.NamingDefaults()
	ns.RenameOnImport = true
	tgt := ShowTarget{
		LibraryPath: "/lib",
		FolderName:  "Tokyo Ghoul (2014)",
		Show:        mediafiles.TitleRef{ID: 1, Name: "Tokyo Ghoul", Year: 2014},
		Order:       mediafiles.OrderAired,
		Naming:      &ns,
		ReleaseName: relFolder,
		Episodes: []Episode{
			{Ref: mediafiles.EpisodeRef{ItemID: 9, Season: 1, Episode: 9}, Name: "Birdcage", SlotID: 109, Wanted: true},
		},
	}
	steps := PlanEpisodes([]Candidate{
		{Path: filepath.Join("/dl", relFolder, previewName), Size: 60 << 20},
		{Path: filepath.Join("/dl", relFolder, realName), Size: 1550 << 20},
	}, tgt)
	byBase := map[string]mediafiles.PlanStep{}
	for _, st := range steps {
		byBase[filepath.Base(st.SourceFile)] = st
	}
	if byBase[realName].Method != "hardlink" || !slices.Equal(byBase[realName].TargetSlotIDs, []int64{109}) {
		t.Fatalf("expected real episode to be hardlinked, got %+v", byBase[realName])
	}
	if byBase[previewName].Method != "skip" {
		t.Fatalf("expected smaller preview to be skipped, got %+v", byBase[previewName])
	}

	w := tokyoGhoulSetup(t, relFolder, map[string]struct {
		secs int
		size int
	}{
		realName:    {secs: 24 * 60, size: 3 << 20},
		previewName: {secs: 60, size: 1500 << 10},
	})
	imp, err := (&Importer{Store: w.s}).Import(context.Background(), w.dlID, false, domain.System)
	if err != nil {
		t.Fatal(err)
	}
	if imp.State != "done" {
		t.Fatalf("import state %s, plan %+v, result %+v", imp.State, imp.Plan, imp.Result)
	}
	sl := w.slot(t, 9)
	if sl.State != mediafiles.SlotInLibrary || sl.FileID == nil {
		t.Fatalf("slot not placed: %+v", sl)
	}
	placed := w.file(t, *sl.FileID)
	if placed.ReleaseName == nil || *placed.ReleaseName != strings.TrimSuffix(realName, ".mkv") {
		t.Fatalf("expected real episode %q to be placed, got %+v", realName, placed.ReleaseName)
	}
	if _, err := os.Stat(filepath.Join(w.lib, filepath.FromSlash(placed.RelativePath))); err != nil {
		t.Fatalf("placed file missing in library: %v", err)
	}
}

// Confirmed-sample fallback for shows when the real video has an obfuscated name alongside a 60s preview.
func TestShowConfirmedSampleFallbackImportsObfuscatedRealMedia(t *testing.T) {
	relFolder := "Tokyo Ghoul S01E09 1080p BluRay x264-ANiHLS"
	previewName := relFolder + ".mkv"
	obfuscated := "8f4e2a1b.mkv"

	w := tokyoGhoulSetup(t, relFolder, map[string]struct {
		secs int
		size int
	}{
		previewName: {secs: 60, size: 1500 << 10},
		obfuscated:  {secs: 24 * 60, size: 2500 << 10},
	})
	imp, err := (&Importer{Store: w.s}).Import(context.Background(), w.dlID, false, domain.System)
	if err != nil {
		t.Fatal(err)
	}
	if imp.State != "done" {
		t.Fatalf("import state %s, plan %+v", imp.State, imp.Plan)
	}
	byBase := map[string]mediafiles.PlanStep{}
	for _, st := range imp.Plan {
		byBase[filepath.Base(st.SourceFile)] = st
	}
	if len(imp.Plan) != 2 {
		t.Fatalf("expected no duplicate plan steps, got %d: %+v", len(imp.Plan), imp.Plan)
	}
	if byBase[previewName].Method != "refuse" || !strings.Contains(strings.Join(byBase[previewName].Why, " "), "a sample, not the full video") {
		t.Fatalf("expected preview refused as sample, got %+v", byBase[previewName])
	}
	if byBase[obfuscated].Method != "hardlink" {
		t.Fatalf("expected obfuscated real file promoted to hardlink, got %+v", byBase[obfuscated])
	}
	sl := w.slot(t, 9)
	if sl.State != mediafiles.SlotInLibrary || sl.FileID == nil {
		t.Fatalf("slot not placed: %+v", sl)
	}
	placed := w.file(t, *sl.FileID)
	if placed.ReleaseName == nil || *placed.ReleaseName != "8f4e2a1b" {
		t.Fatalf("unexpected placed file: %+v", placed)
	}
	if _, err := os.Stat(filepath.Join(w.lib, filepath.FromSlash(placed.RelativePath))); err != nil {
		t.Fatalf("placed file missing in library: %v", err)
	}
}

// Confirmed-sample fallback when the larger file is a 60s preview and the smaller file is the real episode.
func TestShowConfirmedSampleFallbackWhenPreviewIsLarger(t *testing.T) {
	relFolder := "Tokyo Ghoul S01E09 1080p BluRay x264-ANiHLS"
	previewName := relFolder + ".mkv"
	realName := "anihls-tokgho.s01e09.1080p.bluray.mkv"

	w := tokyoGhoulSetup(t, relFolder, map[string]struct {
		secs int
		size int
	}{
		previewName: {secs: 60, size: 3 << 20},
		realName:    {secs: 24 * 60, size: 1500 << 10},
	})
	imp, err := (&Importer{Store: w.s}).Import(context.Background(), w.dlID, false, domain.System)
	if err != nil {
		t.Fatal(err)
	}
	if imp.State != "done" {
		t.Fatalf("import state %s, plan %+v", imp.State, imp.Plan)
	}
	if len(imp.Plan) != 2 {
		t.Fatalf("expected 2 unique plan steps, got %d: %+v", len(imp.Plan), imp.Plan)
	}
	sl := w.slot(t, 9)
	if sl.State != mediafiles.SlotInLibrary || sl.FileID == nil {
		t.Fatalf("slot not placed: %+v", sl)
	}
	placed := w.file(t, *sl.FileID)
	if placed.ReleaseName == nil || *placed.ReleaseName != strings.TrimSuffix(realName, ".mkv") {
		t.Fatalf("expected %q placed, got %+v", realName, placed.ReleaseName)
	}
	if _, err := os.Stat(filepath.Join(w.lib, filepath.FromSlash(placed.RelativePath))); err != nil {
		t.Fatalf("placed file missing in library: %v", err)
	}
}

// Confirmed-sample fallback for movies when the larger file is a 60s preview and the smaller file is the full movie.
func TestMovieConfirmedSampleFallbackWhenPreviewIsLarger(t *testing.T) {
	w, _, _ := upgradeWorld(t, relName, relName, "")
	setFake(t, w.s, "import_size_floors", "true")
	setFake(t, w.s, "import_min_video_mb", "1")
	_ = os.Remove(filepath.Join(w.dl, relName+".mkv"))

	previewRel := filepath.Join(relName, relName+".mkv")
	realRel := filepath.Join(relName, "grp-dpt.2024.1080p.web.mkv")
	pSz := writeTestMKV(t, filepath.Join(w.dl, previewRel), 60, 3<<20)
	rSz := writeTestMKV(t, filepath.Join(w.dl, realRel), 120*60, 1500<<10)

	err := w.s.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec("UPDATE items SET runtime = 120"); err != nil {
			return err
		}
		_, err := tx.Exec(
			`UPDATE downloads SET files_seen = json_array(json_object('name', ?, 'size', ?), json_object('name', ?, 'size', ?)) WHERE id = ?`,
			filepath.ToSlash(previewRel), pSz, filepath.ToSlash(realRel), rSz, w.dlID,
		)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	imp, err := (&Importer{Store: w.s}).Import(context.Background(), w.dlID, false, domain.System)
	if err != nil {
		t.Fatal(err)
	}
	if imp.State != "done" {
		t.Fatalf("import state %s, plan %+v, result %+v", imp.State, imp.Plan, imp.Result)
	}
	byBase := map[string]mediafiles.PlanStep{}
	for _, st := range imp.Plan {
		byBase[filepath.Base(st.SourceFile)] = st
	}
	if len(imp.Plan) != 2 {
		t.Fatalf("expected 2 unique plan steps, got %d: %+v", len(imp.Plan), imp.Plan)
	}
	if byBase[filepath.Base(previewRel)].Method != "refuse" || !strings.Contains(strings.Join(byBase[filepath.Base(previewRel)].Why, " "), "a sample, not the full video") {
		t.Fatalf("expected preview refused as sample, got %+v", byBase[filepath.Base(previewRel)])
	}
	if byBase[filepath.Base(realRel)].Method != "hardlink" {
		t.Fatalf("expected real movie promoted to hardlink, got %+v", byBase[filepath.Base(realRel)])
	}
	sl := slotOf(t, w)
	if sl.State != mediafiles.SlotInLibrary || sl.FileID == nil {
		t.Fatalf("movie slot not placed: %+v", sl)
	}
}

func TestPreviewAndSceneAbbrevNegativeSafety(t *testing.T) {
	relFolder := "Tokyo Ghoul S01E09 1080p BluRay x264-ANiHLS"
	wrongShow := "bravery-the.office.s01e09.1080p.bluray.mkv"
	previewName := relFolder + ".mkv"

	// Wrong show alongside a 60s preview in Tokyo Ghoul folder is still refused.
	w := tokyoGhoulSetup(t, relFolder, map[string]struct {
		secs int
		size int
	}{
		previewName: {secs: 60, size: 1500 << 10},
		wrongShow:   {secs: 24 * 60, size: 3 << 20},
	})
	imp, err := (&Importer{Store: w.s}).Import(context.Background(), w.dlID, false, domain.System)
	if err != nil {
		t.Fatal(err)
	}
	if imp.State == "done" {
		t.Fatalf("wrong show should not be placed: %+v", imp.Plan)
	}
	for _, st := range imp.Plan {
		if filepath.Base(st.SourceFile) == wrongShow && st.Method != "refuse" {
			t.Fatalf("expected wrong show refused, got %+v", st)
		}
	}
	if sl := w.slot(t, 9); sl.FileID != nil || sl.State == mediafiles.SlotInLibrary {
		t.Fatalf("wrong show must not fill slot: %+v", sl)
	}

	// Preview/ folder is a sample; an episode titled "The Preview" is NOT a sample.
	previewDirFile := "/dl/" + relFolder + "/Preview/clip.mkv"
	if !isSample(previewDirFile, releasename.Parse(filepath.Base(previewDirFile), releasename.Hints{Show: true})) {
		t.Fatalf("expected Preview/ directory file to be treated as sample")
	}
	previewsDirFile := "/dl/" + relFolder + "/Previews/clip.mkv"
	if !isSample(previewsDirFile, releasename.Parse(filepath.Base(previewsDirFile), releasename.Hints{Show: true})) {
		t.Fatalf("expected Previews/ directory file to be treated as sample")
	}
	epTitledPreview := "/dl/Show.S01E05.The.Preview.1080p.WEB.H264-GRP/Show.S01E05.The.Preview.1080p.mkv"
	if isSample(epTitledPreview, releasename.Parse(filepath.Base(epTitledPreview), releasename.Hints{Show: true})) {
		t.Fatalf("episode titled The Preview must not be falsely flagged as sample")
	}

	// Direct unit checks on matchSceneAbbrev corroboration, stop-words, and negative safety.
	lib := []mediafiles.TitleRef{{ID: 1, Name: "Tokyo Ghoul", Kind: "show"}}
	if _, _, ok := matchSceneAbbrev("anihls-tokgho.s01e09.1080p.bluray.mkv", releasename.Hints{Show: true}, lib, "Unrelated.Show.S01E09.1080p"); ok {
		t.Fatal("must not match without corroborating context")
	}
	if _, _, ok := matchSceneAbbrev("bravery-the.office.s01e02.1080p.bluray.x264.mkv", releasename.Hints{Show: true},
		[]mediafiles.TitleRef{{ID: 2, Name: "The Agency", Kind: "show"}}, "The.Agency.2024.S01E02.1080p.BluRay.x264-BRAVERY"); ok {
		t.Fatal("the.office must not match The Agency")
	}
	aotLib := []mediafiles.TitleRef{{ID: 3, Name: "Attack on Titan", Aliases: []string{"Shingeki no Kyojin"}, Kind: "show"}}
	if _, m, ok := matchSceneAbbrev("grp-at.s01e01.1080p.mkv", releasename.Hints{Show: true}, aotLib, "Attack.on.Titan.S01E01.1080p"); !ok || m.Title == nil {
		t.Fatal("expected stop-word-trimmed abbreviation 'at' to match Attack on Titan")
	}
}
