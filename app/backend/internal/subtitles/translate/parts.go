package translate

import (
	"context"
	"sync"
)

// RunParts translates j like Run, cut into up to parts runs of lines that are
// translated at the same time, for a translator that takes several calls at
// once. Each part is a Run of its own, so the checks and retries are Run's. A
// part is never shorter than one chunk. A refusal in one part ends the others;
// the result then holds what was translated, with source text for the rest.
func RunParts(ctx context.Context, c Client, j Job, parts int) (Result, error) {
	n := len(j.Lines)
	chunk := j.Chunk
	if chunk <= 0 {
		chunk = DefaultChunk
	}
	chunk = min(chunk, MaxChunk)
	parts = min(parts, (n+chunk-1)/chunk)
	if parts <= 1 || n > MaxLines {
		return Run(ctx, c, j)
	}
	// Whole chunks to every part, so no part ends on a short call it need not make.
	per := ((n+parts-1)/parts + chunk - 1) / chunk * chunk
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type piece struct {
		from int
		res  Result
		err  error
	}
	var pieces []*piece
	var wg sync.WaitGroup
	for from := 0; from < n; from += per {
		p := &piece{from: from}
		pieces = append(pieces, p)
		sub := j
		sub.Lines = j.Lines[from:min(from+per, n)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.res, p.err = Run(ctx, c, sub)
			if p.err != nil && KindOf(p.err) == KindRefused {
				cancel() // the others would be refused the same way
			}
		}()
	}
	wg.Wait()
	out := Result{Lines: make([]string, n), Done: make([]bool, n)}
	var first, refused error
	for _, p := range pieces {
		end := min(p.from+per, n)
		for i := p.from; i < end; i++ {
			k := i - p.from
			if k < len(p.res.Lines) {
				out.Lines[i], out.Done[i] = p.res.Lines[k], p.res.Done[k]
			} else {
				out.Lines[i] = j.Lines[i].Text
			}
		}
		out.Translated += p.res.Translated
		out.Total += p.res.Total
		out.Calls += p.res.Calls
		out.Extra += p.res.Extra
		switch {
		case p.err == nil:
		case KindOf(p.err) == KindRefused && refused == nil:
			refused = p.err
		case first == nil:
			first = p.err
		}
	}
	if refused != nil {
		return out, refused
	}
	return out, first
}
