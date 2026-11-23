package subtitles

import "time"

// MissingRow is one entry in the missing subtitles list.
type MissingRow struct {
	FileID    int64      `json:"file_id"`
	ItemID    int64      `json:"item_id"`
	TitleID   int64      `json:"title_id"`
	Title     string     `json:"title"`
	Kind      string     `json:"kind"`
	Season    *int       `json:"season,omitempty"`
	Episode   *int       `json:"episode,omitempty"`
	Language  string     `json:"language"`
	Forced    bool       `json:"forced"`
	HI        bool       `json:"hearing_impaired"`
	State     string     `json:"state"`
	Tries     int        `json:"tries"`
	NextTryAt *time.Time `json:"next_try_at,omitempty"`
	// HasText says the File has a subtitle that can be read, in any language:
	// one to translate from. Without one, only speech to text can make one.
	HasText bool `json:"has_text"`
	// Speech is how the File's last try at a subtitle from its audio ended
	// when nothing was saved: failed (tried again later) or final.
	Speech       string `json:"speech,omitempty"`
	SpeechReason string `json:"speech_reason,omitempty"`
}

// What a File with a missing subtitle needs, for MissingFilter.Need.
const (
	NeedSpeech = "speech" // no subtitle that can be read: speech to text
	NeedText   = "text"   // a subtitle in another language is there
)

// MissingCounts sums up the missing list for the Subtitles page.
type MissingCounts struct {
	Missing      int `json:"missing"`       // wanted subtitles that are not there
	Files        int `json:"files"`         // Files missing at least one
	NeedSpeech   int `json:"need_speech"`   // of those, Files with no subtitle that can be read
	CanTranslate int `json:"can_translate"` // missing subtitles on Files that have one in another language
	SpeechHeld   int `json:"speech_held"`   // Files whose subtitle from the audio will not be tried by itself
}

// MissingFilter narrows the missing subtitles list. An empty field matches all.
type MissingFilter struct {
	Language string // two-letter language code
	Kind     string // movie or episode
	TitleID  int64  // limits the list to one movie or show
	Need     string // NeedSpeech or NeedText
}

// How a saved subtitle was made, for DoneRow.How.
const (
	HowDownloaded = "downloaded" // from a subtitle site
	HowTranslated = "translated" // by HolyArrs's translator
	HowSpeech     = "speech"     // from the video's audio
)

// DoneRow is one subtitle HolyArrs saved beside a video.
type DoneRow struct {
	ID       int64     `json:"id"`
	FileID   int64     `json:"file_id"`
	TitleID  int64     `json:"title_id"`
	Title    string    `json:"title"`
	Kind     string    `json:"kind"`
	Season   *int      `json:"season,omitempty"`
	Episode  *int      `json:"episode,omitempty"`
	Language string    `json:"language"`
	Forced   bool      `json:"forced"`
	HI       bool      `json:"hearing_impaired"`
	How      string    `json:"how"`
	By       string    `json:"by,omitempty"` // the site, the translator or the speech model
	SavedAt  time.Time `json:"saved_at"`
}

// DoneCounts is how many of the subtitles now beside the videos HolyArrs made each way.
type DoneCounts struct {
	Downloaded int `json:"downloaded"`
	Translated int `json:"translated"`
	Speech     int `json:"speech"`
}

// MissingTitle is a movie or show missing at least one subtitle.
type MissingTitle struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}
