package transcribe

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Time limits of the speech server. Variables so tests can shorten them.
var (
	// ServerIdleStop is how long the server stays after its last call. A
	// stopped server gives the card's memory back.
	ServerIdleStop = 5 * time.Minute
	// ServerStartLimit is how long loading the model may take.
	ServerStartLimit = 3 * time.Minute
)

// serverStart is what a speech server process was started with: a change in
// any of it needs a new process.
type serverStart struct {
	binary, model, vad, cache string
	device, threads           int
	noFlashAttn               bool
}

func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func (o Options) noFlashAttn() bool {
	return o.NoFlashAttn || envBool("HOLYARRS_WHISPER_NO_FLASH_ATTN")
}

func (o Options) serverStart() serverStart {
	return serverStart{
		binary:      o.Programs.Server,
		model:       o.Model,
		vad:         o.VAD,
		cache:       o.CacheDir,
		device:      o.Device,
		threads:     max(o.Threads, 1),
		noFlashAttn: o.noFlashAttn(),
	}
}

// serverProc is one started server process.
type serverProc struct {
	cmd    *exec.Cmd
	start  serverStart
	base   string
	exited chan struct{}

	mu     sync.Mutex
	listen func(line string) // the call in flight; nil between calls
	tail   []string
}

func (p *serverProc) line(l string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tail = append(p.tail, l); len(p.tail) > tailLines {
		p.tail = p.tail[len(p.tail)-tailLines:]
	}
	if p.listen != nil {
		p.listen(l)
	}
}

// Server is whisper.cpp's server with the speech model loaded: one process,
// started when a file needs it and stopped when it has been idle. It takes
// one call at a time. Keeping it between calls saves loading the model for
// each of a file's language checks and for its transcript, and for the next
// file.
type Server struct {
	log func(line string)

	call sync.Mutex // one call at a time: the log between two calls is one call's

	mu        sync.Mutex
	cur       *serverProc
	idle      *time.Timer
	closed    bool
	forceNoFA bool
}

// NewServer returns a Server that passes a failed start's last lines to log.
func NewServer(log func(line string)) *Server {
	if log == nil {
		log = func(string) {}
	}
	return &Server{log: log}
}

// Running reports whether a server process is there.
func (s *Server) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur != nil
}

// Stop ends the process and keeps the Server from starting another.
func (s *Server) Stop() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.stop()
}

// StopIfIdle stops the server process if no call is currently in flight.
func (s *Server) StopIfIdle() bool {
	if !s.call.TryLock() {
		return false
	}
	defer s.call.Unlock()
	s.stop()
	return true
}

func (s *Server) stop() {
	s.mu.Lock()
	p := s.cur
	s.cur = nil
	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
	s.mu.Unlock()
	if p == nil {
		return
	}
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	select {
	case <-p.exited:
	case <-time.After(10 * time.Second):
	}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// ensure returns a running server started with st, starting or replacing the
// process as needed.
func (s *Server) ensure(ctx context.Context, st serverStart) (*serverProc, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("the speech server is stopped")
	}
	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
	p := s.cur
	s.mu.Unlock()
	if p != nil {
		select {
		case <-p.exited:
			p = nil
		default:
		}
	}
	if p != nil && p.start == st {
		return p, nil
	}
	s.stop()
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	args := []string{"-m", st.model, "-vm", st.vad, "-vmsd", "20", "-t", strconv.Itoa(st.threads), "--host", "127.0.0.1", "--port", strconv.Itoa(port), "-pp"}
	if st.device < 0 {
		args = append(args, "-ng")
	} else {
		args = append(args, "-dev", strconv.Itoa(st.device))
	}
	if st.noFlashAttn {
		args = append(args, "-nfa")
	}
	p = &serverProc{start: st, base: "http://127.0.0.1:" + strconv.Itoa(port), exited: make(chan struct{})}
	// Not CommandContext: the process outlives the call that started it.
	p.cmd = exec.Command(st.binary, args...)
	p.cmd.SysProcAttr = childAttr()
	if st.device >= 0 && st.cache != "" {
		p.cmd.Env = append(os.Environ(), "XDG_CACHE_HOME="+st.cache)
	}
	pr, pw := io.Pipe()
	p.cmd.Stdout, p.cmd.Stderr = io.Discard, pw
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			p.line(sc.Text())
		}
		_, _ = io.Copy(io.Discard, pr)
	}()
	started := make(chan error, 1)
	// Started and waited for on one locked thread: the kernel stops the child
	// when the thread that started it ends, and Go may end any other.
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := p.cmd.Start(); err != nil {
			started <- err
			_ = pw.Close()
			close(p.exited)
			return
		}
		started <- nil
		_ = p.cmd.Wait()
		_ = pw.Close()
		s.mu.Lock()
		if s.cur == p {
			s.cur = nil
		}
		s.mu.Unlock()
		close(p.exited)
	}()
	if err := <-started; err != nil {
		return nil, fmt.Errorf("the speech server could not be started: %w", err)
	}
	s.mu.Lock()
	s.cur = p
	s.mu.Unlock()
	fail := func(why string) (*serverProc, error) {
		p.mu.Lock()
		tail := append([]string(nil), p.tail...)
		p.mu.Unlock()
		for _, l := range tail {
			s.log(filepath.Base(st.binary) + ": " + l)
		}
		s.stop()
		if n := len(tail); n > 0 {
			why += ": " + strings.Join(tail[max(n-3, 0):], "; ")
		}
		return nil, errors.New(why)
	}
	deadline := time.Now().Add(ServerStartLimit)
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		select {
		case <-p.exited:
			return fail("the speech server stopped while it loaded the model")
		case <-ctx.Done():
			s.stop()
			return nil, ctx.Err()
		default:
		}
		if resp, err := client.Get(p.base + "/health"); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return p, nil
			}
		}
		if time.Now().After(deadline) {
			return fail("the speech server took too long to load the model")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func hasDeviceLost(err error, lines []string) bool {
	if err != nil && (strings.Contains(err.Error(), "ErrorDeviceLost") || strings.Contains(err.Error(), "VK_ERROR_DEVICE_LOST")) {
		return true
	}
	for _, l := range lines {
		if strings.Contains(l, "ErrorDeviceLost") || strings.Contains(l, "VK_ERROR_DEVICE_LOST") {
			return true
		}
	}
	return false
}

// post sends one sound file to the server with the given fields and returns
// its answer. onLine hears every line the server says while it works on it.
// A failure passes the server's last lines to log.
func (s *Server) post(ctx context.Context, o Options, wav string, fields map[string]string, onLine func(string), log func(string)) ([]byte, error) {
	s.call.Lock()
	defer s.call.Unlock()
	st := o.serverStart()
	s.mu.Lock()
	if s.forceNoFA {
		st.noFlashAttn = true
	}
	s.mu.Unlock()
	body, deviceLost, err := s.postOnce(ctx, o, st, wav, fields, onLine, log)
	if err != nil && deviceLost && st.device >= 0 && !st.noFlashAttn && ctx.Err() == nil {
		s.mu.Lock()
		s.forceNoFA = true
		s.mu.Unlock()
		s.stop()
		if log != nil {
			log(filepath.Base(o.Programs.Server) + ": retrying without flash attention after GPU device loss")
		}
		st.noFlashAttn = true
		body, _, err = s.postOnce(ctx, o, st, wav, fields, onLine, log)
	}
	return body, err
}

func (s *Server) postOnce(ctx context.Context, o Options, st serverStart, wav string, fields map[string]string, onLine func(string), log func(string)) ([]byte, bool, error) {
	p, err := s.ensure(ctx, st)
	if err != nil {
		return nil, hasDeviceLost(err, nil), err
	}
	var said []string
	p.mu.Lock()
	p.listen = func(l string) {
		if said = append(said, l); len(said) > tailLines {
			said = said[len(said)-tailLines:]
		}
		if onLine != nil {
			onLine(l)
		}
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.listen = nil
		p.mu.Unlock()
		s.mu.Lock()
		if s.cur == p && !s.closed {
			if s.idle != nil {
				s.idle.Stop()
			}
			s.idle = time.AfterFunc(ServerIdleStop, func() {
				// Not while a call that came in meanwhile is running.
				if s.call.TryLock() {
					s.stop()
					s.call.Unlock()
				}
			})
		}
		s.mu.Unlock()
	}()
	f, err := os.Open(wav)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	// The sound is sent as it is read: a film's is a few hundred megabytes.
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		var werr error
		for k, v := range fields {
			if werr == nil {
				werr = mw.WriteField(k, v)
			}
		}
		if werr == nil {
			var part io.Writer
			if part, werr = mw.CreateFormFile("file", "audio.wav"); werr == nil {
				_, werr = io.Copy(part, f)
			}
		}
		if werr == nil {
			werr = mw.Close()
		}
		_ = pw.CloseWithError(werr)
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/inference", pr)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	failed := func(err error, exited bool) ([]byte, bool, error) {
		if ctx.Err() != nil {
			// A stopped call leaves the server working on it: a new one is
			// started for the next file.
			s.stop()
			return nil, false, ctx.Err()
		}
		// What the server said is read after its answer, so give its last
		// lines a moment to arrive.
		time.Sleep(50 * time.Millisecond)
		p.mu.Lock()
		lines := append([]string(nil), said...)
		p.mu.Unlock()
		if !exited {
			select {
			case <-p.exited:
				exited = true
			default:
			}
		}
		if log != nil {
			for _, l := range lines {
				if !progressLine.MatchString(l) {
					log(filepath.Base(o.Programs.Server) + ": " + l)
				}
			}
		}
		return nil, exited || hasDeviceLost(err, lines), err
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		select {
		case <-p.exited:
			return failed(errors.New("the speech server stopped while it worked on the file"), true)
		default:
		}
		return failed(fmt.Errorf("the speech server did not answer: %w", err), false)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return failed(fmt.Errorf("the speech server's answer could not be read: %w", err), false)
	}
	if resp.StatusCode != http.StatusOK {
		return failed(fmt.Errorf("the speech server failed: %s", strings.TrimSpace(string(bytes.TrimSpace(body[:min(len(body), 200)])))), false)
	}
	// The log of this call is complete once its last lines have been read.
	time.Sleep(20 * time.Millisecond)
	return body, false, nil
}
