package builtin

// Model is one model the built-in translator offers for download.
type Model struct {
	ID      string // also the file name in the models folder, with .gguf
	Name    string
	URL     string
	SHA256  string
	License string
	Size    int64
	// NeedBytes is the memory the runner holds with this model loaded.
	NeedBytes uint64
	// Kind says what the model is for: "" a translation model the runner
	// loads, KindSpeech a speech model and KindVAD a voice-detection model,
	// both for the transcriber.
	Kind string
	// Form is how the model is asked: "" for HolyArrs's own instruction, or
	// one of the Form names for a model that needs its own.
	Form string
}

// Catalogue lists the models offered, the default first. Each was run through
// the real runner and prompt before it was listed (see the design's trial):
// a model must answer with the numbered lines HolyArrs asks for.
var Catalogue = []Model{
	{
		ID:        "hy-mt2-1.8b-q8",
		Name:      "Hy-MT2 1.8B",
		URL:       "https://huggingface.co/tencent/Hy-MT2-1.8B-GGUF/resolve/a0c709d9fac510f2c807aa3af52872340dc37a4a/Hy-MT2-1.8B-Q8_0.gguf",
		SHA256:    "5c3fe0b1408a5ceb0143184ef247b11b579c525f4b02b060e6c851bb76fef1a4",
		License:   "Apache-2.0",
		Size:      1908528192,
		NeedBytes: 3 << 30,
		Form:      FormHyMT,
	},
	{
		ID:        "gemma-4-e4b-q4",
		Name:      "Gemma 4 E4B",
		URL:       "https://huggingface.co/ggml-org/gemma-4-E4B-it-GGUF/resolve/8903cb408c7df39cde2ed15fac397e6d7be92951/gemma-4-E4B-it-Q4_0.gguf",
		SHA256:    "a555b900214b477d8880e7832e0b8925e139b0159640036b09fe472b6f2097f2",
		License:   "Apache-2.0",
		Size:      4590807392,
		NeedBytes: 7 << 30,
		Form:      FormNoThink,
	},
	{
		ID:        "whisper-large-v3-turbo-q8_0",
		Name:      "Whisper large-v3-turbo (Q8_0)",
		Kind:      KindSpeech,
		URL:       "https://huggingface.co/ggerganov/whisper.cpp/resolve/5359861c739e955e79d9a303bcbc70fb988958b1/ggml-large-v3-turbo-q8_0.bin",
		SHA256:    "317eb69c11673c9de1e1f0d459b253999804ec71ac4c23c17ecf5fbe24e259a1",
		License:   "MIT",
		Size:      874188075,
		NeedBytes: 1600 << 20,
	},
	{
		ID:        "whisper-large-v3-turbo-q5_0",
		Name:      "Whisper large-v3-turbo (Q5_0)",
		Kind:      KindSpeech,
		URL:       "https://huggingface.co/ggerganov/whisper.cpp/resolve/5359861c739e955e79d9a303bcbc70fb988958b1/ggml-large-v3-turbo-q5_0.bin",
		SHA256:    "394221709cd5ad1f40c46e6031ca61bce88931e6e088c188294c6d5a55ffa7e2",
		License:   "MIT",
		Size:      574041195,
		NeedBytes: 1100 << 20,
	},
	{
		ID:        "whisper-large-v3-turbo",
		Name:      "Whisper large-v3-turbo (FP16)",
		Kind:      KindSpeech,
		URL:       "https://huggingface.co/ggerganov/whisper.cpp/resolve/5359861c739e955e79d9a303bcbc70fb988958b1/ggml-large-v3-turbo.bin",
		SHA256:    "1fc70f774d38eb169993ac391eea357ef47c88757ef72ee5943879b7e8e2bc69",
		License:   "MIT",
		Size:      1624555275,
		NeedBytes: 2500 << 20,
	},
	{
		ID:        "silero-vad-6.2.0",
		Name:      "Silero voice detection",
		Kind:      KindVAD,
		URL:       "https://huggingface.co/ggml-org/whisper-vad/resolve/9ffd54a1e1ee413ddf265af9913beaf518d1639b/ggml-silero-v6.2.0.bin",
		SHA256:    "2aa269b785eeb53a82983a20501ddf7c1d9c48e33ab63a41391ac6c9f7fb6987",
		License:   "MIT",
		Size:      885098,
		NeedBytes: 64 << 20,
	},
}

// Kinds of model that are not translation models.
const (
	KindSpeech = "speech"
	KindVAD    = "vad"
)

// ByKind finds the first catalogue model of a kind.
func ByKind(kind string) (Model, bool) {
	for _, m := range Catalogue {
		if m.Kind == kind {
			return m, true
		}
	}
	return Model{}, false
}

// AllByKind returns all catalogue models matching kind.
func AllByKind(kind string) []Model {
	var out []Model
	for _, m := range Catalogue {
		if m.Kind == kind {
			out = append(out, m)
		}
	}
	return out
}

// ByID finds a catalogue model.
func ByID(id string) (Model, bool) {
	for _, m := range Catalogue {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}
