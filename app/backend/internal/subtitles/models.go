package subtitles

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/builtin"
)

// ownPrefix starts the id of a model file the owner put in the models folder.
const ownPrefix = "file:"

// ownExtra is the memory an own model is taken to need on top of its size.
const ownExtra = 1 << 30

// Errors about models.
var (
	// ErrNoModel means the chosen model's file is not in the models folder.
	ErrNoModel = errors.New("the chosen model's file is not there")
	// ErrUnknownModel means the id names no model HolyArrs knows.
	ErrUnknownModel = errors.New("no such model")
	// ErrModelInUse means the built-in translator is set to use this model.
	ErrModelInUse = errors.New("this model is in use by the built-in translator")
)

// modelFile is a model the built-in translator can load.
type modelFile struct {
	ID, Name, Path string
	NeedBytes      uint64
	Form           string // how the model is asked; "" for an own file
}

// ownName returns the file name of an own-file id: a plain name ending in
// .gguf, with no folder in it.
func ownName(id string) (string, bool) {
	name, ok := strings.CutPrefix(id, ownPrefix)
	if !ok || len(name) <= len(".gguf") || !strings.HasSuffix(strings.ToLower(name), ".gguf") ||
		strings.ContainsAny(name, `/\`) || name != filepath.Base(name) || strings.HasPrefix(name, ".") || len(name) > 200 {
		return "", false
	}
	// A catalogue model's file is that model, never an own file: under an
	// own id it would lose its prompt form and slip past the in-use check.
	for _, m := range builtin.Catalogue {
		if strings.EqualFold(name, m.ID+".gguf") {
			return "", false
		}
	}
	return name, true
}

// validModelID reports whether id has the form of a model id: a catalogue
// model or an own file.
func validModelID(id string) bool {
	if _, ok := builtin.ByID(id); ok {
		return true
	}
	_, ok := ownName(id)
	return ok
}

// plainFile returns the size of the plain file at p; a link is not followed.
func plainFile(p string) (int64, bool) {
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return 0, false
	}
	return fi.Size(), true
}

// modelFor resolves a model id to its file in the models folder. A catalogue
// model's file must have the catalogue's size; an own file must be a plain
// file with something in it.
func (s *Service) modelFor(id string) (modelFile, error) {
	if s.ModelsDir == "" {
		return modelFile{}, fmt.Errorf("%w: %q", ErrNoModel, id)
	}
	if m, ok := builtin.ByID(id); ok {
		p := filepath.Join(s.ModelsDir, m.ID+".gguf")
		if size, ok := plainFile(p); !ok || size != m.Size {
			return modelFile{}, fmt.Errorf("%w: %s", ErrNoModel, m.Name)
		}
		return modelFile{ID: id, Name: m.Name, Path: p, NeedBytes: m.NeedBytes, Form: m.Form}, nil
	}
	name, ok := ownName(id)
	if !ok {
		return modelFile{}, fmt.Errorf("%w: %q", ErrNoModel, id)
	}
	p := filepath.Join(s.ModelsDir, name)
	size, ok := plainFile(p)
	if !ok || size == 0 {
		return modelFile{}, fmt.Errorf("%w: %s", ErrNoModel, name)
	}
	return modelFile{ID: id, Name: strings.TrimSuffix(name, filepath.Ext(name)), Path: p, NeedBytes: uint64(size) + ownExtra}, nil
}

// ModelView is one model as the settings page shows it.
type ModelView struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Need     uint64  `json:"need"`
	License  string  `json:"license"`
	Own      bool    `json:"own"`
	State    string  `json:"state"`    // present, downloading or missing
	Progress float64 `json:"progress"` // 0 to 1 while downloading
	Fits     bool    `json:"fits"`     // the machine has the memory it needs
	Chosen   bool    `json:"chosen"`
	// Kind is "" for a translation model, or what else the model is for:
	// speech (the transcriber's model) or vad (its voice detection).
	Kind string `json:"kind"`
}

// Models lists the catalogue's models, then the owner's own .gguf files in
// the models folder. A half-downloaded file is never a model.
func (s *Service) Models(ctx context.Context) []ModelView {
	chosen := readTranslator(ctx, s.Store.Read()).BuiltInModel
	chosenSpeechID := ""
	if sp, ok := s.speechModel(ctx); ok {
		if set := ReadSettings(ctx, s.Store.Read()); set.TranscribeModel == sp.ID {
			chosenSpeechID = sp.ID
		} else if _, err := s.modelFor(sp.ID); err == nil {
			chosenSpeechID = sp.ID
		}
	}
	total := builtin.TotalMemory()
	fits := func(need uint64) bool { return total == 0 || need <= total }
	out := []ModelView{}
	taken := map[string]bool{}
	for _, m := range builtin.Catalogue {
		taken[strings.ToLower(m.ID+".gguf")] = true
		isChosen := false
		switch m.Kind {
		case "":
			isChosen = m.ID == chosen
		case builtin.KindSpeech:
			isChosen = m.ID == chosenSpeechID
		}
		v := ModelView{ID: m.ID, Name: m.Name, Size: m.Size, Need: m.NeedBytes, License: m.License, State: "missing", Fits: fits(m.NeedBytes), Chosen: isChosen, Kind: m.Kind}
		if _, err := s.modelFor(m.ID); err == nil {
			v.State = "present"
		} else if p, ok := s.downloading(m.ID); ok {
			v.State, v.Progress = "downloading", p
		}
		out = append(out, v)
	}
	entries, _ := os.ReadDir(s.ModelsDir)
	var own []ModelView
	for _, e := range entries {
		id := ownPrefix + e.Name()
		if taken[strings.ToLower(e.Name())] {
			continue
		}
		m, err := s.modelFor(id)
		if err != nil {
			continue
		}
		own = append(own, ModelView{ID: id, Name: m.Name, Size: int64(m.NeedBytes - ownExtra), Need: m.NeedBytes, Own: true, State: "present", Fits: fits(m.NeedBytes), Chosen: id == chosen})
	}
	sort.Slice(own, func(i, j int) bool { return own[i].ID < own[j].ID })
	return append(out, own...)
}

// DeleteModel removes a model's file. The model the built-in translator is
// set to use cannot be removed while that translator is on.
func (s *Service) DeleteModel(ctx context.Context, id string) error {
	if !validModelID(id) {
		return ErrUnknownModel
	}
	if t := readTranslator(ctx, s.Store.Read()); t.Kind == TranslatorBuiltIn && t.BuiltInModel == id {
		return ErrModelInUse
	}
	if set := ReadSettings(ctx, s.Store.Read()); set.TranscribeMissing && set.TranscribeModel == id {
		return ErrModelInUse
	}
	name := ""
	if m, ok := builtin.ByID(id); ok {
		name = m.ID + ".gguf"
	} else {
		name, _ = ownName(id)
	}
	p := filepath.Join(s.ModelsDir, name)
	if _, ok := plainFile(p); !ok {
		return ErrUnknownModel
	}
	if m, ok := builtin.ByID(id); ok && (m.Kind == builtin.KindSpeech || m.Kind == builtin.KindVAD) {
		s.speech.stop()
	} else if s.Runners != nil {
		s.Runners.Stop() // one may still hold this file from an earlier call
	}
	return os.Remove(p)
}
