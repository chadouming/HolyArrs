package transcribe

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
)

const (
	wavSampleRate  = 16000
	wavBytesPerSec = 32000 // 16000 Hz, 1 channel, 16-bit PCM
	wavHeaderSize  = 44
)

type wavInfo struct {
	dataOffset int64
	dataSize   int64
	seconds    float64
}

// parseWAV checks that path is a 16 kHz mono 16-bit PCM WAV file and locates
// its PCM data payload. Non-WAV files return false so callers can fall back.
func parseWAV(path string) (wavInfo, bool) {
	f, err := os.Open(path)
	if err != nil {
		return wavInfo{}, false
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return wavInfo{}, false
	}
	fileSize := fi.Size()
	if fileSize < wavHeaderSize {
		return wavInfo{}, false
	}

	var riff [12]byte
	if _, err := io.ReadFull(f, riff[:]); err != nil {
		return wavInfo{}, false
	}
	if string(riff[0:4]) != "RIFF" || string(riff[8:12]) != "WAVE" {
		return wavInfo{}, false
	}

	var hasFmt, hasData bool
	var info wavInfo
	pos := int64(12)
	var hdr [8]byte
	for pos+8 <= fileSize {
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return wavInfo{}, false
		}
		if _, err := io.ReadFull(f, hdr[:]); err != nil {
			break
		}
		id := string(hdr[0:4])
		size := int64(binary.LittleEndian.Uint32(hdr[4:8]))
		payloadPos := pos + 8

		switch id {
		case "fmt ":
			if size < 16 || payloadPos+16 > fileSize {
				return wavInfo{}, false
			}
			var fmtBuf [16]byte
			if _, err := io.ReadFull(f, fmtBuf[:]); err != nil {
				return wavInfo{}, false
			}
			audioFormat := binary.LittleEndian.Uint16(fmtBuf[0:2])
			channels := binary.LittleEndian.Uint16(fmtBuf[2:4])
			sampleRate := binary.LittleEndian.Uint32(fmtBuf[4:8])
			bitsPerSample := binary.LittleEndian.Uint16(fmtBuf[14:16])
			if audioFormat != 1 || channels != 1 || sampleRate != wavSampleRate || bitsPerSample != 16 {
				return wavInfo{}, false
			}
			hasFmt = true
		case "data":
			dataSize := size
			if payloadPos+dataSize > fileSize {
				dataSize = max(fileSize-payloadPos, 0)
			}
			dataSize &^= 1
			info.dataOffset = payloadPos
			info.dataSize = dataSize
			hasData = true
		}

		// RIFF chunks are padded to an even byte boundary.
		pos = payloadPos + size + (size & 1)
	}

	if !hasFmt || !hasData || info.dataSize <= 0 {
		return wavInfo{}, false
	}
	info.seconds = float64(info.dataSize) / float64(wavBytesPerSec)
	return info, true
}

// sliceWAV writes bytes [startByte, endByte) of src's PCM data to dst with a
// fresh 44-byte RIFF/WAVE header.
func sliceWAV(src string, info wavInfo, startByte, endByte int64, dst string) error {
	startByte = max(startByte, 0) &^ 1
	endByte = min(endByte, info.dataSize) &^ 1
	if endByte < startByte {
		endByte = startByte
	}
	sliceLen := endByte - startByte

	sf, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sf.Close()

	df, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer df.Close()

	var hdr [wavHeaderSize]byte
	copy(hdr[0:4], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(36+sliceLen))
	copy(hdr[8:12], "WAVE")
	copy(hdr[12:16], "fmt ")
	binary.LittleEndian.PutUint32(hdr[16:20], 16)
	binary.LittleEndian.PutUint16(hdr[20:22], 1)
	binary.LittleEndian.PutUint16(hdr[22:24], 1)
	binary.LittleEndian.PutUint32(hdr[24:28], wavSampleRate)
	binary.LittleEndian.PutUint32(hdr[28:32], wavBytesPerSec)
	binary.LittleEndian.PutUint16(hdr[32:34], 2)
	binary.LittleEndian.PutUint16(hdr[34:36], 16)
	copy(hdr[36:40], "data")
	binary.LittleEndian.PutUint32(hdr[40:44], uint32(sliceLen))

	if _, err := df.Write(hdr[:]); err != nil {
		return err
	}
	sr := io.NewSectionReader(sf, info.dataOffset+startByte, sliceLen)
	if _, err := io.Copy(df, sr); err != nil {
		return fmt.Errorf("writing wav slice: %w", err)
	}
	return df.Close()
}

// findSilenceSplits picks targetParts+1 byte offsets [0, split1, ..., dataSize]
// relative to dataOffset, placing each interior boundary at the midpoint of
// the quietest 300ms valley within +/-20s of the nominal split point.
func findSilenceSplits(src string, info wavInfo, targetParts int) []int64 {
	const (
		frameBytes    = 640 // 20ms at 32000 B/s (320 int16 samples)
		smoothFrames  = 15  // 300ms rolling average
		windowBytes   = 20 * wavBytesPerSec
		minChunkBytes = 30 * wavBytesPerSec
	)
	if targetParts <= 1 || info.seconds < 180 {
		return []int64{0, info.dataSize}
	}
	maxParts := int(info.dataSize / minChunkBytes)
	if maxParts < 2 {
		return []int64{0, info.dataSize}
	}
	if targetParts > maxParts {
		targetParts = maxParts
	}

	f, err := os.Open(src)
	if err != nil {
		return []int64{0, info.dataSize}
	}
	defer f.Close()

	splits := make([]int64, 0, targetParts+1)
	splits = append(splits, 0)

	for k := 1; k < targetParts; k++ {
		prev := splits[len(splits)-1]
		nominal := (int64(k) * info.dataSize / int64(targetParts)) &^ 1
		minAllowed := (prev + minChunkBytes) &^ 1
		maxAllowed := (info.dataSize - int64(targetParts-k)*minChunkBytes) &^ 1
		lo := max(nominal-windowBytes, minAllowed) &^ 1
		hi := min(nominal+windowBytes, maxAllowed) &^ 1

		split := min(max(nominal, minAllowed), maxAllowed) &^ 1
		if hi-lo >= frameBytes*smoothFrames {
			buf := make([]byte, hi-lo)
			if _, err := io.ReadFull(io.NewSectionReader(f, info.dataOffset+lo, hi-lo), buf); err == nil {
				nFrames := len(buf) / frameBytes
				rms := make([]float64, nFrames)
				for i := 0; i < nFrames; i++ {
					off := i * frameBytes
					var sumSq float64
					for j := 0; j < frameBytes; j += 2 {
						s := float64(int16(binary.LittleEndian.Uint16(buf[off+j:])))
						sumSq += s * s
					}
					rms[i] = math.Sqrt(sumSq / float64(frameBytes/2))
				}
				if nFrames >= smoothFrames {
					var winSum float64
					for i := 0; i < smoothFrames; i++ {
						winSum += rms[i]
					}
					bestSum := winSum
					bestIdx := 0
					for i := smoothFrames; i < nFrames; i++ {
						winSum += rms[i] - rms[i-smoothFrames]
						if winSum < bestSum {
							bestSum = winSum
							bestIdx = i - smoothFrames + 1
						}
					}
					midFrame := bestIdx + smoothFrames/2
					cand := (lo + int64(midFrame)*frameBytes + frameBytes/2) &^ 1
					split = min(max(cand, minAllowed), maxAllowed) &^ 1
				}
			}
		}
		splits = append(splits, split)
	}
	splits = append(splits, info.dataSize)
	return splits
}
