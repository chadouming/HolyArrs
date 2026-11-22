package transcribe

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func writeTestWAV(t *testing.T, path string, samples []int16) {
	t.Helper()
	dataSize := len(samples) * 2
	buf := make([]byte, wavHeaderSize+dataSize)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(36+dataSize))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], 1)
	binary.LittleEndian.PutUint16(buf[22:24], 1)
	binary.LittleEndian.PutUint32(buf[24:28], wavSampleRate)
	binary.LittleEndian.PutUint32(buf[28:32], wavBytesPerSec)
	binary.LittleEndian.PutUint16(buf[32:34], 2)
	binary.LittleEndian.PutUint16(buf[34:36], 16)
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(dataSize))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(buf[wavHeaderSize+i*2:], uint16(s))
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestParseAndSliceWAV(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake.wav")
	if err := os.WriteFile(fake, []byte("RIFF fake sound"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := parseWAV(fake); ok {
		t.Fatal("fake text file parsed as WAV")
	}

	// Wrong sample rate / channels must be rejected.
	bad := filepath.Join(dir, "bad.wav")
	writeTestWAV(t, bad, make([]int16, wavSampleRate))
	b, _ := os.ReadFile(bad)
	binary.LittleEndian.PutUint16(b[22:24], 2)
	_ = os.WriteFile(bad, b, 0o600)
	if _, ok := parseWAV(bad); ok {
		t.Fatal("stereo WAV parsed as mono 16kHz")
	}

	samples := make([]int16, wavSampleRate*5)
	for i := range samples {
		samples[i] = int16(i%2000 - 1000)
	}
	src := filepath.Join(dir, "src.wav")
	writeTestWAV(t, src, samples)
	info, ok := parseWAV(src)
	if !ok || info.dataOffset != wavHeaderSize || info.dataSize != int64(len(samples)*2) || info.seconds != 5 {
		t.Fatalf("parseWAV got %+v, ok=%v", info, ok)
	}

	// Slice [0.5s, 1.5s) with odd byte bounds to verify 2-byte alignment.
	dst := filepath.Join(dir, "slice.wav")
	if err := sliceWAV(src, info, 16001, 48001, dst); err != nil {
		t.Fatal(err)
	}
	sInfo, ok := parseWAV(dst)
	if !ok || sInfo.dataSize != 32000 || sInfo.seconds != 1 {
		t.Fatalf("slice info %+v, ok=%v", sInfo, ok)
	}
	srcBytes, _ := os.ReadFile(src)
	dstBytes, _ := os.ReadFile(dst)
	if !bytes.Equal(dstBytes[wavHeaderSize:], srcBytes[wavHeaderSize+16000:wavHeaderSize+48000]) {
		t.Fatal("sliced PCM bytes do not match source range")
	}
}

func TestFindSilenceSplits(t *testing.T) {
	dir := t.TempDir()

	// Short file (<180s) is never split.
	shortPath := filepath.Join(dir, "short.wav")
	writeTestWAV(t, shortPath, make([]int16, wavSampleRate*120))
	shortInfo, _ := parseWAV(shortPath)
	if got := findSilenceSplits(shortPath, shortInfo, 3); len(got) != 2 || got[0] != 0 || got[1] != shortInfo.dataSize {
		t.Fatalf("short splits %v", got)
	}

	// 200s audio: loud speech everywhere except a 1s quiet valley centered at 94s.
	const durSec = 200
	samples := make([]int16, wavSampleRate*durSec)
	for i := range samples {
		if i%2 == 0 {
			samples[i] = 10000
		} else {
			samples[i] = -10000
		}
	}
	valleyStart := int(93.5 * wavSampleRate)
	valleyEnd := int(94.5 * wavSampleRate)
	for i := valleyStart; i < valleyEnd; i++ {
		samples[i] = 0
	}
	wavPath := filepath.Join(dir, "film.wav")
	writeTestWAV(t, wavPath, samples)
	info, ok := parseWAV(wavPath)
	if !ok {
		t.Fatal("parseWAV failed")
	}
	splits := findSilenceSplits(wavPath, info, 2)
	if len(splits) != 3 || splits[0] != 0 || splits[2] != info.dataSize {
		t.Fatalf("splits %v", splits)
	}
	splitSec := float64(splits[1]) / float64(wavBytesPerSec)
	if splitSec < 93.5 || splitSec > 94.5 {
		t.Fatalf("split landed at %.2fs, want inside [93.5, 94.5]", splitSec)
	}

	// 240s audio split into 3 parts with valleys at 74s and 166s; all chunks >= 30s.
	samples3 := make([]int16, wavSampleRate*240)
	for i := range samples3 {
		if i%2 == 0 {
			samples3[i] = 9000
		} else {
			samples3[i] = -9000
		}
	}
	for _, center := range []float64{74, 166} {
		for i := int((center - 0.5) * wavSampleRate); i < int((center+0.5)*wavSampleRate); i++ {
			samples3[i] = 0
		}
	}
	wav3 := filepath.Join(dir, "film3.wav")
	writeTestWAV(t, wav3, samples3)
	info3, _ := parseWAV(wav3)
	splits3 := findSilenceSplits(wav3, info3, 3)
	if len(splits3) != 4 {
		t.Fatalf("splits3 %v", splits3)
	}
	s1 := float64(splits3[1]) / float64(wavBytesPerSec)
	s2 := float64(splits3[2]) / float64(wavBytesPerSec)
	if s1 < 73.5 || s1 > 74.5 || s2 < 165.5 || s2 > 166.5 {
		t.Fatalf("splits3 landed at %.2fs, %.2fs; want ~74s and ~166s", s1, s2)
	}
	for i := 1; i < len(splits3); i++ {
		if splits3[i]-splits3[i-1] < 30*wavBytesPerSec {
			t.Fatalf("chunk %d shorter than 30s: %d bytes", i-1, splits3[i]-splits3[i-1])
		}
	}
}
