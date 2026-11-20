package subtitles

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/jobs"
	"github.com/HolyArrs/holyarrs/app/backend/internal/subtitles/translate/builtin"
)

// KindModelDownload fetches one catalogue model into the models folder.
const KindModelDownload = "subtitle_model_download"

// modelHTTP fetches model files: no limit on the whole download, which can
// take long, but a server that stops answering is given up.
var modelHTTP = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
	TLSHandshakeTimeout:   30 * time.Second,
	ResponseHeaderTimeout: time.Minute,
}}

// modelStall is how long a download may get no bytes before it is given up.
// A variable so tests can shorten it.
var modelStall = time.Minute

// download is one model download that is queued or running.
type download struct {
	progress  float64
	cancelled bool
	cancel    context.CancelFunc // set while it runs
}

// downloading reports the progress of a queued or running download.
func (s *Service) downloading(id string) (float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.downloads[id]
	if d == nil || d.cancelled {
		return 0, false
	}
	return d.progress, true
}

func modelKey(id string) string { return KindModelDownload + ":" + id }

// QueueModelDownload asks for a catalogue model to be fetched. Each model has
// at most one live download.
func (s *Service) QueueModelDownload(ctx context.Context, id string) error {
	if _, ok := builtin.ByID(id); !ok {
		return ErrUnknownModel
	}
	if s.Enqueue == nil {
		return errors.New("no job queue")
	}
	if s.ModelsDir == "" {
		return errors.New("no models folder")
	}
	s.mu.Lock()
	if s.downloads == nil {
		s.downloads = map[string]*download{}
	}
	if d := s.downloads[id]; d == nil || d.cancelled {
		s.downloads[id] = &download{}
	}
	s.mu.Unlock()
	return s.Enqueue(ctx, KindModelDownload, nil, map[string]any{"id": id}, modelKey(id))
}

// CancelModelDownload stops a queued or running download and removes what it
// had fetched.
func (s *Service) CancelModelDownload(_ context.Context, id string) error {
	if _, ok := builtin.ByID(id); !ok {
		return ErrUnknownModel
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := s.downloads[id]; d != nil {
		d.cancelled = true
		if d.cancel != nil {
			d.cancel()
		}
	}
	return nil
}

func (s *Service) handleModelDownload(ctx context.Context, r *jobs.Run) error {
	id, _ := r.Job.Params["id"].(string)
	var last time.Time
	err := s.runModelDownload(ctx, id, func(p float64) {
		if p >= 1 || time.Since(last) >= time.Second {
			last = time.Now()
			_ = r.Progress(ctx, p, "")
		}
	})
	if err != nil && ctx.Err() != nil {
		return ctx.Err() // the app is stopping: the part file stays for the next start
	}
	return err
}

// runModelDownload is one run of a download Job: it honours a cancel that
// came before or during the run, and forgets the download when it is over.
func (s *Service) runModelDownload(ctx context.Context, id string, progress func(float64)) error {
	m, ok := builtin.ByID(id)
	if !ok {
		return ErrUnknownModel
	}
	dctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mu.Lock()
	if s.downloads == nil {
		s.downloads = map[string]*download{}
	}
	d := s.downloads[id]
	if d == nil {
		d = &download{}
		s.downloads[id] = d // a Job found again after a restart
	}
	d.cancel = cancel
	stopped := d.cancelled
	s.mu.Unlock()
	var err error
	if !stopped {
		err = s.downloadModel(dctx, id, func(p float64) {
			s.mu.Lock()
			d.progress = p
			s.mu.Unlock()
			progress(p)
		})
	}
	s.mu.Lock()
	stopped = d.cancelled
	if s.downloads[id] == d {
		delete(s.downloads, id)
	}
	s.mu.Unlock()
	if stopped {
		_ = os.Remove(filepath.Join(s.ModelsDir, m.ID+".gguf.part"))
		return nil // the owner stopped it: not a failure
	}
	return err
}

// downloadModel fetches a catalogue model: into a .part file, going on from
// what an earlier try left, then checked against the catalogue's size and
// SHA-256 and only then given its name. A file that fails the check is removed.
func (s *Service) downloadModel(ctx context.Context, id string, progress func(float64)) error {
	m, ok := builtin.ByID(id)
	if !ok {
		return ErrUnknownModel
	}
	if _, err := s.modelFor(id); err == nil {
		progress(1)
		return nil
	}
	if err := os.MkdirAll(s.ModelsDir, 0o750); err != nil {
		return fmt.Errorf("the models folder cannot be made: %w", err)
	}
	final := filepath.Join(s.ModelsDir, m.ID+".gguf")
	part := final + ".part"
	have, _ := plainFile(part)
	if have >= m.Size {
		have = 0 // more than the model: not a part of it
	}
	// A server that stops sending ends the download: each chunk of bytes
	// pushes the limit further out.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stall := time.AfterFunc(modelStall, cancel)
	defer stall.Stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL, nil)
	if err != nil {
		return err
	}
	if have > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
	}
	hc := s.HTTP
	if hc == nil {
		hc = modelHTTP
	}
	res, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s could not be fetched: %w", m.Name, err)
	}
	defer res.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY
	switch {
	case res.StatusCode == http.StatusPartialContent && have > 0:
		flags |= os.O_APPEND
	case res.StatusCode == http.StatusOK:
		have = 0
		flags |= os.O_TRUNC // the server sent the whole file: start over
	default:
		return fmt.Errorf("%s could not be fetched: the server answered %d", m.Name, res.StatusCode)
	}
	f, err := os.OpenFile(part, flags, 0o640)
	if err != nil {
		return err
	}
	written, werr := io.Copy(f, io.TeeReader(io.LimitReader(res.Body, m.Size-have+1), progressWriter{have: have, size: m.Size, tell: progress, seen: new(int64), alive: func() { stall.Reset(modelStall) }}))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("%s was not fetched whole, it goes on from here next time: %w", m.Name, werr)
	}
	if have+written < m.Size {
		return fmt.Errorf("%s was not fetched whole, it goes on from here next time", m.Name)
	}
	if sum, err := fileSHA256(part); err != nil || have+written != m.Size || sum != m.SHA256 {
		_ = os.Remove(part)
		return fmt.Errorf("the file fetched for %s is not the model it should be; it was removed", m.Name)
	}
	if err := os.Rename(part, final); err != nil {
		return err
	}
	progress(1)
	return nil
}

// progressWriter tells how far a download is as its bytes pass.
type progressWriter struct {
	have, size int64
	tell       func(float64)
	seen       *int64
	alive      func() // called as bytes come
}

func (p progressWriter) Write(b []byte) (int, error) {
	if p.seen == nil {
		return len(b), nil
	}
	*p.seen += int64(len(b))
	if p.alive != nil {
		p.alive()
	}
	p.tell(min(float64(p.have+*p.seen)/float64(max(p.size, 1)), 0.99))
	return len(b), nil
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
