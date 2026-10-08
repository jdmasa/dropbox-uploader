// Package uploader runs the upload queue: a pool of workers that upload files to
// Dropbox in chunked sessions, and a finisher that commits them in batches.
package uploader

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dropboxuploader/internal/dbx"
)

// API is the subset of the Dropbox client the uploader needs (faked in tests).
type API interface {
	Metadata(ctx context.Context, path string) (*dbx.Entry, error)
	StartSession(ctx context.Context, data []byte, close bool, progress dbx.ProgressFunc) (string, error)
	AppendSession(ctx context.Context, cur dbx.Cursor, data []byte, close bool, progress dbx.ProgressFunc) error
	FinishBatch(ctx context.Context, entries []dbx.FinishEntry) ([]dbx.FinishResult, error)
}

var (
	// ChunkSize is how much of a file is sent per request. Variables so tests can shrink them.
	ChunkSize int64 = 8 << 20
	// Retry attempts per chunk (backoff 1s, 2s, 4s ... capped at 60s ≈ 2 minutes total).
	chunkAttempts       = 8
	finishBatchMax      = 100
	finishWait          = 800 * time.Millisecond
	tickInterval        = 300 * time.Millisecond
	maxWorkers          = 8
	sessionMaxAge       = 6 * 24 * time.Hour // Dropbox sessions expire after 7 days
	errPaused           = errors.New("paused")
	finishRetryInterval = 5 * time.Second
)

type Options struct {
	Workers   int
	StatePath string                       // queue.json; empty disables persistence
	Emit      func(event string, data any) // sends events to the UI
	OnBusy    func(busy bool)              // keep the PC awake while true
	OnAllDone func(s RunStats)             // a run of uploads just finished
}

type RunStats struct {
	Done, Skipped, Failed int
}

type Summary struct {
	Queued     int     `json:"queued"`
	Uploading  int     `json:"uploading"`
	Done       int     `json:"done"`
	Skipped    int     `json:"skipped"`
	Failed     int     `json:"failed"`
	TotalBytes int64   `json:"totalBytes"`
	SentBytes  int64   `json:"sentBytes"`
	Speed      float64 `json:"speed"`
	ETA        float64 `json:"eta"` // seconds, 0 if unknown
	Paused     bool    `json:"paused"`
	Workers    int     `json:"workers"`
	Busy       bool    `json:"busy"`
}

// Update is sent to the UI: changed items, removed ids and the overall summary.
type Update struct {
	Items   []Item  `json:"items"`
	Removed []int64 `json:"removed"`
	Order   []int64 `json:"order,omitempty"` // set when the queue was reordered or on full snapshots
	Summary Summary `json:"summary"`
	Full    bool    `json:"full"`
}

type Manager struct {
	api  API
	opts Options

	mu      sync.Mutex
	cond    *sync.Cond
	items   []*Item
	byID    map[int64]*Item
	nextID  int64
	workers int
	active  int // items holding a worker slot (checking/uploading)
	paused  bool
	closed  bool
	cancels map[int64]context.CancelFunc
	retries map[int64]int // automatic finish retries per item

	dirty        map[int64]bool
	removed      []int64
	orderChanged bool
	saveDirty    bool
	lastSave     time.Time

	busy     bool
	run      RunStats
	speed    float64
	tickSent int64

	finishCh chan *Item
	ctx      context.Context
	stop     context.CancelFunc
	wg       sync.WaitGroup
}

func New(api API, opts Options) *Manager {
	if opts.Workers < 1 || opts.Workers > maxWorkers {
		opts.Workers = 4
	}
	if opts.Emit == nil {
		opts.Emit = func(string, any) {}
	}
	ctx, stop := context.WithCancel(context.Background())
	m := &Manager{
		api:      api,
		opts:     opts,
		byID:     map[int64]*Item{},
		workers:  opts.Workers,
		cancels:  map[int64]context.CancelFunc{},
		retries:  map[int64]int{},
		dirty:    map[int64]bool{},
		finishCh: make(chan *Item, 4096),
		ctx:      ctx,
		stop:     stop,
	}
	m.cond = sync.NewCond(&m.mu)
	m.load()
	return m
}

// SetAPI swaps the Dropbox client (after logging in).
func (m *Manager) SetAPI(api API) {
	m.mu.Lock()
	m.api = api
	m.cond.Broadcast()
	m.mu.Unlock()
}

func (m *Manager) getAPI() API {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.api
}

func (m *Manager) Start() {
	for i := 0; i < maxWorkers; i++ {
		m.wg.Add(1)
		go m.worker()
	}
	m.wg.Add(2)
	go m.finisher()
	go m.ticker()
}

// Close stops all workers and saves the queue. Interrupted uploads resume on next start.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.cond.Broadcast()
	m.mu.Unlock()
	m.stop()
	m.wg.Wait()
	m.save(true)
}

// ---- Queue operations (called from the UI) ----

var junkNames = map[string]bool{"desktop.ini": true, "thumbs.db": true, ".ds_store": true, "$recycle.bin": true, "system volume information": true}

func skipName(name string) bool {
	l := strings.ToLower(name)
	return junkNames[l] || strings.HasPrefix(l, ".") || strings.HasPrefix(l, "~$")
}

// Enqueue adds files and folders (recursively, keeping their structure) under destFolder.
func (m *Manager) Enqueue(paths []string, destFolder string) (int, error) {
	if destFolder == "" {
		destFolder = "/"
	}
	var add []*Item
	newItem := func(local, dest string, info fs.FileInfo) {
		add = append(add, &Item{Local: local, Dest: dest, Name: info.Name(), Size: info.Size(), ModTime: info.ModTime(), Status: Queued})
	}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return 0, err
		}
		if !info.IsDir() {
			newItem(p, path.Join(destFolder, info.Name()), info)
			continue
		}
		root := filepath.Dir(filepath.Clean(p))
		err = filepath.WalkDir(p, func(fp string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable folder: skip it
			}
			if fp != p && skipName(d.Name()) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			rel, err := filepath.Rel(root, fp)
			if err != nil {
				return nil
			}
			newItem(fp, path.Join(destFolder, filepath.ToSlash(rel)), info)
			return nil
		})
		if err != nil {
			return 0, err
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	pending := map[string]bool{}
	for _, it := range m.items {
		if it.Status == Queued || it.Status.active() {
			pending[it.Local+"\x00"+strings.ToLower(it.Dest)] = true
		}
	}
	n := 0
	for _, it := range add {
		key := it.Local + "\x00" + strings.ToLower(it.Dest)
		if pending[key] {
			continue
		}
		pending[key] = true
		m.nextID++
		it.ID = m.nextID
		m.items = append(m.items, it)
		m.byID[it.ID] = it
		m.dirty[it.ID] = true
		n++
	}
	m.orderChanged = true
	m.saveDirty = true
	m.cond.Broadcast()
	return n, nil
}

func (m *Manager) Pause() {
	m.mu.Lock()
	m.paused = true
	m.saveDirty = true
	m.mu.Unlock()
}

func (m *Manager) Resume() {
	m.mu.Lock()
	m.paused = false
	m.cond.Broadcast()
	m.mu.Unlock()
}

func (m *Manager) SetWorkers(n int) {
	if n < 1 {
		n = 1
	}
	if n > maxWorkers {
		n = maxWorkers
	}
	m.mu.Lock()
	m.workers = n
	m.cond.Broadcast()
	m.mu.Unlock()
}

// Remove takes items out of the queue, cancelling them if they are uploading.
func (m *Manager) Remove(ids []int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	drop := map[int64]bool{}
	for _, id := range ids {
		if _, ok := m.byID[id]; ok {
			drop[id] = true
		}
	}
	m.removeLocked(func(it *Item) bool { return drop[it.ID] })
}

// CancelAll removes everything that has not been uploaded yet.
func (m *Manager) CancelAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeLocked(func(it *Item) bool { return it.Status != Done && it.Status != Skipped && it.Status != Finishing })
}

// ClearFinished removes uploaded and skipped items from the list.
func (m *Manager) ClearFinished() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeLocked(func(it *Item) bool { return it.Status == Done || it.Status == Skipped })
}

func (m *Manager) removeLocked(match func(*Item) bool) {
	kept := m.items[:0]
	for _, it := range m.items {
		if !match(it) {
			kept = append(kept, it)
			continue
		}
		if cancel := m.cancels[it.ID]; cancel != nil {
			cancel()
		}
		delete(m.byID, it.ID)
		delete(m.dirty, it.ID)
		delete(m.retries, it.ID)
		m.removed = append(m.removed, it.ID)
	}
	for i := len(kept); i < len(m.items); i++ {
		m.items[i] = nil
	}
	m.items = kept
	m.saveDirty = true
	m.cond.Broadcast()
}

// Retry puts failed items (all of them when ids is empty) back in the queue.
func (m *Manager) Retry(ids []int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[int64]bool{}
	for _, id := range ids {
		want[id] = true
	}
	for _, it := range m.items {
		if it.Status == Failed && (len(ids) == 0 || want[it.ID]) {
			it.Status = Queued
			it.Error = ""
			delete(m.retries, it.ID)
			m.dirty[it.ID] = true
		}
	}
	m.saveDirty = true
	m.cond.Broadcast()
}

// MoveToTop moves items to the front of the queue so they upload next.
func (m *Manager) MoveToTop(ids []int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sel := map[int64]bool{}
	for _, id := range ids {
		sel[id] = true
	}
	front := make([]*Item, 0, len(m.items))
	var rest []*Item
	for _, it := range m.items {
		if sel[it.ID] {
			front = append(front, it)
		} else {
			rest = append(rest, it)
		}
	}
	m.items = append(front, rest...)
	m.orderChanged = true
	m.saveDirty = true
}

// Move shifts one item up (delta<0) or down (delta>0) among the queued items.
func (m *Manager) Move(id int64, delta int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := -1
	for i, it := range m.items {
		if it.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 || delta == 0 {
		return
	}
	step := 1
	if delta < 0 {
		step = -1
	}
	// Swap with the next queued neighbour in that direction.
	for j := idx + step; j >= 0 && j < len(m.items); j += step {
		if m.items[j].Status == Queued {
			m.items[idx], m.items[j] = m.items[j], m.items[idx]
			m.orderChanged = true
			m.saveDirty = true
			return
		}
	}
}

// Snapshot returns the whole queue for the UI.
func (m *Manager) Snapshot() Update {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := Update{Full: true, Summary: m.summaryLocked()}
	for _, it := range m.items {
		u.Items = append(u.Items, *it)
		u.Order = append(u.Order, it.ID)
	}
	return u
}

func (m *Manager) Busy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.busyLocked()
}

// HasPending reports whether anything is still waiting or uploading (even while paused).
func (m *Manager) HasPending() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, it := range m.items {
		if it.Status == Queued || it.Status.active() {
			return true
		}
	}
	return false
}

// ---- Workers ----

func (m *Manager) worker() {
	defer m.wg.Done()
	for {
		it, ctx, ok := m.next()
		if !ok {
			return
		}
		finishing, err := m.process(ctx, it)
		m.release(it, finishing, err)
	}
}

// next blocks until there is a queued item and a free worker slot.
func (m *Manager) next() (*Item, context.Context, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for {
		if m.closed {
			return nil, nil, false
		}
		if !m.paused && m.active < m.workers && m.api != nil {
			for _, it := range m.items {
				if it.Status == Queued {
					it.Status = Checking
					it.Error = ""
					m.active++
					ctx, cancel := context.WithCancel(m.ctx)
					m.cancels[it.ID] = cancel
					m.dirty[it.ID] = true
					m.updateBusyLocked()
					return it, ctx, true
				}
			}
		}
		m.cond.Wait()
	}
}

func (m *Manager) release(it *Item, finishing bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active--
	if cancel := m.cancels[it.ID]; cancel != nil {
		cancel()
		delete(m.cancels, it.ID)
	}
	m.cond.Broadcast()
	if _, still := m.byID[it.ID]; !still {
		m.updateBusyLocked()
		return // removed by the user while uploading
	}
	it.Speed = 0
	switch {
	case finishing:
		// The finisher owns it now.
	case errors.Is(err, errPaused) || (m.closed && err != nil):
		it.Status = Queued
	case err != nil:
		it.Status = Failed
		it.Error = friendlyError(err)
		m.run.Failed++
	}
	m.dirty[it.ID] = true
	m.saveDirty = true
	m.updateBusyLocked()
}

// process checks for duplicates and uploads the file. It returns finishing=true
// when the closed session was handed to the finisher.
func (m *Manager) process(ctx context.Context, it *Item) (bool, error) {
	api := m.getAPI()
	info, err := os.Stat(it.Local)
	if err != nil {
		return false, err
	}
	m.mu.Lock()
	if info.Size() != it.Size || !info.ModTime().Equal(it.ModTime) {
		// The file changed since it was queued: start over.
		it.Size, it.ModTime = info.Size(), info.ModTime()
		it.resetSession()
	}
	if it.SessionID != "" && time.Since(it.SessionStarted) > sessionMaxAge {
		it.resetSession()
	}
	resuming := it.SessionID != ""
	m.mu.Unlock()

	if !resuming {
		meta, err := api.Metadata(ctx, it.Dest)
		if err != nil {
			return false, err
		}
		if meta != nil && !meta.IsFolder() && meta.Size == it.Size {
			h, err := dbx.FileContentHash(it.Local)
			if err != nil {
				return false, err
			}
			if h == meta.ContentHash {
				m.mu.Lock()
				it.Status = Skipped
				it.Sent = it.Size
				it.ResultPath = meta.PathDisplay
				m.run.Skipped++
				m.mu.Unlock()
				return false, nil
			}
		}
	}

	m.mu.Lock()
	complete := it.SessionID != "" && it.Offset == it.Size
	m.mu.Unlock()
	if !complete {
		if err := m.upload(ctx, api, it); err != nil {
			return false, err
		}
	}
	m.mu.Lock()
	it.Status = Finishing
	it.Sent = it.Size
	m.dirty[it.ID] = true
	m.mu.Unlock()
	m.finishCh <- it
	return true, nil
}

func (m *Manager) upload(ctx context.Context, api API, it *Item) error {
	f, err := os.Open(it.Local)
	if err != nil {
		return err
	}
	defer f.Close()

	m.mu.Lock()
	it.Status = Uploading
	it.Sent = it.Offset
	it.lastSent = it.Sent
	m.dirty[it.ID] = true
	size := it.Size
	m.mu.Unlock()

	buf := make([]byte, min(ChunkSize, max(size, 1)))
	restarted := false
	for {
		m.mu.Lock()
		paused, sid, off := m.paused, it.SessionID, it.Offset
		m.mu.Unlock()
		if sid != "" && off >= size {
			return nil
		}
		if paused {
			return errPaused
		}
		n := min(ChunkSize, size-off)
		data := buf[:n]
		if _, err := f.ReadAt(data, off); err != nil && !(errors.Is(err, io.EOF) && n == 0) {
			return err
		}
		last := off+n >= size

		var attemptSent atomic.Int64
		progress := func(k int64) {
			attemptSent.Add(k)
			m.addSent(it, k)
		}
		var newSID string
		err := dbx.Retry(ctx, chunkAttempts, func() error {
			m.addSent(it, -attemptSent.Swap(0)) // undo progress of a failed attempt
			if sid == "" {
				var err error
				newSID, err = api.StartSession(ctx, data, last, progress)
				return err
			}
			return api.AppendSession(ctx, dbx.Cursor{SessionID: sid, Offset: off}, data, last, progress)
		})
		if err != nil {
			m.addSent(it, -attemptSent.Swap(0))
			if correct, ok := dbx.IncorrectOffset(err); ok && sid != "" {
				m.mu.Lock()
				it.Offset, it.Sent = correct, correct
				m.mu.Unlock()
				continue
			}
			if sid != "" && dbx.SessionGone(err) && !restarted {
				restarted = true
				m.mu.Lock()
				it.resetSession()
				it.Sent = 0
				m.mu.Unlock()
				continue
			}
			return err
		}
		m.mu.Lock()
		if sid == "" {
			it.SessionID = newSID
			it.SessionStarted = time.Now()
		}
		it.Offset = off + n
		it.Sent = it.Offset
		m.dirty[it.ID] = true
		m.saveDirty = true
		m.mu.Unlock()
		if last {
			return nil
		}
	}
}

func (m *Manager) addSent(it *Item, n int64) {
	if n == 0 {
		return
	}
	m.mu.Lock()
	it.Sent += n
	m.tickSent += n
	m.dirty[it.ID] = true
	m.mu.Unlock()
}

// ---- Finisher: commits closed sessions in batches ----

func (m *Manager) finisher() {
	defer m.wg.Done()
	var batch []*Item
	var timer <-chan time.Time
	for {
		select {
		case it := <-m.finishCh:
			batch = append(batch, it)
			if len(batch) == 1 {
				timer = time.After(finishWait)
			}
			if len(batch) < finishBatchMax {
				continue
			}
		case <-timer:
		case <-m.ctx.Done():
			return
		}
		if len(batch) > 0 {
			m.flush(batch)
		}
		batch, timer = nil, nil
	}
}

func (m *Manager) flush(batch []*Item) {
	api := m.getAPI()
	entries := make([]dbx.FinishEntry, len(batch))
	m.mu.Lock()
	for i, it := range batch {
		entries[i] = dbx.FinishEntry{
			Cursor: dbx.Cursor{SessionID: it.SessionID, Offset: it.Offset},
			Commit: dbx.Commit{Path: it.Dest, ClientModified: it.ModTime},
		}
	}
	m.mu.Unlock()

	var results []dbx.FinishResult
	err := dbx.Retry(m.ctx, chunkAttempts, func() error {
		var err error
		results, err = api.FinishBatch(m.ctx, entries)
		return err
	})

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		for _, it := range batch {
			it.Status = Queued // session kept; finish is retried on next start
		}
		return
	}
	var again []*Item
	for i, it := range batch {
		m.dirty[it.ID] = true
		m.saveDirty = true
		if err != nil {
			it.Status = Failed
			it.Error = friendlyError(err)
			m.run.Failed++
			continue
		}
		r := results[i]
		if r.Err == nil {
			it.Status = Done
			it.Sent = it.Size
			it.ResultPath = r.Entry.PathDisplay
			it.resetSession()
			m.run.Done++
			continue
		}
		switch {
		case dbx.Retryable(r.Err) && m.retries[it.ID] < 5:
			m.retries[it.ID]++
			again = append(again, it)
		case strings.Contains(r.Err.Error(), "lookup_failed") && m.retries[it.ID] < 5:
			// Session expired or already committed: upload again (the duplicate check
			// will notice if the earlier commit did go through).
			m.retries[it.ID]++
			it.resetSession()
			it.Sent = 0
			it.Status = Queued
			m.cond.Broadcast()
		default:
			it.Status = Failed
			it.Error = friendlyError(r.Err)
			m.run.Failed++
		}
	}
	if len(again) > 0 {
		go func() {
			select {
			case <-time.After(finishRetryInterval):
				for _, it := range again {
					m.finishCh <- it
				}
			case <-m.ctx.Done():
			}
		}()
	}
	m.updateBusyLocked()
}

// ---- Progress events and persistence ----

func (m *Manager) ticker() {
	defer m.wg.Done()
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			m.tick()
		case <-m.ctx.Done():
			return
		}
	}
}

func (m *Manager) tick() {
	m.mu.Lock()
	secs := tickInterval.Seconds()
	inst := float64(m.tickSent) / secs
	m.tickSent = 0
	m.speed = 0.7*m.speed + 0.3*inst
	if m.speed < 1 {
		m.speed = 0
	}
	u := Update{Summary: m.summaryLocked()}
	for _, it := range m.items {
		if it.Status == Uploading {
			d := float64(it.Sent-it.lastSent) / secs
			it.lastSent = it.Sent
			it.Speed = 0.6*it.Speed + 0.4*d
			m.dirty[it.ID] = true
		}
	}
	for id := range m.dirty {
		if it := m.byID[id]; it != nil {
			u.Items = append(u.Items, *it)
		}
	}
	m.dirty = map[int64]bool{}
	u.Removed, m.removed = m.removed, nil
	if m.orderChanged {
		m.orderChanged = false
		for _, it := range m.items {
			u.Order = append(u.Order, it.ID)
		}
	}
	m.mu.Unlock()

	m.opts.Emit("queue:update", u)
	m.save(false)
}

func (m *Manager) summaryLocked() Summary {
	s := Summary{Paused: m.paused, Workers: m.workers, Speed: m.speed, Busy: m.busyLocked()}
	var remaining int64
	for _, it := range m.items {
		switch it.Status {
		case Queued:
			s.Queued++
		case Checking, Uploading, Finishing:
			s.Uploading++
		case Done:
			s.Done++
		case Skipped:
			s.Skipped++
		case Failed:
			s.Failed++
		}
		if it.Status == Failed {
			continue
		}
		s.TotalBytes += it.Size
		if it.Status == Done || it.Status == Skipped {
			s.SentBytes += it.Size
		} else {
			s.SentBytes += min(it.Sent, it.Size)
			remaining += it.Size - min(it.Sent, it.Size)
		}
	}
	if s.Speed > 0 && remaining > 0 {
		s.ETA = float64(remaining) / s.Speed
	}
	return s
}

func (m *Manager) busyLocked() bool {
	for _, it := range m.items {
		if it.Status.active() || (it.Status == Queued && !m.paused) {
			return true
		}
	}
	return false
}

func (m *Manager) updateBusyLocked() {
	busy := m.busyLocked()
	if busy == m.busy {
		return
	}
	m.busy = busy
	if busy {
		m.run = RunStats{}
	}
	stats := m.run
	if m.opts.OnBusy != nil {
		go m.opts.OnBusy(busy)
	}
	if !busy && !m.paused && m.opts.OnAllDone != nil && stats != (RunStats{}) {
		go m.opts.OnAllDone(stats)
	}
}

type savedState struct {
	NextID int64   `json:"nextId"`
	Paused bool    `json:"paused"`
	Items  []*Item `json:"items"`
}

func (m *Manager) load() {
	if m.opts.StatePath == "" {
		return
	}
	b, err := os.ReadFile(m.opts.StatePath)
	if err != nil {
		return
	}
	var st savedState
	if json.Unmarshal(b, &st) != nil {
		return
	}
	m.nextID = st.NextID
	m.paused = st.Paused
	for _, it := range st.Items {
		if it.Status.active() {
			it.Status = Queued // interrupted: resume from the saved session offset
		}
		it.Sent = it.Offset
		if it.Status == Done || it.Status == Skipped {
			it.Sent = it.Size
		}
		it.Speed = 0
		m.items = append(m.items, it)
		m.byID[it.ID] = it
		if it.ID > m.nextID {
			m.nextID = it.ID
		}
	}
}

func (m *Manager) save(force bool) {
	if m.opts.StatePath == "" {
		return
	}
	m.mu.Lock()
	if !m.saveDirty || (!force && time.Since(m.lastSave) < 2*time.Second) {
		m.mu.Unlock()
		return
	}
	m.saveDirty = false
	m.lastSave = time.Now()
	st := savedState{NextID: m.nextID, Paused: m.paused}
	for _, it := range m.items {
		cp := *it
		st.Items = append(st.Items, &cp)
	}
	m.mu.Unlock()

	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	tmp := m.opts.StatePath + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, m.opts.StatePath)
	}
}
