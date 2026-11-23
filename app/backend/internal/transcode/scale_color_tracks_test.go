package transcode

import (
	"slices"
	"strings"
	"testing"
)

func TestComputeScalePlan(t *testing.T) {
	def := DefaultProfileConfig()

	t.Run("4k_downscale", func(t *testing.T) {
		p, err := ComputeScalePlan(3840, 2160, "1:1", def)
		if err != nil {
			t.Fatal(err)
		}
		if p.ScaledW != 1920 || p.ScaledH != 1080 || p.NeedsPad || p.AlreadyNormalized {
			t.Fatalf("unexpected 4k plan: %+v", p)
		}
	})

	t.Run("720p_upscale_normalize", func(t *testing.T) {
		p, err := ComputeScalePlan(1280, 720, "1:1", def)
		if err != nil {
			t.Fatal(err)
		}
		if p.ScaledW != 1920 || p.ScaledH != 1080 || p.AlreadyNormalized {
			t.Fatalf("unexpected 720p normalize plan: %+v", p)
		}
	})

	t.Run("720p_downscale_only", func(t *testing.T) {
		cfg := def
		cfg.ScaleMode = "downscale_only"
		p, err := ComputeScalePlan(1280, 720, "1:1", cfg)
		if err != nil {
			t.Fatal(err)
		}
		if p.ScaledW != 1280 || p.ScaledH != 720 || !p.AlreadyNormalized {
			t.Fatalf("unexpected downscale_only plan: %+v", p)
		}
	})

	t.Run("4_3_pillarbox", func(t *testing.T) {
		p, err := ComputeScalePlan(640, 480, "1:1", def)
		if err != nil {
			t.Fatal(err)
		}
		if p.ScaledW != 1440 || p.ScaledH != 1080 || p.FrameW != 1920 || p.FrameH != 1080 || !p.NeedsPad || p.PadX != 240 {
			t.Fatalf("unexpected 4:3 plan: %+v", p)
		}
	})

	t.Run("portrait_shorts", func(t *testing.T) {
		p, err := ComputeScalePlan(720, 1280, "1:1", def)
		if err != nil {
			t.Fatal(err)
		}
		if p.ScaledW != 608 || p.ScaledH != 1080 || p.NeedsPad {
			t.Fatalf("unexpected portrait plan: %+v", p)
		}
	})

	t.Run("already_1080p", func(t *testing.T) {
		p, err := ComputeScalePlan(1920, 1080, "1:1", def)
		if err != nil {
			t.Fatal(err)
		}
		if !p.AlreadyNormalized {
			t.Fatalf("expected AlreadyNormalized=true, got %+v", p)
		}
	})

	t.Run("scale_none", func(t *testing.T) {
		cfg := def
		cfg.ScaleMode = "none"
		p, err := ComputeScalePlan(1440, 900, "1:1", cfg)
		if err != nil {
			t.Fatal(err)
		}
		if p.ScaledW != 1440 || p.ScaledH != 900 || !p.AlreadyNormalized {
			t.Fatalf("unexpected none plan: %+v", p)
		}
	})
}

func TestResolveColorHandlingAndFilter(t *testing.T) {
	dv5 := ProbeStream{
		ColorPrimaries: "bt2020",
		ColorSpace:     "ipt-c2",
		SideDataList:   []ProbeSideData{{DVProfile: intPtr(5)}},
	}
	tm, flags := ResolveColorHandling(dv5, "preserve")
	if !tm {
		t.Fatal("expected DV5 to always tonemap")
	}
	if !slices.Contains(flags, "bt709") {
		t.Fatalf("expected bt709 flags, got %v", flags)
	}

	hdr10 := ProbeStream{
		ColorPrimaries: "bt2020",
		ColorTransfer:  "smpte2084",
		ColorSpace:     "bt2020nc",
		ColorRange:     "tv",
	}
	tm, flags = ResolveColorHandling(hdr10, "preserve")
	if tm {
		t.Fatal("expected HDR10 preserve to not tonemap")
	}
	if !slices.Contains(flags, "smpte2084") {
		t.Fatalf("expected smpte2084 preserved, got %v", flags)
	}
	tm, _ = ResolveColorHandling(hdr10, "tonemap_sdr")
	if !tm {
		t.Fatal("expected HDR10 tonemap_sdr to tonemap")
	}

	plan, _ := ComputeScalePlan(3840, 2160, "1:1", DefaultProfileConfig())
	vf := BuildVideoFilter(plan, "p010le", true, true, false, false)
	if !strings.Contains(vf, "zscale=t=linear:npl=1000") || !strings.Contains(vf, "format=p010le,hwupload") {
		t.Fatalf("unexpected zscale filter: %s", vf)
	}
	vfTX := BuildVideoFilter(plan, "p010le", true, false, true, true)
	if !strings.Contains(vfTX, "tonemapx=") || !strings.Contains(vfTX, "format=p010le,hwupload") {
		t.Fatalf("unexpected tonemapx vulkan filter: %s", vfTX)
	}
}

func TestSelectBestStreamsAndMaps(t *testing.T) {
	streams := []ProbeStream{
		{Index: 0, CodecType: "video", CodecName: "hevc", Width: 1920, Height: 1080},
		{Index: 1, CodecType: "audio", Channels: 6, BitRate: "640000", Tags: map[string]string{"language": "fre", "title": "VFF TrueFrench"}},
		{Index: 2, CodecType: "audio", Channels: 2, BitRate: "192000", Tags: map[string]string{"language": "fre", "title": "VFQ"}},
		{Index: 3, CodecType: "audio", Channels: 6, BitRate: "384000", Tags: map[string]string{"language": "eng", "title": "Director's Commentary"}},
		{Index: 4, CodecType: "audio", Channels: 2, BitRate: "192000", Tags: map[string]string{"language": "eng", "title": "English Main"}},
		{Index: 5, CodecType: "audio", Channels: 6, BitRate: "448000", Tags: map[string]string{"language": "ita", "title": "Italian Original"}},
		{Index: 6, CodecType: "subtitle", CodecName: "subrip", Tags: map[string]string{"language": "eng", "title": "SDH"}},
		{Index: 7, CodecType: "subtitle", CodecName: "ass", Tags: map[string]string{"language": "eng", "title": "Full"}},
		{Index: 8, CodecType: "subtitle", CodecName: "mov_text", Tags: map[string]string{"language": "fra", "title": "Forced"}},
	}

	audio, subs := SelectBestStreams(streams, "ita", []string{"eng", "fra", "jpn", "und"}, true)
	if len(audio) != 3 {
		t.Fatalf("expected 3 audio streams (fra, eng, ita), got %+v", audio)
	}
	if audio[0].ISOLang != "fra" || audio[0].RelIndex != 1 {
		t.Fatalf("expected VFQ stream rel=1 to win over VFF, got %+v", audio[0])
	}
	if audio[1].ISOLang != "eng" || audio[1].RelIndex != 3 {
		t.Fatalf("expected non-commentary eng stream rel=3, got %+v", audio[1])
	}
	if audio[2].ISOLang != "ita" || audio[2].RelIndex != 4 {
		t.Fatalf("expected original language ita stream rel=4, got %+v", audio[2])
	}

	if len(subs) != 2 {
		t.Fatalf("expected 2 subs, got %+v", subs)
	}
	if subs[0].ISOLang != "eng" || subs[0].RelIndex != 1 || subs[0].CodecName != "ass" {
		t.Fatalf("expected Full ASS subtitle rel=1 to beat SDH SRT, got %+v", subs[0])
	}

	cfg := DefaultProfileConfig()
	maps, tail := BuildStreamMapsAndTail(true, cfg, "/tmp/out.mkv", 0, audio, subs)
	joinedMaps := strings.Join(maps, " ")
	if !strings.Contains(joinedMaps, "-map 0:t?") {
		t.Fatalf("expected anime attachment map when ass sub present, got %s", joinedMaps)
	}
	joinedTail := strings.Join(tail, " ")
	if !strings.Contains(joinedTail, "-c:s:1 srt") {
		t.Fatalf("expected mov_text converted to srt, got %s", joinedTail)
	}
	if !strings.Contains(joinedTail, "-max_muxing_queue_size 4096 -f matroska /tmp/out.mkv") {
		t.Fatalf("expected -max_muxing_queue_size 4096 right before -f matroska, got %s", joinedTail)
	}
	if !strings.Contains(joinedTail, "comment="+DefaultWriteTag) {
		t.Fatalf("expected default WriteTag in tail: %s", joinedTail)
	}

	cfgCustom := cfg
	cfgCustom.WriteTag = "MyCustomTag"
	_, tailCustom := BuildStreamMapsAndTail(true, cfgCustom, "/tmp/out.mkv", 0, audio, subs)
	if !strings.Contains(strings.Join(tailCustom, " "), "comment=MyCustomTag") {
		t.Fatalf("expected custom WriteTag in tail: %v", tailCustom)
	}

	cfgNone := cfg
	cfgNone.WriteTag = ""
	_, tailNone := BuildStreamMapsAndTail(true, cfgNone, "/tmp/out.mkv", 0, audio, subs)
	if strings.Contains(strings.Join(tailNone, " "), "-metadata comment=") {
		t.Fatalf("expected no comment metadata when write_tag is empty: %v", tailNone)
	}
}

func TestEvaluateProbe(t *testing.T) {
	cfg := DefaultProfileConfig()
	av1Untagged := ProbeResult{
		Format:  ProbeFormat{Tags: map[string]string{}},
		Streams: []ProbeStream{{CodecType: "video", CodecName: "av1", Width: 1920, Height: 1080}},
	}
	skip, reason := EvaluateProbe(av1Untagged, "/lib/movie.mkv", 500<<20, cfg, true, false)
	if skip {
		t.Fatalf("untagged AV1 with skip_tags non-empty should not skip, got reason=%s", reason)
	}

	for _, tag := range []string{DefaultWriteTag, fileFlowsCommentTag} {
		av1Tagged := av1Untagged
		av1Tagged.Format.Tags = map[string]string{"COMMENT": tag}
		skip, reason = EvaluateProbe(av1Tagged, "/lib/movie.mkv", 500<<20, cfg, true, false)
		if !skip || reason != "tag_matched" {
			t.Fatalf("expected tag_matched skip for %q, got skip=%v reason=%s", tag, skip, reason)
		}
	}

	av1Tagged := av1Untagged
	av1Tagged.Format.Tags = map[string]string{"ENCODER": "encoded by mytag"}
	cfgCustom := cfg
	cfgCustom.SkipTags = []string{"FirstTag", "MYTAG"}
	skip, reason = EvaluateProbe(av1Tagged, "/lib/movie.mp4", 500<<20, cfgCustom, true, false)
	if !skip || reason != "tag_matched" {
		t.Fatalf("expected tag_matched skip on mp4 for custom skip tag, got skip=%v reason=%s", skip, reason)
	}

	skip, reason = EvaluateProbe(av1Tagged, "/lib/movie.mkv", 500<<20, cfgCustom, false, false)
	if !skip || reason != "library_disabled" {
		t.Fatalf("expected library_disabled skip, got skip=%v reason=%s", skip, reason)
	}
	skip, _ = EvaluateProbe(av1Tagged, "/lib/movie.mkv", 500<<20, cfgCustom, false, true)
	if skip {
		t.Fatal("force=true should bypass skip checks")
	}

	cfgNoFF := cfg
	cfgNoFF.SkipTags = []string{}
	cfgNoFF.TargetCodec = "hevc"
	hevc := ProbeResult{Streams: []ProbeStream{{CodecType: "video", CodecName: "hevc", Width: 1920, Height: 1080}}}
	skip, reason = EvaluateProbe(hevc, "/lib/movie.mkv", 500<<20, cfgNoFF, true, false)
	if !skip || reason != "already_hevc" {
		t.Fatalf("expected already_hevc, got skip=%v reason=%s", skip, reason)
	}
}

func intPtr(v int) *int { return &v }
