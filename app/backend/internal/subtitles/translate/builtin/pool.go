package builtin

import "sync"

// Pool keeps one Runner for each graphics card, and one for the CPU, so a
// model can run on several cards at once.
type Pool struct {
	log func(line string)

	mu      sync.Mutex
	runners map[string]*Runner
}

// NewPool returns a Pool whose runners pass their lines to log.
func NewPool(log func(line string)) *Pool {
	return &Pool{log: log, runners: map[string]*Runner{}}
}

// Runner returns the runner for a device ("Vulkan0"), or for the CPU when
// device is empty. It is made on first use and kept.
func (p *Pool) Runner(device string) *Runner {
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.runners[device]
	if r == nil {
		r = NewRunner(p.log)
		p.runners[device] = r
	}
	return r
}

func (p *Pool) all() []*Runner {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*Runner, 0, len(p.runners))
	for _, r := range p.runners {
		out = append(out, r)
	}
	return out
}

// Running reports whether any runner process is there.
func (p *Pool) Running() bool {
	for _, r := range p.all() {
		if r.Running() {
			return true
		}
	}
	return false
}

// RunningOn reports whether the runner for device has a loaded process.
func (p *Pool) RunningOn(device string) bool {
	p.mu.Lock()
	r := p.runners[device]
	p.mu.Unlock()
	return r != nil && r.Resident()
}

// ActiveOn reports whether the runner for device is actively serving calls.
func (p *Pool) ActiveOn(device string) bool {
	p.mu.Lock()
	r := p.runners[device]
	p.mu.Unlock()
	return r != nil && r.Active()
}

// StopIdle stops the runner for device if it exists and is idle.
func (p *Pool) StopIdle(device string) {
	p.mu.Lock()
	r := p.runners[device]
	p.mu.Unlock()
	if r != nil {
		r.StopIfIdle()
	}
}

// StopIdleAll stops every idle runner in the pool.
func (p *Pool) StopIdleAll() {
	for _, r := range p.all() {
		r.StopIfIdle()
	}
}

// Stop ends every runner process and waits until they are gone.
func (p *Pool) Stop() {
	var wg sync.WaitGroup
	for _, r := range p.all() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Stop()
		}()
	}
	wg.Wait()
}
