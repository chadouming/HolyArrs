package translate

import (
	"context"
	"strings"
	"sync"
)

// RunParts translates j like Run, pulling chunks dynamically across up to parts
// concurrent workers for a translator that takes several calls at once. A
// refusal or fatal error in one worker ends the others; the result then holds
// what was translated, with source text for the rest.
func RunParts(ctx context.Context, c Client, j Job, parts int) (Result, error) {
	n := len(j.Lines)
	if parts <= 1 || n > MaxLines {
		return Run(ctx, c, j)
	}
	start := j.Chunk
	if start <= 0 {
		start = DefaultChunk
	}
	start = min(start, MaxChunk)
	res := Result{Lines: make([]string, n), Done: make([]bool, n)}
	var pending []int
	for i, l := range j.Lines {
		res.Lines[i] = l.Text
		if strings.TrimSpace(l.Text) == "" {
			res.Done[i] = true
			continue
		}
		pending = append(pending, i)
	}
	res.Total = len(pending)
	initChunks := 0
	for cur := 0; cur < len(pending); {
		_, cur = nextChunk(j.Lines, pending, cur, start, j.MaxChars)
		initChunks++
	}
	workers := min(parts, initChunks)
	if workers <= 1 {
		return Run(ctx, c, j)
	}

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var mu sync.Mutex
	cond := sync.NewCond(&mu)
	stopWake := context.AfterFunc(wctx, func() {
		mu.Lock()
		cond.Broadcast()
		mu.Unlock()
	})
	defer stopWake()

	cursor := 0
	var again [][]int
	inFlight := 0
	size := start
	good := 0
	fruitless := 0
	tries := make([]int, n)
	var refusedErr, firstErr error
	aborted := false

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				for !aborted && wctx.Err() == nil && len(again) == 0 && cursor >= len(pending) && inFlight > 0 {
					cond.Wait()
				}
				if aborted || wctx.Err() != nil {
					mu.Unlock()
					return
				}
				var chunk []int
				if len(again) > 0 {
					chunk, again = again[0], again[1:]
				} else if cursor < len(pending) {
					chunk, cursor = nextChunk(j.Lines, pending, cursor, size, j.MaxChars)
				} else {
					mu.Unlock()
					return
				}
				inFlight++
				mu.Unlock()

				b := j.Batch
				b.Lines = make([]Line, len(chunk))
				for k, i := range chunk {
					b.Lines[k] = j.Lines[i]
				}
				localCalls := 0
				got, err := call(wctx, c, b, &localCalls)

				mu.Lock()
				inFlight--
				res.Calls += localCalls
				if err != nil {
					if aborted {
						if KindOf(err) == KindRefused && refusedErr == nil {
							refusedErr = err
						}
						cond.Broadcast()
						mu.Unlock()
						return
					}
					switch k := KindOf(err); k {
					case KindCut, KindUnreadable:
						if fruitless++; fruitless >= maxFruitless {
							if firstErr == nil {
								firstErr = err
							}
							aborted = true
							cancel()
							cond.Broadcast()
							mu.Unlock()
							return
						}
						size, good = max(1, len(chunk)/2), 0
						if k == KindCut && len(chunk) > 1 {
							again = append(splitChunks(chunk, size), again...)
						} else {
							again = append(retryChunks(chunk, tries, size), again...)
						}
						cond.Broadcast()
						mu.Unlock()
						continue
					case KindRefused:
						if refusedErr == nil {
							refusedErr = err
						}
						aborted = true
						cancel()
						cond.Broadcast()
						mu.Unlock()
						return
					default:
						if firstErr == nil {
							firstErr = err
						}
						aborted = true
						cancel()
						cond.Broadcast()
						mu.Unlock()
						return
					}
				}

				byN := map[int]int{}
				for k, i := range chunk {
					byN[j.Lines[i].N] = k
				}
				filled := make([]bool, len(chunk))
				before := res.Translated
				for _, l := range got {
					k, ok := byN[l.N]
					if !ok {
						res.Extra++
						continue
					}
					text := cleanReply(l.Text)
					if filled[k] || text == "" {
						continue
					}
					filled[k] = true
					i := chunk[k]
					if !res.Done[i] {
						res.Lines[i], res.Done[i] = text, true
						res.Translated++
					}
				}
				delta := res.Translated - before
				if aborted {
					cond.Broadcast()
					mu.Unlock()
					if j.Progress != nil && delta > 0 {
						j.Progress(delta)
					}
					return
				}
				var missing []int
				for k, ok := range filled {
					if !ok {
						missing = append(missing, chunk[k])
					}
				}
				if len(missing) < len(chunk) {
					fruitless = 0
				} else if fruitless++; fruitless >= maxFruitless {
					if firstErr == nil {
						firstErr = &Error{Kind: KindUnreadable, Msg: "the translator keeps sending back no usable lines"}
					}
					aborted = true
					cancel()
					cond.Broadcast()
					mu.Unlock()
					if j.Progress != nil && delta > 0 {
						j.Progress(delta)
					}
					return
				}
				if len(missing) == 0 {
					if good++; good >= 3 && size < start {
						size, good = min(start, size*2), 0
					}
				} else {
					good = 0
					again = append(retryChunks(missing, tries, max(1, len(chunk)/2)), again...)
				}
				cond.Broadcast()
				mu.Unlock()
				if j.Progress != nil && delta > 0 {
					j.Progress(delta)
				}
			}
		}()
	}
	wg.Wait()
	if refusedErr != nil {
		return res, refusedErr
	}
	if firstErr != nil {
		return res, firstErr
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	return res, nil
}
