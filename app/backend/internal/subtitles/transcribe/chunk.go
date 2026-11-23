package transcribe

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
)

// Slot is one GPU device paired with its persistent speech server.
type Slot struct {
	Device      int
	Server      *Server
	Release     func()
	ShouldYield func() bool
}

func serverFields(o Options, lang, prompt string) map[string]string {
	beam, tempInc := "5", "0.4"
	if o.Fast {
		beam, tempInc = "1", "0.0"
	}
	fields := map[string]string{
		"language":                  lang,
		"response_format":           "verbose_json",
		"vad":                       "true",
		"token_timestamps":          "true",
		"no_language_probabilities": "true",
		"beam_size":                 beam,
		"best_of":                   beam,
		"temperature_inc":           tempInc,
	}
	if prompt != "" {
		fields["prompt"] = prompt
	}
	return fields
}

func runSingleServer(ctx context.Context, o Options, wav, lang, prompt string) ([]Cue, error) {
	var kept timeMap
	onLine := func(line string) {
		kept.add(line)
		if m := progressLine.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			o.tell(shareLanguage + (1-shareLanguage)*float64(min(n, 100))/100)
		}
	}
	body, err := o.Server.post(ctx, o, wav, serverFields(o, lang, prompt), onLine, o.Log)
	if err != nil {
		return nil, err
	}
	segs, err := serverSegments(body)
	if err != nil {
		return nil, err
	}
	var cues []Cue
	for _, seg := range segs {
		cues = append(cues, seg.lines(kept)...)
	}
	return cues, nil
}

func chunkCount(seconds float64, slotCount int) int {
	n := min(max(slotCount*2, int(seconds/180)), int(seconds/90))
	return min(n, 24)
}

// runChunked splits wav at quiet speech pauses and transcribes chunks across
// all slots using a work-stealing queue, then merges cues in strict order.
func runChunked(ctx context.Context, o Options, dir, wav string, info wavInfo, lang, prompt string) ([]Cue, error) {
	slots := append([]Slot{{Device: o.Device, Server: o.Server}}, o.ExtraSlots...)
	numChunks := chunkCount(info.seconds, len(slots))
	if numChunks < 2 {
		return runSingleServer(ctx, o, wav, lang, prompt)
	}
	splits := findSilenceSplits(wav, info, numChunks)
	actualChunks := len(splits) - 1
	if actualChunks < 2 {
		return runSingleServer(ctx, o, wav, lang, prompt)
	}

	type chunkJob struct {
		idx      int
		path     string
		offsetMS int64
	}
	jobs := make([]chunkJob, actualChunks)
	for i := 0; i < actualChunks; i++ {
		p := filepath.Join(dir, fmt.Sprintf("chunk_%d.wav", i))
		if err := sliceWAV(wav, info, splits[i], splits[i+1], p); err != nil {
			return nil, err
		}
		jobs[i] = chunkJob{
			idx:      i,
			path:     p,
			offsetMS: splits[i] * 1000 / wavBytesPerSec,
		}
	}

	jobCh := make(chan chunkJob, actualChunks)
	for _, j := range jobs {
		jobCh <- j
	}
	close(jobCh)

	chunkProg := make([]atomic.Int64, actualChunks)
	var progMu sync.Mutex
	var lastProg float64
	reportProgress := func() {
		if o.Progress == nil {
			return
		}
		var sum int64
		for i := range chunkProg {
			sum += chunkProg[i].Load()
		}
		avg := float64(sum) / float64(actualChunks*100)
		p := shareLanguage + (1-shareLanguage)*avg
		progMu.Lock()
		if p > lastProg {
			lastProg = p
			o.tell(p)
		}
		progMu.Unlock()
	}

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make([][]Cue, actualChunks)
	var errMu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	workers := min(len(slots), actualChunks)
	for w := workers; w < len(slots); w++ {
		if w > 0 && slots[w].Release != nil {
			slots[w].Release()
		}
	}
	for w := 0; w < workers; w++ {
		isExtra := w > 0
		slot := slots[w]
		wg.Add(1)
		go func() {
			defer wg.Done()
			if isExtra && slot.Release != nil {
				defer slot.Release()
			}
			slotOpts := o
			slotOpts.Device = slot.Device
			slotOpts.Server = slot.Server
			slotOpts.ExtraSlots = nil
			for {
				if isExtra && slot.ShouldYield != nil && slot.ShouldYield() {
					return
				}
				job, ok := <-jobCh
				if !ok {
					return
				}
				if wctx.Err() != nil {
					return
				}
				var kept timeMap
				onLine := func(line string) {
					kept.add(line)
					if m := progressLine.FindStringSubmatch(line); m != nil {
						n, _ := strconv.Atoi(m[1])
						chunkProg[job.idx].Store(int64(min(max(n, 0), 100)))
						reportProgress()
					}
				}
				body, err := slot.Server.post(wctx, slotOpts, job.path, serverFields(slotOpts, lang, prompt), onLine, slotOpts.Log)
				if err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					errMu.Unlock()
					return
				}
				segs, err := serverSegments(body)
				if err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					errMu.Unlock()
					return
				}
				var cues []Cue
				for _, seg := range segs {
					for _, c := range seg.lines(kept) {
						c.StartMS += job.offsetMS
						c.EndMS += job.offsetMS
						cues = append(cues, c)
					}
				}
				results[job.idx] = cues
				chunkProg[job.idx].Store(100)
				reportProgress()
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}

	var merged []Cue
	var prevEndMS int64
	for _, cues := range results {
		for _, c := range cues {
			if len(merged) > 0 && c.StartMS < prevEndMS {
				c.StartMS = prevEndMS
			}
			if c.EndMS <= c.StartMS {
				c.EndMS = c.StartMS + 400
			}
			prevEndMS = c.EndMS
			merged = append(merged, c)
		}
	}
	return merged, nil
}
