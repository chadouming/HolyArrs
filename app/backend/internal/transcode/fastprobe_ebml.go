package transcode

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
)

const (
	ebmlFastHeaderID      = 0x1a45dfa3
	ebmlFastDocTypeID     = 0x4282
	ebmlFastSegmentID     = 0x18538067
	ebmlFastSeekHeadID    = 0x114d9b74
	ebmlFastSeekID        = 0x4dbb
	ebmlFastSeekIDID      = 0x53ab
	ebmlFastSeekPosition  = 0x53ac
	ebmlFastClusterID     = 0x1f43b675
	ebmlFastTracksID      = 0x1654ae6b
	ebmlFastTrackEntryID  = 0xae
	ebmlFastTrackTypeID   = 0x83
	ebmlFastCodecID       = 0x86
	ebmlFastCodecPrivate  = 0x63a2
	ebmlFastVideoID       = 0xe0
	ebmlFastPixelWidth    = 0xb0
	ebmlFastPixelHeight   = 0xba
	ebmlFastColourID      = 0x55b0
	ebmlFastTransferID    = 0x55ba
	ebmlFastPrimariesID   = 0x55bb
	ebmlFastMaxCLLID      = 0x55bc
	ebmlFastMasteringID   = 0x55d0
	ebmlFastBlockAddMap   = 0x41e4
	ebmlFastBlockAddType  = 0x41e7
	ebmlFastBlockAddExtra = 0x41ed
	ebmlFastTagsID        = 0x1254c367
	ebmlFastTagID         = 0x7373
	ebmlFastSimpleTagID   = 0x67c8
	ebmlFastTagNameID     = 0x45a3
	ebmlFastTagStringID   = 0x4487

	fastBlockAddDVCC = 0x64766343
	fastBlockAddDVVC = 0x64767643
	fastBlockAddDVWC = 0x64767743

	fastMaxElements     = 8192
	fastMaxDepth        = 12
	fastMaxTracks       = 128
	fastMaxString       = 4096
	fastMaxPrivateBytes = 8192
)

type fastEBMLElement struct {
	id      uint64
	start   int64
	end     int64
	unknown bool
}

type fastEBMLReader struct {
	r        io.ReaderAt
	size     int64
	elements int
	tracks   int
}

func probeMatroskaFast(r io.ReaderAt, size int64) (FastProbeResult, error) {
	p := &fastEBMLReader{r: r, size: size}
	hdr, next, err := p.readElement(0, size, false)
	if err != nil || hdr.id != ebmlFastHeaderID || hdr.unknown {
		return FastProbeResult{}, errFastUnsupported
	}
	docType, err := p.readDocType(hdr.start, hdr.end)
	if err != nil {
		return FastProbeResult{}, err
	}
	if docType != "matroska" && docType != "webm" {
		return FastProbeResult{}, errFastUnsupported
	}

	for pos := next; pos < size; {
		el, following, err := p.readElement(pos, size, true)
		if err != nil {
			return FastProbeResult{}, err
		}
		if el.id == ebmlFastSegmentID {
			return p.readSegment(el.start, el.end)
		}
		pos = following
	}
	return FastProbeResult{}, errFastUnsupported
}

func (p *fastEBMLReader) readDocType(start, end int64) (string, error) {
	docType := ""
	err := p.each(start, end, 1, func(el fastEBMLElement) error {
		if el.id != ebmlFastDocTypeID {
			return nil
		}
		v, err := p.readString(el)
		docType = v
		return err
	})
	if err != nil {
		return "", err
	}
	if docType == "" {
		return "", errFastUnsupported
	}
	return docType, nil
}

func (p *fastEBMLReader) readSegment(segStart, segEnd int64) (FastProbeResult, error) {
	var res FastProbeResult
	tracksDone := false
	tagsDone := false
	seekTracks := int64(-1)
	seekTags := int64(-1)
	var seekHeads []int64
	visited := map[int64]bool{}

	for pos := segStart; pos < segEnd; {
		el, next, err := p.readElement(pos, segEnd, true)
		if err != nil {
			if tracksDone {
				break
			}
			return FastProbeResult{}, err
		}
		switch el.id {
		case ebmlFastSeekHeadID:
			visited[pos] = true
			_ = p.parseSeekHead(el.start, el.end, segStart, segEnd, pos, &seekTracks, &seekTags, &seekHeads)
		case ebmlFastTracksID:
			visited[pos] = true
			if !tracksDone {
				if err := p.parseTracks(el.start, el.end, &res); err == nil {
					tracksDone = true
				}
			}
		case ebmlFastTagsID:
			visited[pos] = true
			if tags, ok := p.parseTags(el.start, el.end); ok {
				res.Tags = appendCappedTags(res.Tags, tags, fastMaxTagStrings)
				tagsDone = true
			}
		case ebmlFastClusterID:
			goto afterCluster
		}
		if tracksDone && tagsDone {
			break
		}
		if el.unknown {
			break
		}
		pos = next
	}

afterCluster:
	// Follow up to 1 hop to unseen secondary SeekHeads if Tracks or Tags offsets are still unknown.
	if (!tracksDone && seekTracks < 0) || (!tagsDone && seekTags < 0) {
		for _, shOff := range seekHeads {
			if visited[shOff] || shOff < segStart || shOff >= segEnd {
				continue
			}
			visited[shOff] = true
			el, _, err := p.readElement(shOff, segEnd, false)
			if err != nil || el.id != ebmlFastSeekHeadID || el.unknown {
				continue
			}
			_ = p.parseSeekHead(el.start, el.end, segStart, segEnd, shOff, &seekTracks, &seekTags, nil)
			if (tracksDone || seekTracks >= 0) && (tagsDone || seekTags >= 0) {
				break
			}
		}
	}

	if !tracksDone && seekTracks >= segStart && seekTracks < segEnd && !visited[seekTracks] {
		visited[seekTracks] = true
		if el, _, err := p.readElement(seekTracks, segEnd, false); err == nil && el.id == ebmlFastTracksID && !el.unknown {
			if p.parseTracks(el.start, el.end, &res) == nil {
				tracksDone = true
			}
		}
	}

	if !tagsDone && seekTags >= segStart && seekTags < segEnd && !visited[seekTags] {
		visited[seekTags] = true
		if el, _, err := p.readElement(seekTags, segEnd, false); err == nil && el.id == ebmlFastTagsID && !el.unknown {
			if tags, ok := p.parseTags(el.start, el.end); ok {
				res.Tags = appendCappedTags(res.Tags, tags, fastMaxTagStrings)
				tagsDone = true
			}
		}
	}

	// Tail safety fallback for MKVs where Tags were appended near EOF without a SeekHead entry.
	if !tagsDone {
		if tags, ok := scanTailForTags(p.r, segStart, p.size); ok {
			res.Tags = appendCappedTags(res.Tags, tags, fastMaxTagStrings)
		}
	}

	if !tracksDone {
		return FastProbeResult{}, errFastUnsupported
	}
	return res, nil
}

func scanTailForTags(r io.ReaderAt, segStart, size int64) ([]string, bool) {
	if size <= segStart {
		return nil, false
	}
	tailStart := max64(segStart, size-fastReadWindowSize)
	tailLen := size - tailStart
	if tailLen < 6 {
		return nil, false
	}
	buf := make([]byte, int(tailLen))
	n, err := r.ReadAt(buf, tailStart)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false
	}
	buf = buf[:n]
	needle := []byte{0x12, 0x54, 0xc3, 0x67}
	offsetReader := bytes.NewReader(buf)
	searchFrom := 0
	for searchFrom < len(buf) {
		idx := bytes.Index(buf[searchFrom:], needle)
		if idx < 0 {
			return nil, false
		}
		relPos := int64(searchFrom + idx)
		tp := &fastEBMLReader{r: offsetReader, size: int64(len(buf))}
		el, _, err := tp.readElement(relPos, int64(len(buf)), false)
		if err == nil && el.id == ebmlFastTagsID && !el.unknown && el.end > el.start {
			if tags, ok := tp.parseTags(el.start, el.end); ok {
				return tags, true
			}
		}
		searchFrom += idx + len(needle)
	}
	return nil, false
}

func (p *fastEBMLReader) parseSeekHead(start, end, segStart, segEnd, curPos int64, seekTracks, seekTags *int64, seekHeads *[]int64) error {
	return p.each(start, end, 2, func(seek fastEBMLElement) error {
		if seek.id != ebmlFastSeekID {
			return nil
		}
		var id uint64
		at := int64(-1)
		err := p.each(seek.start, seek.end, 3, func(child fastEBMLElement) error {
			var err error
			switch child.id {
			case ebmlFastSeekIDID:
				id, err = p.readUint(child)
			case ebmlFastSeekPosition:
				var v uint64
				if v, err = p.readUint(child); err == nil && v < uint64(segEnd-segStart) {
					at = segStart + int64(v)
				}
			}
			return err
		})
		if err != nil || at < segStart || at >= segEnd {
			return err
		}
		switch id {
		case ebmlFastTracksID:
			if *seekTracks < 0 {
				*seekTracks = at
			}
		case ebmlFastTagsID:
			if *seekTags < 0 {
				*seekTags = at
			}
		case ebmlFastSeekHeadID:
			if seekHeads != nil && at != curPos {
				*seekHeads = append(*seekHeads, at)
			}
		}
		return nil
	})
}

func (p *fastEBMLReader) parseTracks(start, end int64, res *FastProbeResult) error {
	return p.each(start, end, 2, func(el fastEBMLElement) error {
		if el.id != ebmlFastTrackEntryID {
			return nil
		}
		p.tracks++
		if p.tracks > fastMaxTracks {
			return errors.New("track budget exceeded")
		}
		var trackType uint64
		var codecID string
		var private []byte
		var width, height int
		var transfer, primaries int
		var mastering bool
		var hasDV bool
		dvProfile := -1
		dvCompat := -1

		err := p.each(el.start, el.end, 3, func(child fastEBMLElement) error {
			var err error
			switch child.id {
			case ebmlFastTrackTypeID:
				trackType, err = p.readUint(child)
			case ebmlFastCodecID:
				codecID, err = p.readString(child)
			case ebmlFastCodecPrivate:
				private, err = fastReadAt(p.r, child.start, min64(child.end-child.start, fastMaxPrivateBytes))
			case ebmlFastVideoID:
				err = p.each(child.start, child.end, 4, func(vChild fastEBMLElement) error {
					var err error
					switch vChild.id {
					case ebmlFastPixelWidth:
						var v uint64
						if v, err = p.readUint(vChild); err == nil {
							width = int(min(v, 1<<20))
						}
					case ebmlFastPixelHeight:
						var v uint64
						if v, err = p.readUint(vChild); err == nil {
							height = int(min(v, 1<<20))
						}
					case ebmlFastColourID:
						err = p.each(vChild.start, vChild.end, 5, func(cChild fastEBMLElement) error {
							var err error
							switch cChild.id {
							case ebmlFastTransferID:
								var v uint64
								if v, err = p.readUint(cChild); err == nil && v != 2 {
									transfer = int(min(v, 255))
								}
							case ebmlFastPrimariesID:
								var v uint64
								if v, err = p.readUint(cChild); err == nil && v != 2 {
									primaries = int(min(v, 255))
								}
							case ebmlFastMasteringID:
								mastering = true
							case ebmlFastMaxCLLID:
								var v uint64
								if v, err = p.readUint(cChild); err == nil && v > 0 {
									mastering = true
								}
							}
							return err
						})
					}
					return err
				})
			case ebmlFastBlockAddMap:
				var kind uint64
				var extra []byte
				err = p.each(child.start, child.end, 4, func(bChild fastEBMLElement) error {
					var err error
					switch bChild.id {
					case ebmlFastBlockAddType:
						kind, err = p.readUint(bChild)
					case ebmlFastBlockAddExtra:
						extra, err = fastReadAt(p.r, bChild.start, min64(bChild.end-bChild.start, 64))
					}
					return err
				})
				if err == nil && (kind == fastBlockAddDVCC || kind == fastBlockAddDVVC || kind == fastBlockAddDVWC) {
					hasDV = true
					if len(extra) >= 3 {
						dvProfile = int(extra[2] >> 1)
					}
					if len(extra) >= 5 {
						dvCompat = int(extra[4] >> 4)
					}
				}
			}
			return err
		})
		if err != nil {
			return err
		}
		if trackType == 1 && !res.HasVideo {
			res.HasVideo = true
			res.SourceCodec = fastMatroskaCodec(codecID, private)
			res.SourceWidth = width
			res.SourceHeight = height
			res.SourceHDR = fastMatroskaHDR(transfer, primaries, mastering, hasDV, dvProfile, dvCompat)
		}
		return nil
	})
}

const fastMaxTagStrings = 64

var errFastTagLimit = errors.New("tag string limit reached")

func appendCappedTags(dst, src []string, limit int) []string {
	for _, v := range src {
		if len(dst) >= limit {
			break
		}
		if strings.TrimSpace(v) == "" {
			continue
		}
		dst = append(dst, v)
	}
	return dst
}

func (p *fastEBMLReader) parseTags(start, end int64) ([]string, bool) {
	sawTag := false
	var tags []string
	err := p.each(start, end, 2, func(tagEl fastEBMLElement) error {
		if tagEl.id != ebmlFastTagID {
			return nil
		}
		sawTag = true
		return p.each(tagEl.start, tagEl.end, 3, func(child fastEBMLElement) error {
			if child.id != ebmlFastSimpleTagID {
				return nil
			}
			var val string
			if err := p.each(child.start, child.end, 4, func(stChild fastEBMLElement) error {
				if stChild.id == ebmlFastTagStringID {
					var err error
					val, err = p.readString(stChild)
					return err
				}
				return nil
			}); err != nil {
				return err
			}
			if strings.TrimSpace(val) != "" {
				tags = append(tags, val)
				if len(tags) >= fastMaxTagStrings {
					return errFastTagLimit
				}
			}
			return nil
		})
	})
	if err != nil && !errors.Is(err, errFastTagLimit) {
		return nil, false
	}
	return tags, sawTag || start == end
}

func fastMatroskaCodec(codec string, private []byte) string {
	switch codec {
	case "V_AV1":
		return "av1"
	case "V_MPEGH/ISO/HEVC":
		return "hevc"
	case "V_MPEG4/ISO/AVC":
		return "h264"
	case "V_VP9":
		return "vp9"
	case "V_MPEG2":
		return "mpeg2"
	case "V_MS/VFW/FOURCC":
		if len(private) >= 20 {
			switch strings.ToUpper(string(private[16:20])) {
			case "AVC1", "H264", "X264":
				return "h264"
			case "HEVC", "HVC1", "H265", "HEV1":
				return "hevc"
			case "AV01":
				return "av1"
			case "VP90":
				return "vp9"
			case "MPG2":
				return "mpeg2"
			}
		}
	}
	return ""
}

func fastMatroskaHDR(transfer, primaries int, mastering, hasDV bool, dvProfile, dvCompat int) string {
	if hasDV {
		if dvProfile == 5 && ((transfer != 16 && transfer != 18) || (dvCompat != 1 && dvCompat != 6)) {
			return "dv5"
		}
		return "dv"
	}
	if transfer == 16 {
		return "hdr10"
	}
	if transfer == 18 {
		return "hlg"
	}
	if primaries == 9 {
		return "hdr10"
	}
	if mastering && (transfer == 0 || transfer == 2) {
		return "hdr10"
	}
	return ""
}

func (p *fastEBMLReader) each(start, end int64, depth int, visit func(fastEBMLElement) error) error {
	if depth > fastMaxDepth {
		return errors.New("ebml nesting depth exceeded")
	}
	for pos := start; pos < end; {
		el, next, err := p.readElement(pos, end, false)
		if err != nil {
			return err
		}
		if err := visit(el); err != nil {
			return err
		}
		pos = next
	}
	return nil
}

func (p *fastEBMLReader) readElement(pos, parentEnd int64, allowUnknown bool) (fastEBMLElement, int64, error) {
	p.elements++
	if p.elements > fastMaxElements {
		return fastEBMLElement{}, 0, errors.New("ebml element budget exceeded")
	}
	id, idLen, _, err := p.readVINT(pos, 4, true)
	if err != nil {
		return fastEBMLElement{}, 0, err
	}
	size, sizeLen, unknown, err := p.readVINT(pos+int64(idLen), 8, false)
	if err != nil {
		return fastEBMLElement{}, 0, err
	}
	payloadStart := pos + int64(idLen+sizeLen)
	if payloadStart > parentEnd {
		return fastEBMLElement{}, 0, errors.New("truncated ebml element")
	}
	if allowUnknown && (id == ebmlFastSegmentID || id == ebmlFastClusterID) && (unknown || size > uint64(parentEnd-payloadStart)) {
		return fastEBMLElement{id: id, start: payloadStart, end: parentEnd, unknown: true}, parentEnd, nil
	}
	if unknown || size > uint64(parentEnd-payloadStart) {
		return fastEBMLElement{}, 0, errors.New("invalid ebml element size")
	}
	end := payloadStart + int64(size)
	return fastEBMLElement{id: id, start: payloadStart, end: end}, end, nil
}

func (p *fastEBMLReader) readVINT(pos int64, maxLen int, keepMarker bool) (uint64, int, bool, error) {
	first, err := fastReadAt(p.r, pos, 1)
	if err != nil {
		return 0, 0, false, err
	}
	marker := byte(0x80)
	length := 1
	for length <= 8 && first[0]&marker == 0 {
		marker >>= 1
		length++
	}
	if marker == 0 || length > maxLen {
		return 0, 0, false, errors.New("invalid ebml vint")
	}
	raw, err := fastReadAt(p.r, pos, int64(length))
	if err != nil {
		return 0, 0, false, err
	}
	val := uint64(raw[0])
	if !keepMarker {
		val &= uint64(marker - 1)
	}
	for _, b := range raw[1:] {
		val = val<<8 | uint64(b)
	}
	unknown := false
	if !keepMarker {
		maxVal := uint64(1)<<(7*length) - 1
		unknown = val == maxVal
	}
	return val, length, unknown, nil
}

func (p *fastEBMLReader) readString(el fastEBMLElement) (string, error) {
	n := min64(el.end-el.start, fastMaxString)
	if n <= 0 {
		return "", nil
	}
	raw, err := fastReadAt(p.r, el.start, n)
	if err != nil {
		return "", err
	}
	s := string(raw)
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	return strings.ToValidUTF8(s, ""), nil
}

func (p *fastEBMLReader) readUint(el fastEBMLElement) (uint64, error) {
	n := el.end - el.start
	if n == 0 {
		return 0, nil
	}
	if n < 0 || n > 8 {
		return 0, errors.New("invalid ebml uint length")
	}
	raw, err := fastReadAt(p.r, el.start, n)
	if err != nil {
		return 0, err
	}
	var padded [8]byte
	copy(padded[8-len(raw):], raw)
	return binary.BigEndian.Uint64(padded[:]), nil
}

func fastReadAt(r io.ReaderAt, offset, length int64) ([]byte, error) {
	if offset < 0 || length < 0 || length > int64(^uint(0)>>1) {
		return nil, errors.New("invalid read range")
	}
	b := make([]byte, int(length))
	if length == 0 {
		return b, nil
	}
	n, err := r.ReadAt(b, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if n != len(b) {
		return nil, io.ErrUnexpectedEOF
	}
	return b, nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
