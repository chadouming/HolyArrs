package transcode

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type langEntry struct {
	iso     string
	display string
}

var langTable = map[string]langEntry{
	"en": {"eng", "English"}, "eng": {"eng", "English"}, "english": {"eng", "English"},
	"fr": {"fra", "French"}, "fra": {"fra", "French"}, "fre": {"fra", "French"},
	"fr-ca": {"fra", "French"}, "fr-fr": {"fra", "French"}, "french": {"fra", "French"},
	"francais": {"fra", "French"}, "français": {"fra", "French"},
	"ja": {"jpn", "Japanese"}, "jpn": {"jpn", "Japanese"}, "japanese": {"jpn", "Japanese"},
	"ar": {"ara", "Arabic"}, "ara": {"ara", "Arabic"}, "arabic": {"ara", "Arabic"},
	"ko": {"kor", "Korean"}, "kor": {"kor", "Korean"}, "korean": {"kor", "Korean"},
	"es": {"spa", "Spanish"}, "spa": {"spa", "Spanish"}, "spanish": {"spa", "Spanish"},
	"de": {"deu", "German"}, "deu": {"deu", "German"}, "ger": {"deu", "German"}, "german": {"deu", "German"},
	"it": {"ita", "Italian"}, "ita": {"ita", "Italian"}, "italian": {"ita", "Italian"},
	"pt": {"por", "Portuguese"}, "por": {"por", "Portuguese"}, "portuguese": {"por", "Portuguese"},
	"ru": {"rus", "Russian"}, "rus": {"rus", "Russian"}, "russian": {"rus", "Russian"},
	"zh": {"zho", "Chinese"}, "zho": {"zho", "Chinese"}, "chi": {"zho", "Chinese"},
	"cmn": {"zho", "Chinese"}, "yue": {"zho", "Chinese"}, "chinese": {"zho", "Chinese"},
	"hi": {"hin", "Hindi"}, "hin": {"hin", "Hindi"}, "hindi": {"hin", "Hindi"},
	"pl": {"pol", "Polish"}, "pol": {"pol", "Polish"}, "polish": {"pol", "Polish"},
	"nl": {"nld", "Dutch"}, "nld": {"nld", "Dutch"}, "dut": {"nld", "Dutch"}, "dutch": {"nld", "Dutch"},
	"sv": {"swe", "Swedish"}, "swe": {"swe", "Swedish"}, "swedish": {"swe", "Swedish"},
	"da": {"dan", "Danish"}, "dan": {"dan", "Danish"}, "danish": {"dan", "Danish"},
	"no": {"nor", "Norwegian"}, "nor": {"nor", "Norwegian"}, "nob": {"nor", "Norwegian"},
	"nno": {"nor", "Norwegian"}, "norwegian": {"nor", "Norwegian"},
	"fi": {"fin", "Finnish"}, "fin": {"fin", "Finnish"}, "finnish": {"fin", "Finnish"},
	"tr": {"tur", "Turkish"}, "tur": {"tur", "Turkish"}, "turkish": {"tur", "Turkish"},
	"th": {"tha", "Thai"}, "tha": {"tha", "Thai"}, "thai": {"tha", "Thai"},
	"vi": {"vie", "Vietnamese"}, "vie": {"vie", "Vietnamese"}, "vietnamese": {"vie", "Vietnamese"},
	"id": {"ind", "Indonesian"}, "ind": {"ind", "Indonesian"}, "indonesian": {"ind", "Indonesian"},
	"uk": {"ukr", "Ukrainian"}, "ukr": {"ukr", "Ukrainian"}, "ukrainian": {"ukr", "Ukrainian"},
	"cs": {"ces", "Czech"}, "ces": {"ces", "Czech"}, "cze": {"ces", "Czech"}, "czech": {"ces", "Czech"},
	"hu": {"hun", "Hungarian"}, "hun": {"hun", "Hungarian"}, "hungarian": {"hun", "Hungarian"},
	"ro": {"ron", "Romanian"}, "ron": {"ron", "Romanian"}, "rum": {"ron", "Romanian"}, "romanian": {"ron", "Romanian"},
	"el": {"ell", "Greek"}, "ell": {"ell", "Greek"}, "gre": {"ell", "Greek"}, "greek": {"ell", "Greek"},
	"he": {"heb", "Hebrew"}, "heb": {"heb", "Hebrew"}, "hebrew": {"heb", "Hebrew"},
	"fa": {"fas", "Persian"}, "fas": {"fas", "Persian"}, "per": {"fas", "Persian"}, "persian": {"fas", "Persian"},
	"tl": {"tgl", "Tagalog"}, "tgl": {"tgl", "Tagalog"}, "tagalog": {"tgl", "Tagalog"},
	"ms": {"msa", "Malay"}, "msa": {"msa", "Malay"}, "may": {"msa", "Malay"}, "malay": {"msa", "Malay"},
	"ta": {"tam", "Tamil"}, "tam": {"tam", "Tamil"}, "tamil": {"tam", "Tamil"},
	"te": {"tel", "Telugu"}, "tel": {"tel", "Telugu"}, "telugu": {"tel", "Telugu"},
	"und": {"und", "Undetermined"}, "undetermined": {"und", "Undetermined"}, "unknown": {"und", "Undetermined"},
	"zxx": {"zxx", "No Linguistic Content"},
	"mul": {"mul", "Multiple Languages"},
}

// NormalizeLanguage normalizes ISO-639-1/2, BCP-47, or language names to ISO-639-2 + DisplayName.
func NormalizeLanguage(raw string) (string, string, bool) {
	cleaned := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(raw)), "_", "-")
	if cleaned == "" {
		return "", "", false
	}
	if ent, ok := langTable[cleaned]; ok {
		return ent.iso, ent.display, true
	}
	primary, _, _ := strings.Cut(cleaned, "-")
	if ent, ok := langTable[primary]; ok {
		return ent.iso, ent.display, true
	}
	if len(primary) == 3 && isASCIIAlpha(primary) {
		return primary, strings.ToUpper(primary), true
	}
	return "", "", false
}

func isASCIIAlpha(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 'a' || s[i] > 'z' {
			return false
		}
	}
	return true
}

// SelectedAudio describes one chosen audio track in output order.
type SelectedAudio struct {
	RelIndex     int
	ISOLang      string
	LangName     string
	IsCommentary bool
}

// SelectedSubtitle describes one chosen subtitle track in output order.
type SelectedSubtitle struct {
	RelIndex  int
	ISOLang   string
	CodecName string
}

var (
	commentaryRe = regexp.MustCompile(`(?i)\b(commentary|comments?|director'?s?|visual[ _-]?impaired|audio[ _-]?description|(?:^|\W)ad(?:$|\W))`)
	vfqRe        = regexp.MustCompile(`(?i)\b(vfq|vf2|qu[eé]b[eé]c\w*|canadi\w*|fr[-_ ]ca)\b`)
	vffRe        = regexp.MustCompile(`(?i)\b(vff|truefrench|fr[-_ ]fr|france)\b`)
	forcedSubRe  = regexp.MustCompile(`(?i)\b(forced|signs?\b|songs?\b|s&s)\b`)
	sdhSubRe     = regexp.MustCompile(`(?i)\b(sdh|cc|hearing[ _-]?impaired)\b`)
)

func resolveStreamLang(st ProbeStream, origISO string) (string, string, string) {
	rawLang := strings.TrimSpace(st.Tags["language"])
	if rawLang == "" {
		if origISO != "" {
			if iso, name, ok := NormalizeLanguage(origISO); ok {
				return iso, name, ""
			}
		}
		return "und", "Undetermined", ""
	}
	if iso, name, ok := NormalizeLanguage(rawLang); ok {
		return iso, name, rawLang
	}
	return "und", "Undetermined", rawLang
}

type rankedAudio struct {
	rank [6]int
	item SelectedAudio
}

type rankedSub struct {
	rank [4]int
	item SelectedSubtitle
}

func rankGreaterAudio(a, b [6]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func rankGreaterSub(a, b [4]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// SelectBestStreams chooses the single highest-ranked audio and subtitle stream per allowed language.
func SelectBestStreams(streams []ProbeStream, origLang string, allowed []string, preferVFQ bool) ([]SelectedAudio, []SelectedSubtitle) {
	allowedSet := make(map[string]struct{})
	baseAllowed := allowed
	if len(baseAllowed) == 0 {
		baseAllowed = []string{"eng", "fra", "jpn", "und"}
	}
	for _, lang := range baseAllowed {
		if iso, _, ok := NormalizeLanguage(lang); ok {
			allowedSet[iso] = struct{}{}
		}
	}
	origISO := ""
	if iso, _, ok := NormalizeLanguage(origLang); ok {
		origISO = iso
		allowedSet[iso] = struct{}{}
	}

	var audioByLang = map[string]rankedAudio{}
	var allAudio []rankedAudio
	var subsByLang = map[string]rankedSub{}
	var allSubs []rankedSub

	audioRel := -1
	subRel := -1
	for _, st := range streams {
		switch st.CodecType {
		case "audio":
			audioRel++
			iso, name, raw := resolveStreamLang(st, origISO)
			title := st.Tags["title"]
			isComm := st.Disposition["comment"] == 1 ||
				st.Disposition["visual_impaired"] == 1 ||
				commentaryRe.MatchString(title)
			nonComm := 1
			if isComm {
				nonComm = 0
			}
			vfScore := 1
			if iso == "fra" && preferVFQ {
				blob := raw + " " + title
				if vfqRe.MatchString(blob) {
					vfScore = 2
				} else if vffRe.MatchString(blob) {
					vfScore = 0
				}
			}
			bitrate := 0
			if b, err := strconv.Atoi(strings.TrimSpace(st.BitRate)); err == nil && b > 0 {
				bitrate = b
			} else if b, err := strconv.Atoi(strings.TrimSpace(st.Tags["BPS"])); err == nil && b > 0 {
				bitrate = b
			}
			isDef := 0
			if st.Disposition["default"] == 1 {
				isDef = 1
			}
			cand := rankedAudio{
				rank: [6]int{nonComm, vfScore, st.Channels, bitrate, isDef, -audioRel},
				item: SelectedAudio{
					RelIndex:     audioRel,
					ISOLang:      iso,
					LangName:     name,
					IsCommentary: isComm,
				},
			}
			allAudio = append(allAudio, cand)
			if _, ok := allowedSet[iso]; ok {
				cur, exists := audioByLang[iso]
				if !exists || rankGreaterAudio(cand.rank, cur.rank) {
					audioByLang[iso] = cand
				}
			}

		case "subtitle":
			subRel++
			iso, _, _ := resolveStreamLang(st, origISO)
			title := st.Tags["title"]
			codec := strings.ToLower(strings.TrimSpace(st.CodecName))

			isComm := st.Disposition["comment"] == 1 || strings.Contains(strings.ToLower(title), "commentary")
			isForced := st.Disposition["forced"] == 1 || forcedSubRe.MatchString(title)
			isSDH := st.Disposition["hearing_impaired"] == 1 || sdhSubRe.MatchString(title)

			kindTier := 3
			switch {
			case isComm:
				kindTier = 0
			case isForced:
				kindTier = 1
			case isSDH:
				kindTier = 2
			}

			codecTier := 0
			switch codec {
			case "ass", "ssa":
				codecTier = 2
			case "subrip", "srt", "webvtt", "mov_text", "tx3g", "text", "ttml":
				codecTier = 1
			}

			isDef := 0
			if st.Disposition["default"] == 1 {
				isDef = 1
			}
			cand := rankedSub{
				rank: [4]int{kindTier, codecTier, isDef, -subRel},
				item: SelectedSubtitle{
					RelIndex:  subRel,
					ISOLang:   iso,
					CodecName: codec,
				},
			}
			allSubs = append(allSubs, cand)
			if _, ok := allowedSet[iso]; ok {
				cur, exists := subsByLang[iso]
				if !exists || rankGreaterSub(cand.rank, cur.rank) {
					subsByLang[iso] = cand
				}
			}
		}
	}

	var keptAudio []SelectedAudio
	for _, cand := range audioByLang {
		keptAudio = append(keptAudio, cand.item)
	}
	if len(keptAudio) == 0 && len(allAudio) > 0 {
		best := allAudio[0]
		for _, c := range allAudio[1:] {
			if rankGreaterAudio(c.rank, best.rank) {
				best = c
			}
		}
		keptAudio = append(keptAudio, best.item)
	}
	sort.Slice(keptAudio, func(i, j int) bool { return keptAudio[i].RelIndex < keptAudio[j].RelIndex })

	var keptSubs []SelectedSubtitle
	for _, cand := range subsByLang {
		keptSubs = append(keptSubs, cand.item)
	}
	if len(keptSubs) == 0 && len(allSubs) > 0 {
		best := allSubs[0]
		for _, c := range allSubs[1:] {
			if rankGreaterSub(c.rank, best.rank) {
				best = c
			}
		}
		keptSubs = append(keptSubs, best.item)
	}
	sort.Slice(keptSubs, func(i, j int) bool { return keptSubs[i].RelIndex < keptSubs[j].RelIndex })

	return keptAudio, keptSubs
}

// IsAnimeMedia checks whether the path or title metadata indicates anime.
func IsAnimeMedia(mediaPath string, genres, tags []string) bool {
	sl := strings.ToLower(filepath.ToSlash(mediaPath))
	if strings.Contains(sl, "/anime/") || strings.HasSuffix(sl, "/anime") {
		return true
	}
	for _, g := range genres {
		if strings.Contains(strings.ToLower(g), "anime") {
			return true
		}
	}
	for _, t := range tags {
		if strings.Contains(strings.ToLower(t), "anime") {
			return true
		}
	}
	return false
}

func needsMKVSubTranscode(codec string) bool {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "mov_text", "tx3g", "text", "ttml":
		return true
	default:
		return false
	}
}

func formatChannelsLabel(ch int) string {
	switch ch {
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	case 1:
		return "1.0"
	default:
		if ch <= 0 || ch == 2 {
			return "2.0"
		}
		return strconv.Itoa(ch) + ".0"
	}
}

// BuildStreamMapsAndTail returns ffmpeg -map args and audio/subtitle/metadata/output tail args.
func BuildStreamMapsAndTail(
	isAnime bool,
	cfg ProfileConfig,
	outputPath string,
	relVideoIdx int,
	keptAudio []SelectedAudio,
	keptSubs []SelectedSubtitle,
) ([]string, []string) {
	hasASS := false
	for _, sub := range keptSubs {
		if sub.CodecName == "ass" || sub.CodecName == "ssa" {
			hasASS = true
			break
		}
	}
	keepAttachments := cfg.KeepAnimeAttachments && isAnime && hasASS

	maps := []string{"-map", fmt.Sprintf("0:v:%d", relVideoIdx)}
	for _, a := range keptAudio {
		maps = append(maps, "-map", fmt.Sprintf("0:a:%d", a.RelIndex))
	}
	for _, sub := range keptSubs {
		maps = append(maps, "-map", fmt.Sprintf("0:s:%d", sub.RelIndex))
	}
	if keepAttachments {
		maps = append(maps, "-map", "0:t?")
	}

	var tail []string
	audioCodec := strings.ToLower(strings.TrimSpace(cfg.AudioCodec))
	if audioCodec == "" {
		audioCodec = "aac"
	}
	bitrateKbps := cfg.AudioBitrateKbps
	if bitrateKbps <= 0 {
		bitrateKbps = 192
	}
	channels := cfg.AudioChannels
	if channels <= 0 {
		channels = 2
	}

	if len(keptAudio) > 0 {
		switch audioCodec {
		case "copy":
			tail = append(tail, "-c:a", "copy")
		case "opus":
			tail = append(tail, "-c:a", "libopus", "-ac", strconv.Itoa(channels), "-b:a", strconv.Itoa(bitrateKbps)+"k")
		default:
			tail = append(tail, "-c:a", "aac", "-ac", strconv.Itoa(channels), "-b:a", strconv.Itoa(bitrateKbps)+"k")
		}
	} else {
		tail = append(tail, "-an")
	}

	if len(keptSubs) > 0 {
		for idx, sub := range keptSubs {
			subCodec := "copy"
			if needsMKVSubTranscode(sub.CodecName) {
				subCodec = "srt"
			}
			tail = append(tail, fmt.Sprintf("-c:s:%d", idx), subCodec)
		}
	} else {
		tail = append(tail, "-sn")
	}

	if keepAttachments {
		tail = append(tail, "-c:t", "copy")
	}

	tail = append(tail, "-metadata:s:v:0", "title=")
	codecTag := strings.ToUpper(audioCodec)
	chLabel := formatChannelsLabel(channels)
	for idx, a := range keptAudio {
		titleVal := fmt.Sprintf("%s / %s / %s", a.LangName, codecTag, chLabel)
		if audioCodec == "copy" {
			titleVal = a.LangName
		}
		if a.IsCommentary {
			titleVal = "Commentary"
		}
		disp := "0"
		if idx == 0 {
			disp = "default"
		}
		tail = append(tail,
			fmt.Sprintf("-metadata:s:a:%d", idx), "title="+titleVal,
			fmt.Sprintf("-metadata:s:a:%d", idx), "language="+a.ISOLang,
			fmt.Sprintf("-disposition:a:%d", idx), disp,
		)
	}
	for idx, sub := range keptSubs {
		tail = append(tail,
			fmt.Sprintf("-metadata:s:s:%d", idx), "title="+sub.ISOLang,
			fmt.Sprintf("-metadata:s:s:%d", idx), "language="+sub.ISOLang,
		)
	}

	if tag := strings.TrimSpace(cfg.WriteTag); tag != "" {
		tail = append(tail, "-metadata", "comment="+tag)
	}
	tail = append(tail, "-max_muxing_queue_size", "4096", "-f", "matroska", outputPath)
	return maps, tail
}
