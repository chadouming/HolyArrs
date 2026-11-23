package subtitles

import (
	"context"
	"database/sql"
	"log"
	"sort"
	"strconv"
	"sync"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/transcribe"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/builtin"
)

// speechPlaces hands out the places a transcript can run in: so many on each
// graphics card, or one on the CPU. Each place keeps its own speech server,
// with the model loaded, for the files that come through it.
type speechPlaces struct {
	mu      sync.Mutex
	busy    map[int]bool
	servers map[int]*transcribe.Server
	cardOf  map[int]int
	freed   chan struct{} // closed and replaced when a place is given back
	active  int           // in-flight Transcribe calls
	primary int           // slots currently held as primary via take
	waiters int           // goroutines currently waiting inside take
}

// enter marks an in-flight Transcribe call and returns an idempotent cleanup.
func (p *speechPlaces) enter() func() {
	p.mu.Lock()
	p.active++
	p.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			p.active--
			p.mu.Unlock()
		})
	}
}

// hasDemand reports whether another call is waiting for or needs a primary slot.
func (p *speechPlaces) hasDemand() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waiters > 0 || p.active > p.primary
}

// take waits for a free place and returns the card it is on (-1 for the
// CPU), its speech server, and what gives the place back. cards are the
// cards' numbers; perCard is how many transcripts run on each at once.
func (p *speechPlaces) take(ctx context.Context, cards []int, perCard int) (int, *transcribe.Server, func(), error) {
	n := 1
	if len(cards) > 0 {
		n = len(cards) * max(perCard, 1)
	}
	waiting := false
	for {
		p.mu.Lock()
		if waiting {
			p.waiters--
			waiting = false
		}
		if p.busy == nil {
			p.busy, p.servers, p.cardOf, p.freed = map[int]bool{}, map[int]*transcribe.Server{}, map[int]int{}, make(chan struct{})
		}
		for i := 0; i < n; i++ {
			if p.busy[i] {
				continue
			}
			p.busy[i] = true
			p.primary++
			srv := p.servers[i]
			if srv == nil {
				srv = transcribe.NewServer(func(line string) { log.Print(line) })
				p.servers[i] = srv
			}
			dev := -1
			if len(cards) > 0 {
				// Places alternate over the cards, so two files go to two cards
				// before a card gets its second.
				dev = cards[i%len(cards)]
			}
			p.cardOf[i] = dev
			p.mu.Unlock()
			var once sync.Once
			return dev, srv, func() {
				once.Do(func() {
					p.mu.Lock()
					p.busy[i] = false
					p.primary--
					close(p.freed)
					p.freed = make(chan struct{})
					p.mu.Unlock()
				})
			}, nil
		}
		p.waiters++
		waiting = true
		wait := p.freed
		p.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			p.mu.Lock()
			p.waiters--
			p.mu.Unlock()
			return 0, nil, nil, ctx.Err()
		}
	}
}

// takeExtra grabs up to maxExtra currently idle GPU slots without waiting,
// reserving primary slots for active jobs and capping to a fair share.
func (p *speechPlaces) takeExtra(cards []int, perCard int, maxExtra int) ([]transcribe.Slot, func()) {
	if len(cards) == 0 || maxExtra <= 0 {
		return nil, func() {}
	}
	n := len(cards) * max(perCard, 1)
	p.mu.Lock()
	if p.busy == nil {
		p.busy, p.servers, p.cardOf, p.freed = map[int]bool{}, map[int]*transcribe.Server{}, map[int]int{}, make(chan struct{})
	}
	free := 0
	for i := 0; i < n; i++ {
		if !p.busy[i] {
			free++
		}
	}
	needPrimary := max(p.active-p.primary, 0)
	activeJobs := max(p.active, 1)
	fairExtra := max(n/activeJobs-1, 0)
	limit := min(maxExtra, min(max(free-needPrimary, 0), fairExtra))
	if limit <= 0 {
		p.mu.Unlock()
		return nil, func() {}
	}
	var releases []func()
	var slots []transcribe.Slot
	for i := 0; i < n && len(slots) < limit; i++ {
		if p.busy[i] {
			continue
		}
		p.busy[i] = true
		srv := p.servers[i]
		if srv == nil {
			srv = transcribe.NewServer(func(line string) { log.Print(line) })
			p.servers[i] = srv
		}
		dev := cards[i%len(cards)]
		p.cardOf[i] = dev
		idx := i
		var oneOnce sync.Once
		releaseOne := func() {
			oneOnce.Do(func() {
				p.mu.Lock()
				p.busy[idx] = false
				close(p.freed)
				p.freed = make(chan struct{})
				p.mu.Unlock()
			})
		}
		releases = append(releases, releaseOne)
		slots = append(slots, transcribe.Slot{
			Device:      dev,
			Server:      srv,
			Release:     releaseOne,
			ShouldYield: p.hasDemand,
		})
	}
	p.mu.Unlock()
	var allOnce sync.Once
	release := func() {
		allOnce.Do(func() {
			for _, r := range releases {
				r()
			}
		})
	}
	return slots, release
}

// runningOnCard reports how many speech servers on card have a running process.
func (p *speechPlaces) runningOnCard(card int) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	count := 0
	for i, srv := range p.servers {
		if p.cardOf[i] == card && srv != nil && srv.Running() {
			count++
		}
	}
	return count
}

// activeOnCard reports how many speech slots on card are currently busy.
func (p *speechPlaces) activeOnCard(card int) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	count := 0
	for i, busy := range p.busy {
		if busy && p.cardOf[i] == card {
			count++
		}
	}
	return count
}

// stopIdleExcess stops idle speech servers on card beyond keepRunning total running slots.
func (p *speechPlaces) stopIdleExcess(card int, keepRunning int) {
	if keepRunning < 0 {
		keepRunning = 0
	}
	p.mu.Lock()
	busyRunning := 0
	var idleIndices []int
	for i, srv := range p.servers {
		if p.cardOf[i] != card || srv == nil || !srv.Running() {
			continue
		}
		if p.busy[i] {
			busyRunning++
		} else {
			idleIndices = append(idleIndices, i)
		}
	}
	sort.Ints(idleIndices)
	keepIdle := max(keepRunning-busyRunning, 0)
	var servers []*transcribe.Server
	for idx := keepIdle; idx < len(idleIndices); idx++ {
		servers = append(servers, p.servers[idleIndices[idx]])
	}
	p.mu.Unlock()
	for _, srv := range servers {
		srv.StopIfIdle()
	}
}

// stopIdleCard stops any idle speech server assigned to card.
func (p *speechPlaces) stopIdleCard(card int) {
	p.mu.Lock()
	var servers []*transcribe.Server
	for i, srv := range p.servers {
		if p.cardOf[i] == card && !p.busy[i] && srv != nil {
			servers = append(servers, srv)
		}
	}
	p.mu.Unlock()
	for _, srv := range servers {
		srv.StopIfIdle()
	}
}

// stopIdleAll stops every idle speech server.
func (p *speechPlaces) stopIdleAll() {
	p.mu.Lock()
	var servers []*transcribe.Server
	for i, srv := range p.servers {
		if !p.busy[i] && srv != nil {
			servers = append(servers, srv)
		}
	}
	p.mu.Unlock()
	for _, srv := range servers {
		srv.StopIfIdle()
	}
}

// YieldGPU stops idle whisper and llama runners to free VRAM before a transcode.
func (s *Service) YieldGPU() {
	if s == nil {
		return
	}
	s.speech.stopIdleAll()
	if s.Runners != nil {
		s.Runners.StopIdleAll()
	}
}

// YieldGPUForVRAM stops idle whisper or llama runners on card (or all detected
// cards when card < 0) only when resident models plus reserveMB exceed usable VRAM.
func (s *Service) YieldGPUForVRAM(ctx context.Context, card int, reserveMB int) {
	if s == nil {
		return
	}
	devs := s.Devices(ctx)
	var cards []int
	if card >= 0 {
		cards = []int{card}
	} else {
		cards = cardNumbers(GPUAll, devs)
		if len(cards) == 0 {
			s.YieldGPU()
			return
		}
	}
	speechSlotMB := speechSlotVRAMMB(s.activeSpeechNeedBytes(ctx))
	llamaMB := s.activeTranslatorEstVRAMMB(ctx)
	for _, c := range cards {
		totalMB := cardMemoryMB(devs, c)
		usableMB := usableVRAMMB(totalMB)
		devID := cardDeviceID(devs, c)
		resident := func() int {
			sp := s.speech.runningOnCard(c) * speechSlotMB
			ll := 0
			if s.Runners != nil && s.Runners.RunningOn(devID) {
				ll = llamaMB
			}
			return sp + ll
		}
		if totalMB <= 0 {
			s.speech.stopIdleCard(c)
			if s.Runners != nil {
				s.Runners.StopIdle(devID)
			}
			continue
		}
		if resident()+reserveMB <= usableMB {
			continue
		}
		llamaRes := 0
		if s.Runners != nil && s.Runners.RunningOn(devID) {
			llamaRes = llamaMB
		}
		roomForSpeech := usableMB - reserveMB - llamaRes
		if roomForSpeech >= speechSlotMB {
			s.speech.stopIdleExcess(c, roomForSpeech/speechSlotMB)
		} else {
			s.speech.stopIdleCard(c)
		}
		if resident()+reserveMB > usableMB {
			s.speech.stopIdleCard(c)
		}
		if resident()+reserveMB > usableMB && s.Runners != nil {
			s.Runners.StopIdle(devID)
		}
	}
}

// stop ends every speech server.
func (p *speechPlaces) stop() {
	p.mu.Lock()
	servers := p.servers
	p.servers = map[int]*transcribe.Server{}
	p.cardOf = map[int]int{}
	p.mu.Unlock()
	for _, s := range servers {
		s.Stop()
	}
}

// cardNumbers are the numbers of the graphics cards transcripts run on: the
// one chosen for the built-in translator, every card with "All cards", and
// none for the CPU.
func cardNumbers(gpu string, devs []builtin.Device) []int {
	var out []int
	num := func(id string) (int, bool) {
		m := deviceNumber.FindStringSubmatch(id)
		if m == nil {
			return 0, false
		}
		n, _ := strconv.Atoi(m[1])
		return n, true
	}
	if gpu == GPUAll {
		for _, d := range devs {
			if n, ok := num(d.ID); ok {
				out = append(out, n)
			}
		}
		return out
	}
	if n, ok := num(gpu); ok {
		out = append(out, n)
	}
	return out
}

// speechCards are the cards transcripts run on now.
func (s *Service) speechCards(ctx context.Context) []int {
	gpu := readTranslator(ctx, s.Store.Read()).GPU
	if gpu == "" {
		return nil
	}
	return cardNumbers(gpu, s.Devices(ctx))
}

// StopRunners ends the translator's model runners and the speech servers.
func (s *Service) StopRunners() {
	if s.Runners != nil {
		s.Runners.Stop()
	}
	s.speech.stop()
}

// SpeechWorkers is how many transcripts can run at once with the settings
// and cards as they are: what the job queue gives the transcripts' pool at
// start. It is one on the CPU.
func SpeechWorkers(ctx context.Context, st *database.Store) int {
	db := st.Read()
	if readTranslator(ctx, db).GPU == "" {
		return 1
	}
	var devs []builtin.Device
	if bin, ok := builtin.Find(); ok {
		devs, _ = builtin.Devices(ctx, bin)
	}
	return speechWorkersFromDevices(ctx, db, devs)
}

func speechWorkersFromDevices(ctx context.Context, db *sql.DB, devs []builtin.Device) int {
	gpu := readTranslator(ctx, db).GPU
	if gpu == "" {
		return 1
	}
	cards := cardNumbers(gpu, devs)
	if len(cards) == 0 {
		return 1
	}
	set := ReadSettings(ctx, db)
	var needBytes uint64
	if set.TranscribeModel != "" {
		if m, ok := builtin.ByID(set.TranscribeModel); ok && m.Kind == builtin.KindSpeech {
			needBytes = m.NeedBytes
		}
	}
	if needBytes == 0 {
		if m, ok := builtin.ByKind(builtin.KindSpeech); ok {
			needBytes = m.NeedBytes
		}
	}
	atOnce := effectiveTranscribeAtOnce(set.TranscribeAtOnce, cards, devs, needBytes)
	return len(cards) * atOnce
}

// SpeechPoolWorkers returns the job queue worker ceiling for PoolTranscribe.
// It spawns enough workers for the maximum concurrent GPU slots across all
// detected cards so live GPU or concurrency setting changes take effect
// without a restart, while speechPlaces.take dynamically gates active slots.
func SpeechPoolWorkers(ctx context.Context, st *database.Store) int {
	workers := 1
	if st != nil {
		workers = max(SpeechWorkers(ctx, st), 1)
	}
	var devs []builtin.Device
	if bin, ok := builtin.Find(); ok {
		devs, _ = builtin.Devices(ctx, bin)
	}
	if allCards := cardNumbers(GPUAll, devs); len(allCards) > 0 {
		return max(workers, len(allCards)*MaxTranscribeAtOnce)
	}
	return workers
}
