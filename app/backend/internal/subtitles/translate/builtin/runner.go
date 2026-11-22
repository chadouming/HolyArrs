package builtin

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Where the runner is looked for.
const (
	DefaultPath = "/opt/llama/llama-server"
	EnvPath     = "HOLYARRS_LLAMA_SERVER"
)

// Time limits. Variables so tests can shorten them.
var (
	// IdleStop is how long the runner stays after its last call.
	IdleStop = 5 * time.Minute
	// StartLimit is how long a start may take before it is given up.
	StartLimit = 2 * time.Minute
)

const (
	maxLogLines = 200 // runner lines passed on per run
	tailLines   = 20  // last lines kept for a start error
)

// ErrNoMemory means the model needs more memory than is free right now.
var ErrNoMemory = errors.New("not enough free memory for the model")

// Find returns the runner binary and whether it is there and can be run.
func Find() (path string, ok bool) {
	path = strings.TrimSpace(os.Getenv(EnvPath))
	if path == "" {
		path = DefaultPath
	}
	fi, err := os.Stat(path)
	return path, err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0
}

// MaxDefaultThreads caps the default: on a machine that is also doing other
// work, more threads than this made the model no faster.
const MaxDefaultThreads = 8

// DefaultThreads is half the CPU cores, at least one and at most MaxDefaultThreads.
func DefaultThreads() int { return min(max(runtime.NumCPU()/2, 1), MaxDefaultThreads) }

// Options say what the runner is started with.
type Options struct {
	Binary    string // from Find
	ModelPath string
	Threads   int
	Context   int    // tokens, for each slot
	NeedBytes uint64 // memory the model needs; 0 skips the check
	// GPU is the graphics card the model runs on, as Devices names it
	// ("Vulkan0"); empty keeps the model on the CPU.
	GPU string
	// CacheDir is where the graphics driver keeps what it compiles, so a
	// later start is quicker; empty for none.
	CacheDir string
	// Parallel is how many calls the runner takes at once; 0 and 1 are one.
	Parallel int
	// NoThink tells a model that reasons before it answers not to.
	NoThink bool
	// FlashAttn turns on flash attention when running on a graphics card.
	FlashAttn bool
	// QuantKV stores the KV cache in q8_0 when running on a graphics card.
	QuantKV bool
}

// LlamaNoFlashAttn reports whether flash attention and quantized KV cache
// are disabled via environment variable.
func LlamaNoFlashAttn() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("HOLYARRS_LLAMA_NO_FLASH_ATTN"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// slots is how many calls the runner takes at once.
func (o Options) slots() int { return max(o.Parallel, 1) }

// proc is one started runner process.
type proc struct {
	cmd       *exec.Cmd
	opts      Options
	base, key string
	exited    chan struct{}

	mu    sync.Mutex
	lines int
	tail  []string
}

func (p *proc) lastLines() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.tail, "; ")
}

// Runner owns the runner process: at most one, started on demand.
type Runner struct {
	log func(line string)

	start sync.Mutex // one Ensure at a time

	mu     sync.Mutex
	cur    *proc
	ready  bool // cur answered its health check
	busy   int  // calls between Ensure and Used
	idle   *time.Timer
	last   int // pid of the last process started, for tests
	closed bool
}

// NewRunner returns a Runner that passes the process's own lines to log.
func NewRunner(log func(line string)) *Runner {
	if log == nil {
		log = func(string) {}
	}
	return &Runner{log: log}
}

// Running reports whether a runner process is there.
func (r *Runner) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cur != nil
}

func (r *Runner) pid() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur == nil || r.cur.cmd.Process == nil {
		return 0
	}
	return r.cur.cmd.Process.Pid
}

func (r *Runner) lastPid() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// Ensure starts the runner for o, or reuses the running one when o is the
// same; other options stop it and start again. It returns once the runner
// answers its health check. Every successful Ensure is followed by one Used.
func (r *Runner) Ensure(ctx context.Context, o Options) (baseURL, key string, err error) {
	r.start.Lock()
	defer r.start.Unlock()

	r.mu.Lock()
	if r.idle != nil {
		r.idle.Stop()
	}
	if p := r.cur; p != nil && r.ready && p.opts == o {
		r.busy++
		r.mu.Unlock()
		return p.base, p.key, nil
	}
	r.mu.Unlock()
	r.Stop() // another model or thread count, or a start that never finished

	if o.NeedBytes > 0 {
		if free := FreeMemory(); free > 0 && free < o.NeedBytes {
			return "", "", fmt.Errorf("%w: it needs %s and %s is free", ErrNoMemory, gib(o.NeedBytes), gib(free))
		}
	}
	p, err := r.launch(o)
	if err != nil {
		return "", "", err
	}
	if err := r.waitHealthy(ctx, p); err != nil {
		r.kill(p)
		return "", "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur != p {
		return "", "", errors.New("the model runner was stopped while it started")
	}
	r.ready = true
	r.busy = 1
	return p.base, p.key, nil
}

// Used marks a finished call. With no call left, the idle wait starts.
func (r *Runner) Used() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.busy > 0 {
		r.busy--
	}
	if r.busy > 0 || r.cur == nil {
		return
	}
	if r.idle != nil {
		r.idle.Stop()
	}
	p := r.cur
	r.idle = time.AfterFunc(IdleStop, func() {
		r.mu.Lock()
		still := r.cur == p && r.busy == 0
		r.mu.Unlock()
		if still {
			r.kill(p)
		}
	})
}

// Stop ends the runner process, if any, and waits until it is gone.
func (r *Runner) Stop() {
	r.mu.Lock()
	p := r.cur
	if r.idle != nil {
		r.idle.Stop()
	}
	r.mu.Unlock()
	if p != nil {
		r.kill(p)
	}
}

// StopIfIdle stops the runner process if it is running and has no active calls.
func (r *Runner) StopIfIdle() bool {
	if !r.start.TryLock() {
		return false
	}
	defer r.start.Unlock()
	r.mu.Lock()
	if r.cur == nil || !r.ready || r.busy > 0 {
		r.mu.Unlock()
		return false
	}
	if r.idle != nil {
		r.idle.Stop()
		r.idle = nil
	}
	p := r.cur
	r.cur, r.ready = nil, false
	r.mu.Unlock()
	r.kill(p)
	return true
}

func (r *Runner) kill(p *proc) {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	<-p.exited
}

func gib(n uint64) string {
	return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " GB"
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// launch starts the process and returns at once; waitHealthy says when it is up.
func (r *Runner) launch(o Options) (*proc, error) {
	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("no free port for the model runner: %w", err)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	p := &proc{opts: o, key: hex.EncodeToString(raw), base: "http://127.0.0.1:" + strconv.Itoa(port), exited: make(chan struct{})}
	args := []string{
		"--model", o.ModelPath,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--api-key", p.key,
		"--threads", strconv.Itoa(max(o.Threads, 1)),
		// The runner shares its context between its slots.
		"--ctx-size", strconv.Itoa(o.Context * o.slots()),
		"--parallel", strconv.Itoa(o.slots()),
	}
	if o.NoThink {
		args = append(args, "--reasoning-budget", "0")
	}
	if o.GPU != "" {
		args = append(args, "--gpu-layers", "999", "--device", o.GPU)
		if !LlamaNoFlashAttn() {
			if o.FlashAttn {
				args = append(args, "--flash-attn", "on")
			}
			if o.QuantKV {
				args = append(args, "--cache-type-k", "q8_0", "--cache-type-v", "q8_0")
			}
		}
	} else {
		// Said outright: left alone, the runner takes any card it can see.
		args = append(args, "--gpu-layers", "0")
	}
	p.cmd = exec.Command(o.Binary, append(args,
		// Nothing but the chat call is wanted: no fetching of models, no web
		// page, no view of what is being translated.
		"--offline", "--no-webui", "--no-slots")...)
	if o.CacheDir != "" {
		p.cmd.Env = append(os.Environ(), "XDG_CACHE_HOME="+o.CacheDir)
	}
	p.cmd.SysProcAttr = childAttr()
	pr, pw := io.Pipe()
	p.cmd.Stdout, p.cmd.Stderr = pw, pw
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			p.mu.Lock()
			p.lines++
			pass := p.lines <= maxLogLines
			p.tail = append(p.tail, line)
			if len(p.tail) > tailLines {
				p.tail = p.tail[1:]
			}
			p.mu.Unlock()
			if pass {
				r.log("llama: " + line)
			}
		}
		_, _ = io.Copy(io.Discard, pr)
	}()
	started := make(chan error, 1)
	// The process is started and waited for on one locked thread: the kernel
	// stops the child when the thread that started it ends (Pdeathsig), and
	// Go may end any other thread while HolyArrs is still running.
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := p.cmd.Start(); err != nil {
			started <- err
			_ = pw.Close()
			close(p.exited)
			return
		}
		r.mu.Lock()
		r.cur, r.ready, r.busy, r.last = p, false, 0, p.cmd.Process.Pid
		r.mu.Unlock()
		started <- nil
		_ = p.cmd.Wait()
		_ = pw.Close()
		r.mu.Lock()
		if r.cur == p {
			r.cur, r.ready, r.busy = nil, false, 0
		}
		r.mu.Unlock()
		close(p.exited)
	}()
	if err := <-started; err != nil {
		return nil, fmt.Errorf("the model runner could not be started: %w", err)
	}
	return p, nil
}

func (r *Runner) waitHealthy(ctx context.Context, p *proc) error {
	limit := time.NewTimer(StartLimit)
	defer limit.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	hc := &http.Client{Timeout: 2 * time.Second}
	for {
		select {
		case <-p.exited:
			if l := p.lastLines(); l != "" {
				return fmt.Errorf("the model runner stopped while starting: %s", l)
			}
			return errors.New("the model runner stopped while starting")
		case <-limit.C:
			return fmt.Errorf("the model runner did not start within %s", StartLimit)
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/health", nil)
			if res, err := hc.Do(req); err == nil {
				_, _ = io.Copy(io.Discard, res.Body)
				_ = res.Body.Close()
				if res.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
	}
}
