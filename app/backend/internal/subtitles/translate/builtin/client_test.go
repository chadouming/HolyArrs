package builtin

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate"
)

func batch() translate.Batch {
	return translate.Batch{From: "en", To: "fr", FromName: "English", ToName: "French", Kind: "movie",
		Lines: []translate.Line{{N: 1, Text: "Good morning."}, {N: 2, Text: "See you tomorrow."}}}
}

func TestClientTranslatesThroughTheRunner(t *testing.T) {
	r := newRunner(t)
	c := &Client{Runner: r, Opts: opts("a.gguf"), Name: "lab"}
	got, err := c.Translate(context.Background(), batch())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].N != 1 || got[0].Text != "T:Good morning." || got[1].N != 2 || got[1].Text != "T:See you tomorrow." {
		t.Fatalf("lines %+v", got)
	}
	if !r.Running() {
		t.Fatal("the runner stays for the next call")
	}
	r.mu.Lock()
	busy := r.busy
	r.mu.Unlock()
	if busy != 0 {
		t.Fatalf("%d calls still counted as in flight", busy)
	}
}

func TestClientMapsNoMemoryToRefused(t *testing.T) {
	if FreeMemory() == 0 {
		t.Skip("free memory is not known on this system")
	}
	o := opts("a.gguf")
	o.NeedBytes = math.MaxUint64 / 2
	c := &Client{Runner: newRunner(t), Opts: o, Name: "lab"}
	_, err := c.Translate(context.Background(), batch())
	if translate.KindOf(err) != translate.KindRefused || !strings.Contains(err.Error(), "GB") {
		t.Fatalf("err %v", err)
	}
}

func TestClientMapsStartFailureToDown(t *testing.T) {
	t.Setenv("FAKE_EXIT_AFTER", "0")
	c := &Client{Runner: newRunner(t), Opts: opts("a.gguf"), Name: "lab"}
	_, err := c.Translate(context.Background(), batch())
	if err == nil || translate.KindOf(err) != translate.KindDown {
		t.Fatalf("err %v", err)
	}
}

// A model with a prompt form of its own is asked that way: Hy-MT2 takes one
// user message and no system prompt, and repeats the source when asked the
// way other models are.
func TestHyMTFormAsksTheModelItsOwnWay(t *testing.T) {
	file := t.TempDir() + "/body"
	t.Setenv("FAKE_BODY_FILE", file)
	c := &Client{Runner: newRunner(t), Opts: opts("a.gguf"), Name: "lab", Form: FormHyMT}
	got, err := c.Translate(context.Background(), batch())
	if err != nil || len(got) != 2 || got[0].Text != "T:Good morning." {
		t.Fatalf("%+v %v", got, err)
	}
	raw, _ := os.ReadFile(file)
	var body struct {
		Messages []struct{ Role, Content string } `json:"messages"`
		TopK     int                              `json:"top_k"`
		TopP     float64                          `json:"top_p"`
		Temp     float64                          `json:"temperature"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 1 || body.Messages[0].Role != "user" || !strings.Contains(body.Messages[0].Content, "into French") ||
		!strings.Contains(body.Messages[0].Content, `{"lines":[{"n":1,"text":"Good morning."}`) {
		t.Fatalf("messages %+v", body.Messages)
	}
	if body.TopK != 20 || body.TopP != 0.6 || body.Temp != 0.7 {
		t.Fatalf("sampling %+v", body)
	}
	// The default form keeps HolyArrs's own system prompt.
	c.Form = ""
	if _, err := c.Translate(context.Background(), batch()); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(file)
	_ = json.Unmarshal(raw, &body)
	if len(body.Messages) != 2 || body.Messages[0].Role != "system" {
		t.Fatalf("default form messages %+v", body.Messages)
	}
}

// A model that sends the lines back as they came has translated nothing:
// that is a failed call, never a translation.
func TestClientRejectsEchoedLines(t *testing.T) {
	t.Setenv("FAKE_ECHO", "1")
	c := &Client{Runner: newRunner(t), Opts: opts("a.gguf"), Name: "lab"}
	b := batch()
	b.Lines = append(b.Lines, translate.Line{N: 3, Text: "Where are you going?"}, translate.Line{N: 4, Text: "I do not know yet."})
	_, err := c.Translate(context.Background(), b)
	if translate.KindOf(err) != translate.KindUnreadable || !strings.Contains(err.Error(), "without translating") {
		t.Fatalf("err %v", err)
	}
	// Names and numbers may rightly stay as they are: one such line is no echo.
	if echoed([]translate.Line{{N: 1, Text: "Groot."}, {N: 2, Text: "Good morning."}}, []translate.Line{{N: 1, Text: "Groot."}, {N: 2, Text: "Bonjour."}}) {
		t.Fatal("one unchanged line counted as an echo")
	}
	if echoed([]translate.Line{{N: 1, Text: "42"}, {N: 2, Text: "..."}}, []translate.Line{{N: 1, Text: "42"}, {N: 2, Text: "..."}}) {
		t.Fatal("lines with no words counted as an echo")
	}
	// Single words such as names, "OK." and "No." often read the same in both
	// languages: a chunk of them beside a translated line is no echo.
	short := []translate.Line{{N: 1, Text: "OK."}, {N: 2, Text: "No."}, {N: 3, Text: "Groot!"}, {N: 4, Text: "Peter?"}, {N: 5, Text: "See you tomorrow."}}
	back := []translate.Line{{N: 1, Text: "OK."}, {N: 2, Text: "No."}, {N: 3, Text: "Groot!"}, {N: 4, Text: "Peter?"}, {N: 5, Text: "Hasta mañana."}}
	if echoed(short, back) {
		t.Fatal("single words that stay as they are counted as an echo")
	}
}

// The no-think form keeps HolyArrs's own instruction and adds the switch that
// turns a model's reasoning off.
func TestNoThinkFormAsksWithThinkingOff(t *testing.T) {
	file := t.TempDir() + "/body"
	t.Setenv("FAKE_BODY_FILE", file)
	c := &Client{Runner: newRunner(t), Opts: opts("a.gguf"), Name: "lab", Form: FormNoThink}
	if _, err := c.Translate(context.Background(), batch()); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(file)
	var body struct {
		Messages []struct{ Role, Content string } `json:"messages"`
		Kwargs   map[string]any                   `json:"chat_template_kwargs"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Kwargs["enable_thinking"] != false {
		t.Fatalf("body %s", raw)
	}
}
