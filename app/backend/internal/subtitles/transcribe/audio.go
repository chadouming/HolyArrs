package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Track is one audio track of a video.
type Track struct {
	Index    int     // among the file's audio tracks, from 0
	Language string  // the tag as written, lower case; "" when none
	Seconds  float64 // the file's length
}

// Errors that trying again will not change.
var (
	ErrNoAudio  = errors.New("the video has no audio track")
	ErrNoSpeech = errors.New("no speech was found")
	ErrLanguage = errors.New("the spoken language could not be told")
)

// Final reports whether err is one that trying again will not change.
func Final(err error) bool {
	return errors.Is(err, ErrNoAudio) || errors.Is(err, ErrNoSpeech) || errors.Is(err, ErrLanguage)
}

// Tracks lists a video's audio tracks with ffprobe.
func Tracks(ctx context.Context, p Programs, video string) ([]Track, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, _, err := run(ctx, nil, p.FFprobe, "-v", "error", "-select_streams", "a",
		"-show_entries", "stream=index:stream_tags=language:format=duration", "-of", "json", video)
	if err != nil {
		return nil, fmt.Errorf("the video's audio tracks could not be read: %w", err)
	}
	var doc struct {
		Streams []struct {
			Tags map[string]string `json:"tags"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return nil, fmt.Errorf("the video's audio tracks could not be read: %w", err)
	}
	if len(doc.Streams) == 0 {
		return nil, ErrNoAudio
	}
	secs, _ := strconv.ParseFloat(strings.TrimSpace(doc.Format.Duration), 64)
	tracks := make([]Track, len(doc.Streams))
	for i, s := range doc.Streams {
		tracks[i] = Track{Index: i, Language: strings.ToLower(strings.TrimSpace(s.Tags["language"])), Seconds: secs}
	}
	return tracks, nil
}

// Pick chooses the track to transcribe: the first tagged with a wanted
// language, in the order wanted; else the one tagged with original; else the
// first. The languages are compared as given, so the caller folds tags and
// wants to the same codes first.
func Pick(tracks []Track, wanted []string, original string) (Track, bool) {
	if len(tracks) == 0 {
		return Track{}, false
	}
	for _, w := range append(append([]string{}, wanted...), original) {
		if w == "" {
			continue
		}
		for _, t := range tracks {
			if t.Language == w {
				return t, true
			}
		}
	}
	return tracks[0], true
}
