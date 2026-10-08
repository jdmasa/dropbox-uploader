package uploader

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"dropboxuploader/internal/dbx"
)

// fakeDropbox keeps sessions and committed files in memory.
type fakeDropbox struct {
	mu        sync.Mutex
	sessions  map[string]*fakeSession
	files     map[string][]byte
	nextSID   int
	failNext  int // fail this many append/start calls with a network error
	appends   int
	finishes  int
	wrongOnce bool // report incorrect_offset once
}

type fakeSession struct {
	data   []byte
	closed bool
}

func newFake() *fakeDropbox {
	return &fakeDropbox{sessions: map[string]*fakeSession{}, files: map[string][]byte{}}
}

func (f *fakeDropbox) Metadata(ctx context.Context, p string) (*dbx.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.files[p]
	if !ok {
		return nil, nil
	}
	h := dbx.NewContentHasher()
	h.Write(data)
	return &dbx.Entry{Tag: "file", PathDisplay: p, Size: int64(len(data)), ContentHash: h.Sum()}, nil
}

func (f *fakeDropbox) maybeFail() error {
	if f.failNext > 0 {
		f.failNext--
		return fmt.Errorf("connection reset")
	}
	return nil
}

func (f *fakeDropbox) StartSession(ctx context.Context, data []byte, close bool, progress dbx.ProgressFunc) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.maybeFail(); err != nil {
		return "", err
	}
	f.nextSID++
	id := fmt.Sprintf("s%d", f.nextSID)
	f.sessions[id] = &fakeSession{data: append([]byte(nil), data...), closed: close}
	if progress != nil {
		progress(int64(len(data)))
	}
	return id, nil
}

func (f *fakeDropbox) AppendSession(ctx context.Context, cur dbx.Cursor, data []byte, close bool, progress dbx.ProgressFunc) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.maybeFail(); err != nil {
		return err
	}
	f.appends++
	s := f.sessions[cur.SessionID]
	if s == nil {
		return &dbx.APIError{Status: 409, Summary: "not_found/"}
	}
	if f.wrongOnce || cur.Offset != int64(len(s.data)) {
		f.wrongOnce = false
		raw := []byte(fmt.Sprintf(`{".tag":"incorrect_offset","correct_offset":%d}`, len(s.data)))
		return &dbx.APIError{Status: 409, Summary: "incorrect_offset/", Raw: raw}
	}
	s.data = append(s.data, data...)
	s.closed = close
	if progress != nil {
		progress(int64(len(data)))
	}
	return nil
}

func (f *fakeDropbox) FinishBatch(ctx context.Context, entries []dbx.FinishEntry) ([]dbx.FinishResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finishes++
	res := make([]dbx.FinishResult, len(entries))
	for i, e := range entries {
		s := f.sessions[e.Cursor.SessionID]
		if s == nil || !s.closed || int64(len(s.data)) != e.Cursor.Offset {
			res[i].Err = &dbx.APIError{Status: 409, Summary: "lookup_failed/not_found"}
			continue
		}
		p := e.Commit.Path
		if _, exists := f.files[p]; exists {
			p = p + " (1)"
		}
		f.files[p] = s.data
		delete(f.sessions, e.Cursor.SessionID)
		res[i].Entry = &dbx.Entry{Tag: "file", PathDisplay: p}
	}
	return res, nil
}

func writeFile(t *testing.T, dir, name string, size int) string {
	t.Helper()
	p := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(p), 0o755)
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i*31 + len(name))
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func waitIdle(t *testing.T, m *Manager) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !m.HasPending() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("queue did not finish: %+v", m.Snapshot().Summary)
}

func setFast(t *testing.T) {
	oldChunk, oldWait := ChunkSize, finishWait
	ChunkSize, finishWait = 1000, 20*time.Millisecond
	t.Cleanup(func() { ChunkSize, finishWait = oldChunk, oldWait })
}

func TestUploadFolderKeepsStructure(t *testing.T) {
	setFast(t)
	dir := t.TempDir()
	root := filepath.Join(dir, "Holidays")
	want := map[string]int{
		"a.jpg":           10,
		"b.mp4":           3500, // 4 chunks
		"sub/c.jpg":       1000, // exactly one chunk
		"sub/deep/empty":  0,
		"sub/desktop.ini": 5, // junk, skipped
	}
	for name, size := range want {
		writeFile(t, root, name, size)
	}
	fake := newFake()
	fake.failNext = 2 // transient network errors are retried
	m := New(fake, Options{Workers: 3})
	m.Start()
	defer m.Close()
	n, err := m.Enqueue([]string{root}, "/Photos")
	if err != nil || n != 4 {
		t.Fatalf("enqueued %d, err %v", n, err)
	}
	waitIdle(t, m)
	s := m.Snapshot().Summary
	if s.Done != 4 || s.Failed != 0 {
		t.Fatalf("summary %+v", s)
	}
	for name, size := range want {
		if name == "sub/desktop.ini" {
			continue
		}
		p := "/Photos/Holidays/" + name
		got, ok := fake.files[p]
		if !ok {
			t.Fatalf("missing %s; have %v", p, keys(fake.files))
		}
		orig, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if len(got) != size || !bytes.Equal(got, orig) {
			t.Fatalf("%s: content mismatch (%d bytes)", p, len(got))
		}
	}
}

func TestDuplicateIsSkipped(t *testing.T) {
	setFast(t)
	dir := t.TempDir()
	p := writeFile(t, dir, "photo.jpg", 2500)
	other := writeFile(t, dir, "other.jpg", 2500)
	fake := newFake()
	data, _ := os.ReadFile(p)
	fake.files["/photo.jpg"] = data
	fake.files["/other.jpg"] = []byte("different content")
	m := New(fake, Options{})
	m.Start()
	defer m.Close()
	m.Enqueue([]string{p, other}, "/")
	waitIdle(t, m)
	s := m.Snapshot().Summary
	if s.Skipped != 1 || s.Done != 1 {
		t.Fatalf("summary %+v", s)
	}
	if _, ok := fake.files["/other.jpg (1)"]; !ok {
		t.Fatalf("expected renamed upload, have %v", keys(fake.files))
	}
}

func TestIncorrectOffsetRecovers(t *testing.T) {
	setFast(t)
	dir := t.TempDir()
	p := writeFile(t, dir, "video.mov", 5000)
	fake := newFake()
	fake.wrongOnce = true
	m := New(fake, Options{})
	m.Start()
	defer m.Close()
	m.Enqueue([]string{p}, "/V")
	waitIdle(t, m)
	orig, _ := os.ReadFile(p)
	if !bytes.Equal(fake.files["/V/video.mov"], orig) {
		t.Fatal("content mismatch after offset correction")
	}
}

func TestQueueSurvivesRestartAndResumes(t *testing.T) {
	setFast(t)
	dir := t.TempDir()
	p := writeFile(t, dir, "big.mp4", 4500)
	state := filepath.Join(dir, "queue.json")
	fake := newFake()

	// Simulate an interrupted upload: a session with the first 2 chunks already uploaded.
	sid, _ := fake.StartSession(context.Background(), mustRead(t, p)[:1000], false, nil)
	fake.AppendSession(context.Background(), dbx.Cursor{SessionID: sid, Offset: 1000}, mustRead(t, p)[1000:2000], false, nil)
	m1 := New(fake, Options{StatePath: state})
	m1.Pause()
	m1.Enqueue([]string{p}, "/")
	m1.mu.Lock()
	it := m1.items[0]
	it.SessionID, it.Offset, it.SessionStarted = sid, 2000, time.Now()
	it.Status = Uploading
	m1.saveDirty = true
	m1.mu.Unlock()
	m1.save(true)

	m2 := New(fake, Options{StatePath: state})
	snap := m2.Snapshot()
	if len(snap.Items) != 1 || snap.Items[0].Status != Queued || snap.Items[0].Offset != 2000 {
		t.Fatalf("restored %+v", snap.Items)
	}
	appendsBefore := fake.appends
	m2.Resume()
	m2.Start()
	defer m2.Close()
	waitIdle(t, m2)
	if got := fake.appends - appendsBefore; got != 3 { // chunks 3, 4 and 5 only
		t.Fatalf("expected 3 appends after resume, got %d", got)
	}
	if !bytes.Equal(fake.files["/big.mp4"], mustRead(t, p)) {
		t.Fatal("resumed content mismatch")
	}
}

func TestFailedThenRetry(t *testing.T) {
	setFast(t)
	oldAttempts := chunkAttempts
	chunkAttempts = 1
	defer func() { chunkAttempts = oldAttempts }()
	dir := t.TempDir()
	p := writeFile(t, dir, "x.jpg", 10)
	fake := newFake()
	fake.failNext = 1
	m := New(fake, Options{})
	m.Start()
	defer m.Close()
	m.Enqueue([]string{p}, "/")
	waitIdle(t, m)
	if s := m.Snapshot().Summary; s.Failed != 1 {
		t.Fatalf("expected failure, got %+v", s)
	}
	if e := m.Snapshot().Items[0].Error; e == "" {
		t.Fatal("expected friendly error")
	}
	m.Retry(nil)
	waitIdle(t, m)
	if s := m.Snapshot().Summary; s.Done != 1 {
		t.Fatalf("expected done after retry, got %+v", s)
	}
}

func TestMoveToTopAndRemove(t *testing.T) {
	m := New(newFake(), Options{})
	dir := t.TempDir()
	var paths []string
	for i := 0; i < 4; i++ {
		paths = append(paths, writeFile(t, dir, fmt.Sprintf("f%d", i), 1))
	}
	m.Enqueue(paths, "/")
	m.MoveToTop([]int64{3})
	m.Move(1, 1)
	m.Remove([]int64{4})
	var order []int64
	for _, it := range m.Snapshot().Items {
		order = append(order, it.ID)
	}
	if fmt.Sprint(order) != "[3 2 1]" {
		t.Fatalf("order %v", order)
	}
}

func mustRead(t *testing.T, p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func keys(m map[string][]byte) []string {
	var k []string
	for s := range m {
		k = append(k, s)
	}
	return k
}
