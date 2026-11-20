package builtin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate"
)

// The built-in kind's own limits: a call on a CPU takes far longer than one
// to a service, and a smaller first chunk keeps each call inside the limit.
var (
	Timeout = 15 * time.Minute
	Chunk   = 20
)

// DefaultContext is the context size, in tokens, the runner is started with.
const DefaultContext = 8192

// GPUParallel is how many calls a runner on a graphics card takes at once.
// Measured on an Arc B580: eight at once translated about 2.7 times the lines
// of one, and sixteen fewer than eight.
const GPUParallel = 8

// GPUContext is the context, in tokens, of each slot of a runner on a
// graphics card: room for one chunk and its answer.
const GPUContext = 4096

// Target is one runner and what it is started with.
type Target struct {
	Runner *Runner
	Opts   Options
}

// Client translates through its runners, starting each when needed.
type Client struct {
	// Runner and Opts are the one runner of a Client with no Targets.
	Runner *Runner
	Opts   Options
	// Targets are the runners calls are spread over: one for each graphics
	// card in use.
	Targets []Target
	Name    string       // the model name sent in the chat call
	Form    string       // how the model is asked; see Model.Form
	HTTP    *http.Client // tests only

	mu       sync.Mutex
	busy     []int       // calls in flight on each target
	downTill []time.Time // until when a target that failed to start is passed over
}

func (c *Client) targets() []Target {
	if len(c.Targets) > 0 {
		return c.Targets
	}
	return []Target{{Runner: c.Runner, Opts: c.Opts}}
}

// Slots is how many calls the Client can take at once over all its runners.
func (c *Client) Slots() int {
	n := 0
	for _, t := range c.targets() {
		n += t.Opts.slots()
	}
	return n
}

// targetRest is how long a target whose runner would not start is passed over.
const targetRest = time.Minute

// take picks the target with the most free slots among those not in skip and
// counts a call on it. A target whose runner failed to start a moment ago is
// taken only when no other is left.
func (c *Client) take(skip map[int]bool) (int, Target, func(), bool) {
	ts := c.targets()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.busy) != len(ts) {
		c.busy, c.downTill = make([]int, len(ts)), make([]time.Time, len(ts))
	}
	best := -1
	better := func(i int) bool {
		if best < 0 {
			return true
		}
		di, db := time.Now().Before(c.downTill[i]), time.Now().Before(c.downTill[best])
		if di != db {
			return !di
		}
		return ts[i].Opts.slots()-c.busy[i] > ts[best].Opts.slots()-c.busy[best]
	}
	for i := range ts {
		if !skip[i] && better(i) {
			best = i
		}
	}
	if best < 0 {
		return 0, Target{}, func() {}, false
	}
	c.busy[best]++
	return best, ts[best], func() {
		c.mu.Lock()
		c.busy[best]--
		c.mu.Unlock()
	}, true
}

func (c *Client) rest(i int) {
	c.mu.Lock()
	c.downTill[i] = time.Now().Add(targetRest)
	c.mu.Unlock()
}

// Translate makes sure a runner is up, then makes the same chat call any
// other OpenAI-style translator gets. A runner that will not start is passed
// over for the next; the call fails only when none starts.
func (c *Client) Translate(ctx context.Context, b translate.Batch) ([]translate.Line, error) {
	skip := map[int]bool{}
	var last error
	for {
		i, t, done, ok := c.take(skip)
		if !ok {
			break
		}
		base, key, err := t.Runner.Ensure(ctx, t.Opts)
		if err != nil {
			done()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			c.rest(i)
			skip[i], last = true, err
			continue
		}
		o := &translate.OpenAI{URL: base, Key: key, Model: c.Name, HTTP: c.HTTP, Timeout: Timeout}
		switch c.Form {
		case FormHyMT:
			o.Ask, o.Sampling = askHyMT, samplingHyMT
		case FormNoThink:
			o.Sampling = samplingNoThink
		}
		got, err := o.Translate(ctx, b)
		t.Runner.Used()
		done()
		if err == nil && b.From != b.To && echoed(b.Lines, got) {
			return nil, &translate.Error{Kind: translate.KindUnreadable, Msg: "the model sent the lines back without translating them"}
		}
		return got, err
	}
	kind := translate.KindDown
	if errors.Is(last, ErrNoMemory) {
		kind = translate.KindRefused // waiting and asking again would not free memory
	}
	if last == nil {
		last = errors.New("no model runner to call")
	}
	return nil, &translate.Error{Kind: kind, Msg: last.Error()}
}

// FormHyMT is the way Tencent's Hy-MT2 models are asked, from their model
// card: one user message and no system prompt, with their own sampling.
// Asked the way other models are, they repeat the source for some languages.
const FormHyMT = "hy-mt"

// FormNoThink is for a model that reasons before it answers: it is asked
// HolyArrs's own way with its reasoning turned off, since the reasoning costs
// time and would otherwise end up in the answer.
const FormNoThink = "no-think"

var samplingNoThink = map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": false}}

var samplingHyMT = map[string]any{"temperature": 0.7, "top_p": 0.6, "top_k": 20, "repeat_penalty": 1.05}

func askHyMT(b translate.Batch, lines string) (system, user string) {
	return "", "Translate the user-facing text within the following JSON data into " + b.ToName + ". " +
		`Keep the structure, the keys and the numbers exactly as they are, keep line breaks and tags such as <i> and </i>, ` +
		`and translate only the values of "text". Only output the translated JSON without any additional explanation.` + "\n\n" + lines
}

// echoed reports whether the answer is the question again: most of the lines
// of two words or more came back exactly as they were sent. A name, a number
// or a single word such as "OK." may rightly stay as it is, so those are not
// counted, and a few unchanged lines are not an echo.
func echoed(sent, got []translate.Line) bool {
	src := make(map[int]string, len(sent))
	for _, l := range sent {
		src[l.N] = strings.TrimSpace(l.Text)
	}
	words, same := 0, 0
	for _, l := range got {
		s, ok := src[l.N]
		if !ok || !strings.ContainsFunc(s, unicode.IsLetter) || len(strings.Fields(s)) < 2 {
			continue
		}
		words++
		if strings.TrimSpace(l.Text) == s {
			same++
		}
	}
	return same >= 3 && same*2 > words
}
