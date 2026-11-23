package translate

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// slowClient answers every line and notes how many calls ran at once.
type slowClient struct {
	now, most atomic.Int32
	mu        sync.Mutex
	firstN    []int // the first line number of every call
	fail      func(b Batch) error
}

func (c *slowClient) Translate(_ context.Context, b Batch) ([]Line, error) {
	n := c.now.Add(1)
	defer c.now.Add(-1)
	for {
		m := c.most.Load()
		if n <= m || c.most.CompareAndSwap(m, n) {
			break
		}
	}
	c.mu.Lock()
	c.firstN = append(c.firstN, b.Lines[0].N)
	c.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	if c.fail != nil {
		if err := c.fail(b); err != nil {
			return nil, err
		}
	}
	out := make([]Line, len(b.Lines))
	for i, l := range b.Lines {
		out[i] = Line{N: l.N, Text: "T:" + l.Text}
	}
	return out, nil
}

func jobOf(n, chunk int) Job {
	j := Job{Batch: Batch{From: "en", To: "fr"}, Chunk: chunk}
	for i := 0; i < n; i++ {
		j.Lines = append(j.Lines, Line{N: i + 1, Text: fmt.Sprintf("line %d", i+1)})
	}
	return j
}

// A file is cut into parts that are translated at the same time, and comes
// back whole and in order.
func TestRunPartsTranslatesPartsAtOnce(t *testing.T) {
	c := &slowClient{}
	res, err := RunParts(context.Background(), c, jobOf(400, 20), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) != 400 || res.Translated != 400 || res.Total != 400 || res.Partial() || res.Calls != 20 {
		t.Fatalf("result: %d lines, %d of %d translated, %d calls", len(res.Lines), res.Translated, res.Total, res.Calls)
	}
	for i, l := range res.Lines {
		if l != fmt.Sprintf("T:line %d", i+1) || !res.Done[i] {
			t.Fatalf("line %d is %q", i+1, l)
		}
	}
	if m := c.most.Load(); m < 2 || m > 4 {
		t.Fatalf("%d calls at once, want 2 to 4", m)
	}
}

// A short file is not cut finer than one chunk a part, and one part is the plain run.
func TestRunPartsKeepsWholeChunks(t *testing.T) {
	c := &slowClient{}
	res, err := RunParts(context.Background(), c, jobOf(30, 20), 8)
	if err != nil || res.Translated != 30 {
		t.Fatalf("%+v %v", res.Translated, err)
	}
	if m := c.most.Load(); m > 2 {
		t.Fatalf("30 lines in chunks of 20 ran %d at once", m)
	}
	c = &slowClient{}
	if res, err := RunParts(context.Background(), c, jobOf(100, 20), 1); err != nil || res.Translated != 100 || c.most.Load() != 1 {
		t.Fatalf("one part: %d translated, %d at once, %v", res.Translated, c.most.Load(), err)
	}
	if res, err := RunParts(context.Background(), c, jobOf(0, 20), 4); err != nil || len(res.Lines) != 0 {
		t.Fatalf("empty: %+v %v", res, err)
	}
}

// A refusal in one part ends the others and is what the run reports; what was
// translated before it is kept.
func TestRunPartsStopsOnARefusal(t *testing.T) {
	c := &slowClient{fail: func(b Batch) error {
		if b.Lines[0].N > 300 {
			return &Error{Kind: KindRefused, Msg: "no"}
		}
		return nil
	}}
	res, err := RunParts(context.Background(), c, jobOf(400, 20), 4)
	if KindOf(err) != KindRefused {
		t.Fatalf("err %v", err)
	}
	if len(res.Lines) != 400 || res.Lines[399] != "line 400" || res.Done[399] {
		t.Fatalf("the refused part should keep its source text: %q done %v", res.Lines[399], res.Done[399])
	}
	if !res.Partial() || res.Total != 400 {
		t.Fatalf("partial %v total %d", res.Partial(), res.Total)
	}
}

func TestRunPartsFillsAllPartsOnUnevenChunkCounts(t *testing.T) {
	c := &slowClient{}
	res, err := RunParts(context.Background(), c, jobOf(350, 20), 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) != 350 || res.Translated != 350 || res.Total != 350 || res.Partial() || res.Calls != 18 {
		t.Fatalf("result: %d lines, %d of %d translated, %d calls", len(res.Lines), res.Translated, res.Total, res.Calls)
	}
	for i, l := range res.Lines {
		if l != fmt.Sprintf("T:line %d", i+1) || !res.Done[i] {
			t.Fatalf("line %d is %q", i+1, l)
		}
	}
	if m := c.most.Load(); m < 7 || m > 8 {
		t.Fatalf("%d calls at once, want 7 or 8", m)
	}
}

type slotSpeedClient struct {
	slots     chan time.Duration
	slowCalls atomic.Int32
	fastCalls atomic.Int32
}

func (c *slotSpeedClient) Translate(_ context.Context, b Batch) ([]Line, error) {
	d := <-c.slots
	defer func() { c.slots <- d }()
	if d >= 50*time.Millisecond {
		c.slowCalls.Add(1)
	} else {
		c.fastCalls.Add(1)
	}
	time.Sleep(d)
	out := make([]Line, len(b.Lines))
	for i, l := range b.Lines {
		out[i] = Line{N: l.N, Text: "T:" + l.Text}
	}
	return out, nil
}

// A fast worker steals remaining chunks when another worker is 10x slower,
// finishing well ahead of static sharding while keeping all lines in order.
func TestRunPartsDynamicWorkStealing(t *testing.T) {
	slots := make(chan time.Duration, 2)
	slots <- 100 * time.Millisecond
	slots <- 5 * time.Millisecond
	c := &slotSpeedClient{slots: slots}

	start := time.Now()
	res, err := RunParts(context.Background(), c, jobOf(240, 20), 2)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if res.Translated != 240 || res.Total != 240 || res.Partial() {
		t.Fatalf("translated %d of %d (partial=%v)", res.Translated, res.Total, res.Partial())
	}
	for i, l := range res.Lines {
		if l != fmt.Sprintf("T:line %d", i+1) || !res.Done[i] {
			t.Fatalf("line %d = %q (done=%v)", i+1, l, res.Done[i])
		}
	}
	// Static sharding of 12 chunks across 2 workers would take >= 600ms on the slow slot.
	if elapsed >= 450*time.Millisecond {
		t.Fatalf("elapsed %s, expected fast worker to steal work from slow slot", elapsed)
	}
	if c.fastCalls.Load() <= c.slowCalls.Load()*2 {
		t.Fatalf("fastCalls=%d slowCalls=%d, expected fast slot to steal most chunks", c.fastCalls.Load(), c.slowCalls.Load())
	}
}

type cutClient struct {
	limit int
}

func (c cutClient) Translate(_ context.Context, b Batch) ([]Line, error) {
	if len(b.Lines) > c.limit {
		return nil, &Error{Kind: KindCut, Msg: "truncated"}
	}
	out := make([]Line, len(b.Lines))
	for i, l := range b.Lines {
		out[i] = Line{N: l.N, Text: "T:" + l.Text}
	}
	return out, nil
}

// KindCut splits oversized chunks dynamically across workers and translates all lines.
func TestRunPartsHandlesKindCutSplitting(t *testing.T) {
	res, err := RunParts(context.Background(), cutClient{limit: 5}, jobOf(100, 20), 4)
	if err != nil {
		t.Fatal(err)
	}
	if res.Translated != 100 || res.Total != 100 || res.Partial() {
		t.Fatalf("translated %d of %d (partial=%v)", res.Translated, res.Total, res.Partial())
	}
	for i, l := range res.Lines {
		if l != fmt.Sprintf("T:line %d", i+1) || !res.Done[i] {
			t.Fatalf("line %d = %q (done=%v)", i+1, l, res.Done[i])
		}
	}
}
