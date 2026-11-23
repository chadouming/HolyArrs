package subtitles

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	domain "github.com/HolyArrs/holyarrs/app/backend/internal/core/model"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/transcribe"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/builtin"
)

// Kinds of translator.
const (
	TranslatorOpenAI  = "openai"  // any server with the OpenAI chat API: cloud services, Ollama, LM Studio
	TranslatorLingarr = "lingarr" // a Lingarr server
	TranslatorBuiltIn = "builtin" // a model run inside HolyArrs's own container
)

// The settings rows of the translator. The key lives in the vault; its row
// holds only a reference.
const (
	trGroup    = "what_to_pick"
	trKind     = "subtitle_translator_kind"
	trURL      = "subtitle_translator_url"
	trModel    = "subtitle_translator_model"
	trKey      = "subtitle_translator_key"
	trBuiltIn  = "subtitle_translate_built_in"
	trOwnModel = "subtitle_translator_builtin_model"
	trThreads  = "subtitle_translator_threads"
	trGPU      = "subtitle_translator_gpu"
	trMaxModel = 200
	trMaxKey   = 1000
)

// Translator is the owner's own translator. The key is never sent back:
// HasKey only says one is kept.
type Translator struct {
	Kind  string `json:"kind"` // "" (off), openai or lingarr
	URL   string `json:"url"`
	Model string `json:"model"`
	// HasKey reports that a key is kept in the vault.
	HasKey bool `json:"has_key"`
	// BuiltInAuto lets the timed search translate text tracks inside the video
	// without asking. Off, they are translated only by hand.
	BuiltInAuto bool `json:"built_in_auto"`
	// BuiltInModel is the model the built-in translator loads: a model id.
	BuiltInModel string `json:"builtin_model"`
	// Threads is how many CPU threads the built-in translator uses; 0 for the default.
	Threads int `json:"threads"`
	// GPU is the graphics card the built-in translator runs on, as the
	// runner names it ("Vulkan0"); empty for the CPU.
	GPU string `json:"gpu"`
	// BuiltInAvailable reports that this image holds the model runner.
	BuiltInAvailable bool `json:"builtin_available"`

	builtInName  string // the chosen model's name, when its file is there
	builtInReady bool
}

// Ready reports whether the translator can be called.
func (t Translator) Ready() bool {
	switch t.Kind {
	case TranslatorOpenAI:
		return t.URL != "" && t.Model != ""
	case TranslatorLingarr:
		return t.URL != ""
	case TranslatorBuiltIn:
		return t.builtInReady
	}
	return false
}

// Name is how saved subtitles and Events name the translator: the model, or
// Lingarr.
func (t Translator) Name() string {
	switch t.Kind {
	case TranslatorLingarr:
		return "Lingarr"
	case TranslatorBuiltIn:
		return t.builtInName
	}
	return t.Model
}

// TranslatorInput changes the translator. Fields left out keep their value;
// an empty key keeps the stored one and ClearKey removes it.
type TranslatorInput struct {
	Kind        string `json:"kind"`
	URL         string `json:"url"`
	Model       string `json:"model"`
	Key         string `json:"key"`
	ClearKey    bool   `json:"clear_key"`
	BuiltInAuto bool   `json:"built_in_auto"`
	// BuiltInModel and Threads belong to the built-in kind.
	BuiltInModel string `json:"builtin_model"`
	Threads      int    `json:"threads"`
	GPU          string `json:"gpu"`
}

// Errors from the translator settings.
var (
	ErrNoTranslator = errors.New("no translator is set up: add one in Settings > Subtitles")
	ErrKeyElsewhere = errors.New("the translator address moved to another host: give the key again or clear it")
	ErrNoRunner     = errors.New("the built-in translator needs the HolyArrs image that holds the model runner")
)

// readTranslator reads the translator settings, without looking in the
// vault. Odd values read as off.
func readTranslator(ctx context.Context, db *sql.DB) Translator {
	get := func(k string) string { return database.SettingOr(ctx, db, trGroup, k, "") }
	t := Translator{Kind: get(trKind), URL: get(trURL), Model: get(trModel)}
	if t.Kind != TranslatorOpenAI && t.Kind != TranslatorLingarr && t.Kind != TranslatorBuiltIn {
		t.Kind = ""
	}
	if t.BuiltInModel = get(trOwnModel); !validModelID(t.BuiltInModel) {
		t.BuiltInModel = ""
	}
	if t.GPU = get(trGPU); t.GPU != GPUAll && !builtin.DeviceID(t.GPU) {
		t.GPU = ""
	}
	if t.Threads, _ = strconv.Atoi(get(trThreads)); t.Threads < 0 || t.Threads > runtime.NumCPU() {
		t.Threads = 0
	}
	if translate.CheckAddress(t.URL) != nil {
		t.URL = ""
	}
	t.BuiltInAuto, _ = strconv.ParseBool(get(trBuiltIn))
	return t
}

// builtIn fills in what the built-in kind needs to know about this machine:
// whether the runner is in this image and whether the chosen model is there.
func (s *Service) builtIn(t *Translator) (modelFile, error) {
	if s.Runners != nil {
		_, t.BuiltInAvailable = builtin.Find()
	}
	m, err := s.modelFor(t.BuiltInModel)
	if err == nil {
		t.builtInName = m.Name
		if cat, ok := builtin.ByID(t.BuiltInModel); ok {
			t.builtInName, _, _, _, _, _ = resolveCatalogueDisplay(s.ModelsDir, cat, transcribe.UsesOpenVINO(), builtin.UsesOpenVINO(), transcribe.OpenVINOPreferFP16())
		}
	}
	t.builtInReady = t.BuiltInAvailable && err == nil
	return m, err
}

// Translator reads the translator settings and whether a key is kept.
func (s *Service) Translator(ctx context.Context) Translator {
	t := readTranslator(ctx, s.Store.Read())
	_, _ = s.builtIn(&t)
	key, err := s.translatorKey(ctx)
	t.HasKey = err == nil && key != ""
	return t
}

// check cleans an input and refuses odd values.
func (in *TranslatorInput) check() error {
	in.Kind, in.URL, in.Model, in.Key = strings.TrimSpace(in.Kind), strings.TrimSpace(in.URL), strings.TrimSpace(in.Model), strings.TrimSpace(in.Key)
	switch in.Kind {
	case "", TranslatorOpenAI, TranslatorLingarr, TranslatorBuiltIn:
	default:
		return fmt.Errorf("kind must be %s, %s, %s or empty for off", TranslatorOpenAI, TranslatorLingarr, TranslatorBuiltIn)
	}
	in.BuiltInModel = strings.TrimSpace(in.BuiltInModel)
	if in.BuiltInModel != "" && !validModelID(in.BuiltInModel) {
		return errors.New("the built-in translator does not know that model")
	}
	if m, ok := builtin.ByID(in.BuiltInModel); ok && m.Kind != "" {
		return errors.New("that model is not a translation model")
	}
	if in.Kind == TranslatorBuiltIn && in.BuiltInModel == "" {
		return errors.New("the built-in translator needs a model")
	}
	if in.GPU = strings.TrimSpace(in.GPU); in.GPU != "" && in.GPU != GPUAll && !builtin.DeviceID(in.GPU) {
		return errors.New("the built-in translator does not know that graphics card")
	}
	if in.Threads < 0 || in.Threads > runtime.NumCPU() {
		return fmt.Errorf("threads must be between 0 (the default) and %d", runtime.NumCPU())
	}
	if in.Kind != "" && in.Kind != TranslatorBuiltIn && in.URL == "" {
		return errors.New("the translator needs its address")
	}
	if in.URL != "" {
		if err := translate.CheckAddress(in.URL); err != nil {
			return err
		}
	}
	if in.Kind == TranslatorOpenAI && in.Model == "" {
		return errors.New("the translator needs a model name")
	}
	if len(in.Model) > trMaxModel || strings.ContainsAny(in.Model, "\r\n") {
		return errors.New("the model name is too long")
	}
	if len(in.Key) > trMaxKey || strings.ContainsAny(in.Key, " \r\n") {
		return errors.New("the key is too long or holds spaces")
	}
	return nil
}

// SaveTranslator stores the translator and keeps its key in the vault. A key
// never follows the translator to another host: moving the address with a
// key kept needs the key again or ClearKey.
func (s *Service) SaveTranslator(ctx context.Context, in TranslatorInput, by domain.Actor) (Translator, error) {
	if err := in.check(); err != nil {
		return Translator{}, err
	}
	cur := s.Translator(ctx)
	if in.Kind == TranslatorBuiltIn {
		// The built-in kind has no address, model name or key of its own:
		// those of the other kinds stay as they are for a switch back.
		in.URL, in.Model, in.Key, in.ClearKey = cur.URL, cur.Model, "", false
	}
	if in.URL == "" && in.Key == "" && cur.HasKey {
		in.ClearKey = true // with no address left, a kept key has nowhere to go
	}
	if cur.HasKey && in.Key == "" && !in.ClearKey && originOf(in.URL) != originOf(cur.URL) {
		return cur, ErrKeyElsewhere
	}
	if (in.Key != "" || in.ClearKey) && s.Vault == nil {
		return cur, ErrNoVault
	}
	err := s.Store.Write(ctx, func(tx *sql.Tx) error {
		ts := time.Now()
		for k, v := range map[string]string{trKind: in.Kind, trURL: in.URL, trModel: in.Model, trBuiltIn: strconv.FormatBool(in.BuiltInAuto),
			trOwnModel: in.BuiltInModel, trThreads: strconv.Itoa(in.Threads), trGPU: in.GPU} {
			if err := database.PutSetting(tx, trGroup, k, v, by, ts); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return cur, err
	}
	switch {
	case in.Key != "":
		err = s.Vault.SetSetting(ctx, trGroup, trKey, in.Key, by)
	case in.ClearKey:
		err = s.Vault.SetSetting(ctx, trGroup, trKey, "", by)
	}
	return s.Translator(ctx), err
}

// translatorKey reads the key from the vault; "" when none is kept.
func (s *Service) translatorKey(ctx context.Context) (string, error) {
	if s.Vault == nil || database.SettingOr(ctx, s.Store.Read(), trGroup, trKey, "") == "" {
		return "", nil
	}
	return s.Vault.Setting(ctx, trGroup, trKey)
}

// translatorClient builds the client for the stored translator.
func (s *Service) translatorClient(ctx context.Context) (translate.Client, Translator, error) {
	t := readTranslator(ctx, s.Store.Read())
	if t.Kind == TranslatorBuiltIn {
		m, err := s.builtIn(&t)
		switch {
		case !t.BuiltInAvailable:
			return nil, t, ErrNoRunner
		case err != nil:
			return nil, t, err
		}
		bin, _ := builtin.Find()
		threads := t.Threads
		if threads == 0 {
			threads = builtin.DefaultThreads()
		}
		cpu := builtin.Options{Binary: bin, ModelPath: m.Path, Threads: threads, Context: builtin.DefaultContext, NeedBytes: m.NeedBytes,
			NoThink: m.Form == builtin.FormNoThink}
		// The cards in use: the one named, or with "all" every card the
		// runner lists; none of them leaves the model on the CPU.
		var cards []string
		switch {
		case t.GPU == GPUAll:
			for _, d := range s.Devices(ctx) {
				cards = append(cards, d.ID)
			}
		case t.GPU != "":
			cards = []string{t.GPU}
		}
		devs := s.Devices(ctx)
		speechPerSlot := speechSlotVRAMMB(s.activeSpeechNeedBytes(ctx))
		c := &builtin.Client{Name: m.Name, Form: m.Form, HTTP: s.TranslateHTTP}
		for _, id := range cards {
			// On a graphics card the model sits in the card's memory and the
			// runner takes several calls at once, each with a context of its own.
			totalMB := deviceMemoryMB(devs, id)
			usableMB := usableVRAMMB(totalMB)
			var extMB, activeSpeech, runningSpeech int
			if match := deviceNumber.FindStringSubmatch(id); match != nil {
				if cardNum, err := strconv.Atoi(match[1]); err == nil {
					extMB = s.externalVRAMMB(ctx, cardNum)
					activeSpeech = s.speech.activeOnCard(cardNum)
					runningSpeech = s.speech.runningOnCard(cardNum)
					if runningSpeech > activeSpeech && needsVRAMRoom(totalMB, translatorEstVRAMMB(m.NeedBytes, 8), extMB+runningSpeech*speechPerSlot) {
						s.speech.stopIdleCard(cardNum)
						runningSpeech = s.speech.runningOnCard(cardNum)
					}
				}
			}
			availMB := 0
			if totalMB > 0 {
				availMB = max(usableMB-extMB-max(activeSpeech, runningSpeech)*speechPerSlot, 1024)
			}
			o := cpu
			o.GPU, o.NeedBytes, o.CacheDir = id, min(o.NeedBytes, gpuNeed), filepath.Join(s.ModelsDir, ".cache")
			o.Parallel = builtin.GPUParallelFor(availMB, m.NeedBytes)
			o.Context = builtin.GPUContext
			o.FlashAttn = !builtin.LlamaNoFlashAttn()
			o.QuantKV = o.FlashAttn
			c.Targets = append(c.Targets, builtin.Target{Runner: s.Runners.Runner(id), Opts: o})
		}
		if len(c.Targets) == 0 {
			c.Targets = []builtin.Target{{Runner: s.Runners.Runner(""), Opts: cpu}}
		}
		return c, t, nil
	}
	if !t.Ready() {
		return nil, t, ErrNoTranslator
	}
	key, err := s.translatorKey(ctx)
	if err != nil {
		return nil, t, fmt.Errorf("the translator key cannot be read: %w", err)
	}
	if t.Kind == TranslatorLingarr {
		return &translate.Lingarr{URL: t.URL, Key: key, HTTP: s.TranslateHTTP}, t, nil
	}
	return &translate.OpenAI{URL: t.URL, Key: key, Model: t.Model, HTTP: s.TranslateHTTP}, t, nil
}

// GPUAll is the graphics card setting that uses every card the runner lists,
// one runner on each.
const GPUAll = "all"

// slotsOf is how many calls c takes at once: several for the built-in kind on
// graphics cards, one for every other translator.
func slotsOf(c translate.Client) int {
	if b, ok := c.(*builtin.Client); ok {
		return b.Slots()
	}
	return 1
}

func translateWorkersFromDevices(devs []builtin.Device) int {
	if n := len(cardNumbers(GPUAll, devs)); n > 0 {
		return min(max(n*2, 2), 4)
	}
	return 1
}

// TranslatePoolWorkers returns the job queue worker count for PoolTranslate.
// It scales with detected GPU cards (2 per card, capped at 4) so built-in GPU
// translation jobs can keep up to 24 slots fed across MKV extraction, tail
// waves, retries and SRT saves while keeping llama-server warm; otherwise it
// returns 1.
func TranslatePoolWorkers(ctx context.Context, _ *database.Store) int {
	var devs []builtin.Device
	if bin, ok := builtin.Find(); ok {
		devs, _ = builtin.Devices(ctx, bin)
	}
	return translateWorkersFromDevices(devs)
}

// gpuNeed is the least free memory asked for with the model on a graphics
// card. The runner was seen to hold more than this at its peak, but how much
// of that the card's driver maps differs from card to card, so the check
// only refuses a machine that is nearly out of memory.
const gpuNeed = 1 << 30

// devicesFor is how long a list of graphics cards is kept.
const devicesFor = time.Minute

func cloneDevices(d []builtin.Device) []builtin.Device {
	out := make([]builtin.Device, len(d))
	copy(out, d)
	return out
}

func (s *Service) refreshDevices(bin string) {
	d, err := builtin.Devices(context.Background(), bin)
	s.mu.Lock()
	if err == nil {
		if d == nil {
			d = []builtin.Device{}
		}
		s.devices = d
		s.devicesAt = time.Now()
	} else {
		if s.devices == nil {
			s.devices = []builtin.Device{}
		}
		s.devicesAt = time.Now()
	}
	s.devicesRefreshing = false
	s.mu.Unlock()
}

// Devices lists the graphics cards the runner in this image can use; none
// when there is no runner or the container was given no card.
func (s *Service) Devices(ctx context.Context) []builtin.Device {
	if s.Runners == nil {
		return []builtin.Device{}
	}
	bin, ok := builtin.Find()
	if !ok {
		return []builtin.Device{}
	}
	for {
		s.mu.Lock()
		if s.devices != nil {
			if time.Since(s.devicesAt) >= devicesFor && !s.devicesRefreshing {
				s.devicesRefreshing = true
				go s.refreshDevices(bin)
			}
			d := cloneDevices(s.devices)
			s.mu.Unlock()
			return d
		}
		if wait := s.devicesWait; wait != nil {
			s.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return []builtin.Device{}
			}
		}
		ch := make(chan struct{})
		s.devicesWait = ch
		s.mu.Unlock()

		d, err := builtin.Devices(ctx, bin)
		if err != nil || d == nil {
			d = []builtin.Device{}
		}
		s.mu.Lock()
		if ctx.Err() == nil {
			s.devices, s.devicesAt = d, time.Now()
		}
		s.devicesWait = nil
		close(ch)
		s.mu.Unlock()
		return cloneDevices(d)
	}
}

// chunkFor is how many lines the first calls to tr carry; 0 for the default.
func chunkFor(tr Translator) int {
	if tr.Kind == TranslatorBuiltIn {
		return builtin.Chunk
	}
	return 0
}

// TranslatorTest is how a translator test went, in plain words.
type TranslatorTest struct {
	OK      bool     `json:"ok"`
	Message string   `json:"message"`
	Models  []string `json:"models"`
}

// TestTranslator lists the server's models where it can, and translates two
// short lines to show the address, key and model work together.
func (s *Service) TestTranslator(ctx context.Context) TranslatorTest {
	res := TranslatorTest{Models: []string{}}
	c, t, err := s.translatorClient(ctx)
	if err != nil {
		res.Message = err.Error()
		return res
	}
	if o, ok := c.(*translate.OpenAI); ok {
		if ms, err := o.Models(ctx); err == nil {
			res.Models = ms
		}
	}
	b := translate.Batch{From: "en", To: "es", FromName: "English", ToName: "Spanish", Kind: "movie",
		Lines: []translate.Line{{N: 1, Text: "Good morning."}, {N: 2, Text: "See you tomorrow."}}}
	got, err := c.Translate(ctx, b)
	if err != nil {
		res.Message = translatorWords(err)
		return res
	}
	n := 0
	for _, l := range got {
		if (l.N == 1 || l.N == 2) && strings.TrimSpace(l.Text) != "" {
			n++
		}
	}
	if n < 2 {
		res.Message = "The translator answered, but not with the two numbered test lines. Try another model."
		return res
	}
	res.OK, res.Message = true, fmt.Sprintf("The translator answered: %s translated both test lines.", t.Name())
	return res
}

// translatorWords explains a translator failure in plain words.
func translatorWords(err error) string {
	var te *translate.Error
	if !errors.As(err, &te) {
		return "The translator could not be reached (" + err.Error() + ")."
	}
	switch te.Kind {
	case translate.KindRefused:
		return "The translator refused the call (" + te.Msg + "). Check the address, key and model."
	case translate.KindBusy:
		return "The translator is busy or failing (" + te.Msg + "). Try again later."
	case translate.KindCut:
		return "The translator did not answer in time."
	case translate.KindUnreadable:
		return "The translator answered, but HolyArrs could not read the answer (" + te.Msg + ")."
	}
	return "The translator could not be reached (" + te.Msg + ")."
}
