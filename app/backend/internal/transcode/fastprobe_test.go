package transcode

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func ebmlIDBytes(id uint64) []byte {
	switch {
	case id <= 0xff:
		return []byte{byte(id)}
	case id <= 0xffff:
		return []byte{byte(id >> 8), byte(id)}
	case id <= 0xffffff:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	default:
		return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	}
}

func ebmlSizeVINT(n int) []byte {
	if n < 127 {
		return []byte{byte(0x80 | n)}
	}
	if n < 16383 {
		return []byte{byte(0x40 | (n >> 8)), byte(n)}
	}
	return []byte{
		byte(0x20 | (n >> 16)),
		byte(n >> 8),
		byte(n),
	}
}

func ebmlEl(id uint64, payload []byte) []byte {
	out := ebmlIDBytes(id)
	out = append(out, ebmlSizeVINT(len(payload))...)
	out = append(out, payload...)
	return out
}

func ebmlUint(val uint64) []byte {
	if val == 0 {
		return []byte{0}
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], val)
	i := 0
	for i < 7 && buf[i] == 0 {
		i++
	}
	return buf[i:]
}

func ebmlSeekEntry(targetID uint64, relPos int) []byte {
	return ebmlEl(ebmlFastSeekID, bytes.Join([][]byte{
		ebmlEl(ebmlFastSeekIDID, ebmlIDBytes(targetID)),
		ebmlEl(ebmlFastSeekPosition, ebmlUint(uint64(relPos))),
	}, nil))
}

func ebmlHeaderBytes() []byte {
	return ebmlEl(ebmlFastHeaderID, ebmlEl(ebmlFastDocTypeID, []byte("matroska")))
}

func ebmlTracksBytes(codecID string, width, height, transfer int) []byte {
	videoChildren := [][]byte{
		ebmlEl(ebmlFastPixelWidth, ebmlUint(uint64(width))),
		ebmlEl(ebmlFastPixelHeight, ebmlUint(uint64(height))),
	}
	if transfer > 0 {
		videoChildren = append(videoChildren, ebmlEl(ebmlFastColourID, ebmlEl(ebmlFastTransferID, ebmlUint(uint64(transfer)))))
	}
	entry := bytes.Join([][]byte{
		ebmlEl(ebmlFastTrackTypeID, ebmlUint(1)),
		ebmlEl(ebmlFastCodecID, []byte(codecID)),
		ebmlEl(ebmlFastVideoID, bytes.Join(videoChildren, nil)),
	}, nil)
	return ebmlEl(ebmlFastTracksID, ebmlEl(ebmlFastTrackEntryID, entry))
}

func ebmlTagsBytes(comment string) []byte {
	simple := bytes.Join([][]byte{
		ebmlEl(ebmlFastTagNameID, []byte("comment")),
		ebmlEl(ebmlFastTagStringID, []byte(comment)),
	}, nil)
	return ebmlEl(ebmlFastTagsID, ebmlEl(ebmlFastTagID, ebmlEl(ebmlFastSimpleTagID, simple)))
}

func ebmlClusterBytes(size int, fill byte) []byte {
	payload := bytes.Repeat([]byte{fill}, size)
	return ebmlEl(ebmlFastClusterID, payload)
}

func writeMKVSegment(t *testing.T, path string, segPayload []byte) {
	t.Helper()
	var buf bytes.Buffer
	buf.Write(ebmlHeaderBytes())
	buf.Write(ebmlEl(ebmlFastSegmentID, segPayload))
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFastProbeMKVVariants(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	t.Run("tags_before_cluster", func(t *testing.T) {
		p := filepath.Join(dir, "before_cluster.mkv")
		tracks := ebmlTracksBytes("V_AV1", 1920, 1080, 16)
		tags := ebmlTagsBytes(fileFlowsCommentTag)
		cluster := ebmlClusterBytes(2048, 0xaa)
		writeMKVSegment(t, p, bytes.Join([][]byte{tracks, tags, cluster}, nil))

		fp, err := FastProbeFile(ctx, "", p)
		if err != nil {
			t.Fatal(err)
		}
		if !fp.HasVideo || fp.SourceCodec != "av1" || fp.SourceWidth != 1920 || fp.SourceHeight != 1080 || fp.SourceHDR != "hdr10" || !MatchSkipTags(fp.Tags, DefaultSkipTags()) || fp.UsedFallback {
			t.Fatalf("unexpected FastProbeResult: %+v", fp)
		}
	})

	t.Run("tags_after_large_cluster_via_seekhead", func(t *testing.T) {
		p := filepath.Join(dir, "seekhead_tags.mkv")
		tracks := ebmlTracksBytes("V_MPEGH/ISO/HEVC", 3840, 2160, 18)
		cluster := ebmlClusterBytes(200*1024, 0x11)
		tags := ebmlTagsBytes("Created by FileFlows https://fileflows.com")

		// Build SeekHead twice: first with placeholder offset to learn exact SeekHead byte length.
		dummySH := ebmlEl(ebmlFastSeekHeadID, ebmlSeekEntry(ebmlFastTagsID, 1<<20))
		tagsOffset := len(dummySH) + len(tracks) + len(cluster)
		realSH := ebmlEl(ebmlFastSeekHeadID, ebmlSeekEntry(ebmlFastTagsID, tagsOffset))
		if len(realSH) != len(dummySH) {
			tagsOffset = len(realSH) + len(tracks) + len(cluster)
			realSH = ebmlEl(ebmlFastSeekHeadID, ebmlSeekEntry(ebmlFastTagsID, tagsOffset))
		}
		writeMKVSegment(t, p, bytes.Join([][]byte{realSH, tracks, cluster, tags}, nil))

		fp, err := FastProbeFile(ctx, "", p)
		if err != nil {
			t.Fatal(err)
		}
		if !fp.HasVideo || fp.SourceCodec != "hevc" || fp.SourceWidth != 3840 || fp.SourceHeight != 2160 || fp.SourceHDR != "hlg" || !MatchSkipTags(fp.Tags, DefaultSkipTags()) || fp.UsedFallback {
			t.Fatalf("unexpected FastProbeResult: %+v", fp)
		}
	})

	t.Run("secondary_seekhead_pointing_to_tags", func(t *testing.T) {
		p := filepath.Join(dir, "secondary_seekhead.mkv")
		tracks := ebmlTracksBytes("V_MPEG4/ISO/AVC", 1280, 720, 0)
		cluster := ebmlClusterBytes(150*1024, 0x22)
		tags := ebmlTagsBytes("Created by FileFlows")

		dummySH2 := ebmlEl(ebmlFastSeekHeadID, ebmlSeekEntry(ebmlFastTagsID, 1<<20))
		dummySH1 := ebmlEl(ebmlFastSeekHeadID, ebmlSeekEntry(ebmlFastSeekHeadID, 1<<20))

		sh2Offset := len(dummySH1) + len(tracks) + len(cluster)
		tagsOffset := sh2Offset + len(dummySH2)
		sh2 := ebmlEl(ebmlFastSeekHeadID, ebmlSeekEntry(ebmlFastTagsID, tagsOffset))
		sh1 := ebmlEl(ebmlFastSeekHeadID, ebmlSeekEntry(ebmlFastSeekHeadID, sh2Offset))
		if len(sh1) != len(dummySH1) || len(sh2) != len(dummySH2) {
			sh2Offset = len(sh1) + len(tracks) + len(cluster)
			tagsOffset = sh2Offset + len(sh2)
			sh2 = ebmlEl(ebmlFastSeekHeadID, ebmlSeekEntry(ebmlFastTagsID, tagsOffset))
			sh1 = ebmlEl(ebmlFastSeekHeadID, ebmlSeekEntry(ebmlFastSeekHeadID, sh2Offset))
		}
		writeMKVSegment(t, p, bytes.Join([][]byte{sh1, tracks, cluster, sh2, tags}, nil))

		fp, err := FastProbeFile(ctx, "", p)
		if err != nil {
			t.Fatal(err)
		}
		if !fp.HasVideo || fp.SourceCodec != "h264" || fp.SourceWidth != 1280 || fp.SourceHeight != 720 || !MatchSkipTags(fp.Tags, DefaultSkipTags()) || fp.UsedFallback {
			t.Fatalf("unexpected FastProbeResult: %+v", fp)
		}
	})

	t.Run("tags_in_tail_without_seekhead_and_with_noise", func(t *testing.T) {
		p := filepath.Join(dir, "tail_tags.mkv")
		tracks := ebmlTracksBytes("V_VP9", 1920, 1080, 0)
		// Embed false Tags ID bytes in cluster tail to verify parser skips invalid candidates safely.
		clusterPayload := bytes.Repeat([]byte{0x33}, 120*1024)
		copy(clusterPayload[len(clusterPayload)-128:], []byte{0x12, 0x54, 0xc3, 0x67, 0x01, 0xff, 0xff, 0xff})
		cluster := ebmlEl(ebmlFastClusterID, clusterPayload)
		tags := ebmlTagsBytes(DefaultWriteTag)
		writeMKVSegment(t, p, bytes.Join([][]byte{tracks, cluster, tags}, nil))

		fp, err := FastProbeFile(ctx, "", p)
		if err != nil {
			t.Fatal(err)
		}
		if !fp.HasVideo || fp.SourceCodec != "vp9" || !MatchSkipTags(fp.Tags, DefaultSkipTags()) || fp.UsedFallback {
			t.Fatalf("unexpected FastProbeResult: %+v", fp)
		}
	})

	t.Run("custom_skip_tag_and_untagged_mkv", func(t *testing.T) {
		p := filepath.Join(dir, "untagged.mkv")
		tracks := ebmlTracksBytes("V_MPEGH/ISO/HEVC", 1920, 1080, 0)
		tags := ebmlTagsBytes("Encoded by HandBrake-Custom")
		cluster := ebmlClusterBytes(1024, 0x44)
		writeMKVSegment(t, p, bytes.Join([][]byte{tracks, tags, cluster}, nil))

		fp, err := FastProbeFile(ctx, "", p)
		if err != nil {
			t.Fatal(err)
		}
		if !fp.HasVideo || fp.SourceCodec != "hevc" || MatchSkipTags(fp.Tags, DefaultSkipTags()) || fp.UsedFallback {
			t.Fatalf("unexpected FastProbeResult: %+v", fp)
		}
		if !MatchSkipTags(fp.Tags, []string{"holyarrs", "handbrake-custom"}) {
			t.Fatalf("expected custom skip tag match against %v", fp.Tags)
		}
	})
}

func makeMP4Box(boxType string, payload []byte) []byte {
	sz := uint32(8 + len(payload))
	out := make([]byte, sz)
	out[0], out[1], out[2], out[3] = byte(sz>>24), byte(sz>>16), byte(sz>>8), byte(sz)
	copy(out[4:8], boxType[:4])
	copy(out[8:], payload)
	return out
}

func TestFastProbeMP4Tags(t *testing.T) {
	// Construct a bounded MP4 layout: ftyp + mdat + moov(udta(meta(ilst(©cmt(data)))))
	ftyp := makeMP4Box("ftyp", []byte("isom\x00\x00\x02\x00isomiso2mp41"))
	mdat := makeMP4Box("mdat", bytes.Repeat([]byte{0x55}, 2048))
	dataPayload := append([]byte{0, 0, 0, 1, 0, 0, 0, 0}, []byte("Transcoded by HolyArrs v2")...)
	dataBox := makeMP4Box("data", dataPayload)
	cmtBox := makeMP4Box("\xa9cmt", dataBox)
	ilstBox := makeMP4Box("ilst", cmtBox)
	metaPayload := append([]byte{0, 0, 0, 0}, ilstBox...)
	metaBox := makeMP4Box("meta", metaPayload)
	udtaBox := makeMP4Box("udta", metaBox)
	moovBox := makeMP4Box("moov", udtaBox)
	mp4Bytes := bytes.Join([][]byte{ftyp, mdat, moovBox}, nil)

	tags := probeMP4FastTags(bytes.NewReader(mp4Bytes), int64(len(mp4Bytes)))
	if !MatchSkipTags(tags, DefaultSkipTags()) {
		t.Fatalf("expected MP4 metadata tag match, got %v", tags)
	}
	if !MatchSkipTags(tags, []string{"unrelated", "HOLYARRS V2"}) {
		t.Fatalf("expected case-insensitive substring match, got %v", tags)
	}
}

func TestEvaluateFastProbe(t *testing.T) {
	cfg := DefaultProfileConfig()

	// Tagged MKV skips for HolyArrs or FileFlows even if codec is h264.
	for _, tag := range []string{"HolyArrs", fileFlowsCommentTag} {
		skip, reason := EvaluateFastProbe(FastProbeResult{
			HasVideo:    true,
			SourceCodec: "h264",
			Tags:        []string{tag},
		}, "/lib/movie.mkv", 500<<20, cfg, true, false)
		if !skip || reason != "tag_matched" {
			t.Fatalf("expected tag_matched skip for %q on mkv, got skip=%v reason=%s", tag, skip, reason)
		}
	}

	// Tagged MP4 also skips with custom skip tag (container-agnostic).
	cfgCustom := cfg
	cfgCustom.SkipTags = []string{"other-tag", "mycustomencoder"}
	skip, reason := EvaluateFastProbe(FastProbeResult{
		HasVideo:    true,
		SourceCodec: "h264",
		Tags:        []string{"Encoded with MyCustomEncoder 1.0"},
	}, "/lib/movie.mp4", 500<<20, cfgCustom, true, false)
	if !skip || reason != "tag_matched" {
		t.Fatalf("expected tag_matched skip on mp4, got skip=%v reason=%s", skip, reason)
	}

	// Untagged AV1 with non-empty SkipTags does not skip.
	skip, reason = EvaluateFastProbe(FastProbeResult{
		HasVideo:    true,
		SourceCodec: "av1",
	}, "/lib/movie.mkv", 500<<20, cfg, true, false)
	if skip {
		t.Fatalf("expected untagged av1 not to skip when skip_tags non-empty, got %s", reason)
	}

	// Empty SkipTags falls back to already_<codec> on .mkv.
	cfgEmpty := cfg
	cfgEmpty.SkipTags = []string{}
	skip, reason = EvaluateFastProbe(FastProbeResult{
		HasVideo:    true,
		SourceCodec: "av1",
	}, "/lib/movie.mkv", 500<<20, cfgEmpty, true, false)
	if !skip || reason != "already_av1" {
		t.Fatalf("expected already_av1 fallback when skip_tags empty, got skip=%v reason=%s", skip, reason)
	}

	// Below min size skips.
	cfgMin := cfg
	cfgMin.MinFileSizeMB = 100
	skip, reason = EvaluateFastProbe(FastProbeResult{
		HasVideo:    true,
		SourceCodec: "h264",
	}, "/lib/movie.mkv", 10<<20, cfgMin, true, false)
	if !skip || reason != "below_min_size" {
		t.Fatalf("expected below_min_size, got skip=%v reason=%s", skip, reason)
	}
}

func TestFastProbeRealFFmpegMKVAndMP4(t *testing.T) {
	ffmpegBin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available in PATH")
	}
	dir := t.TempDir()
	mkvPath := filepath.Join(dir, "real.mkv")
	cmd := exec.Command(ffmpegBin,
		"-y", "-f", "lavfi", "-i", "color=c=black:s=320x240:d=0.2",
		"-c:v", "libx264", "-metadata", "comment="+fileFlowsCommentTag, mkvPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg generate mkv: %v (%s)", err, string(out))
	}
	fp, err := FastProbeFile(context.Background(), "", mkvPath)
	if err != nil {
		t.Fatal(err)
	}
	if !fp.HasVideo || fp.SourceCodec != "h264" || fp.SourceWidth != 320 || fp.SourceHeight != 240 || !MatchSkipTags(fp.Tags, DefaultSkipTags()) || fp.UsedFallback {
		t.Fatalf("unexpected real ffmpeg MKV fast probe result: %+v", fp)
	}

	mp4Path := filepath.Join(dir, "real.mp4")
	cmdMP4 := exec.Command(ffmpegBin,
		"-y", "-f", "lavfi", "-i", "color=c=black:s=320x240:d=0.2",
		"-c:v", "libx264", "-metadata", "comment="+DefaultWriteTag, mp4Path)
	if out, err := cmdMP4.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg generate mp4: %v (%s)", err, string(out))
	}
	fpMP4, err := FastProbeFile(context.Background(), "", mp4Path)
	if err != nil {
		t.Fatal(err)
	}
	if !fpMP4.HasVideo || !MatchSkipTags(fpMP4.Tags, DefaultSkipTags()) || fpMP4.UsedFallback {
		t.Fatalf("unexpected real ffmpeg MP4 fast probe result: %+v", fpMP4)
	}
}
