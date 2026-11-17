// Package transcript keeps one session's agent updates on disk, so a window
// that opens later still shows what the agent did.
//
// The host holds a bounded memory buffer for the gap between clients, which is
// enough to survive a window closing. It is not enough to survive the host: a
// long turn is the reason to hand work to an agent at all, and its record has
// to outlive the process that produced it. The daemon is the only process that
// sees every update exactly once, so this is its store.
package transcript

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// MaxBytes bounds one session's transcript. This is a log to read, not an
// archive: past the bound the oldest lines go and the session keeps working.
const MaxBytes = 1 << 20

// maxLineBytes bounds one stored line. An update's text is capped at an eighth
// of a frame on the wire, and JSON escaping can cost six bytes per byte.
const maxLineBytes = 8 << 20

// ErrDuplicate reports an update the log already holds. A host replays its
// buffer to a daemon that reconnects, so the same update arrives twice; the
// number the host gave it is what tells the two copies apart.
var ErrDuplicate = errors.New("transcript: update already stored")

// ErrTrimmed reports an update the log has moved past: it was stored once, and a
// trim has since dropped the record that held it. There is no sequence left to
// hand back, so a caller cannot pass the copy on as one a client already has —
// and storing it again would place work from before the kept tail after it.
var ErrTrimmed = errors.New("transcript: update was trimmed")

// Record is one stored update and its place in the session's log. Seq starts at
// one and keeps counting across trims, so a client can tell the updates it has
// already from the ones it does not, without knowing how much went before it.
type Record struct {
	Seq uint64 `json:"seq"`
	// HostSeq is the number the host gave the update before sending it. A
	// reconnecting daemon is handed that buffer again, so this is what keeps one
	// paragraph from being written into the log twice.
	HostSeq uint64 `json:"host_seq,omitempty"`
	// Update is the update as the host sent it.
	Update json.RawMessage `json:"update"`
}

// Window selects a page: which updates the caller wants, and how many.
type Window struct {
	// After restricts the page to updates past this sequence. Zero asks from
	// the start of what is still stored.
	After uint64
	// Tail keeps only the last n of the selected updates, which is what a
	// window opening on a long session wants: the recent work, not its first
	// line. Zero keeps all of them.
	Tail int
	// Budget bounds the page by stored bytes; zero means no size bound. One
	// record is always included, so a page always makes a client's request
	// advance.
	Budget int
}

// Page is what a window selected, and whether the log holds more than it.
type Page struct {
	Records []Record
	// More is true when records outside the page exist: older ones trimmed by
	// a Tail or a Budget, or newer ones the caller has not asked for yet.
	More bool
}

// Store keeps transcripts under one directory, one file per session.
type Store struct {
	dir      string
	maxBytes int64

	mu   sync.Mutex
	open map[string]*logFile
}

// logFile is an append handle plus what appending needs to know: how long the
// file is, the next sequence to hand out, and the newest host number stored.
type logFile struct {
	file    *os.File
	size    int64
	next    uint64
	hostSeq uint64
}

// NewStore returns a store rooted at dir. It creates nothing until an update is
// stored.
func NewStore(dir string) *Store {
	return &Store{dir: dir, maxBytes: MaxBytes, open: make(map[string]*logFile)}
}

// Append stores one update and reports the sequence it was given. The update is
// stored as the host sent it: the sequence belongs to the record, not to the
// update, so one stored copy can be handed to a client under any sequence.
//
// An update whose host number the log already holds returns ErrDuplicate and the
// sequence it was stored under, so the caller can hand the same copy on without
// writing it twice.
func (s *Store) Append(id string, hostSeq uint64, update json.RawMessage) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	l, err := s.openLocked(id)
	if err != nil {
		return 0, err
	}

	if hostSeq != 0 && hostSeq <= l.hostSeq {
		seq, ok := seqOfHost(s.path(id), hostSeq)
		if ok {
			return seq, ErrDuplicate
		}
		// The record was trimmed away; the update is still one the log already
		// moved past, so it is refused rather than reinserted out of order.
		return 0, ErrTrimmed
	}

	line, err := json.Marshal(Record{Seq: l.next, HostSeq: hostSeq, Update: update})
	if err != nil {
		return 0, fmt.Errorf("encode an update for session %q: %w", id, err)
	}
	line = append(line, '\n')

	seq := l.next
	n, writeErr := l.file.Write(line)
	l.size += int64(n)
	if writeErr != nil {
		// A handle whose write failed is not trusted again: the next append
		// reopens the file and learns its real length.
		_ = s.closeLocked(id)
		return 0, fmt.Errorf("store an update for session %q: %w", id, writeErr)
	}
	l.next++
	if hostSeq > l.hostSeq {
		l.hostSeq = hostSeq
	}

	if l.size <= s.maxBytes {
		return seq, nil
	}
	return seq, s.trimLocked(id)
}

// Page returns the stored updates w selects, oldest first.
func (s *Store) Page(id string, w Window) (Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return readFile(s.path(id), w)
}

// LastHostSeq reports the largest host number the session's log holds, or zero
// for an empty log. A reused session id numbers past it; from one again, its
// first update would look like a replay of the old run's.
func (s *Store) LastHostSeq(id string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	records, err := scan(s.path(id), func([]byte) bool { return true })
	if err != nil {
		return 0, err
	}

	var newest uint64
	for _, rec := range records {
		if rec.HostSeq > newest {
			newest = rec.HostSeq
		}
	}
	return newest, nil
}

// Release closes the handle the store holds for one session. The log stays on
// disk: a session that ended is exactly the one somebody may come back to read.
func (s *Store) Release(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.closeLocked(id)
}

// Close releases every open handle. A store that is never closed still works:
// the files it left open are closed by the process exiting.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var errs []error
	for id := range s.open {
		if err := s.closeLocked(id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Store) openLocked(id string) (*logFile, error) {
	if l, ok := s.open[id]; ok {
		return l, nil
	}

	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, fmt.Errorf("create the transcript directory: %w", err)
	}

	path := s.path(id)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open the transcript of session %q: %w", id, err)
	}

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("measure the transcript of session %q: %w", id, err)
	}

	// Both counters continue what the file already holds. A daemon restart
	// reopens this file, and resuming either at zero would make two updates share
	// a number a client or a replay needs to tell apart.
	last, lastHost, err := tail(path)
	if err != nil {
		_ = file.Close()
		return nil, err
	}

	l := &logFile{file: file, size: info.Size(), next: last + 1, hostSeq: lastHost}
	s.open[id] = l
	return l, nil
}

func (s *Store) closeLocked(id string) error {
	l, ok := s.open[id]
	if !ok {
		return nil
	}
	delete(s.open, id)

	if err := l.file.Close(); err != nil {
		return fmt.Errorf("close the transcript of session %q: %w", id, err)
	}
	return nil
}

// trimLocked drops the oldest half of an oversized log, cutting on a line
// boundary so every kept line is a whole record.
func (s *Store) trimLocked(id string) error {
	path := s.path(id)

	// Windows will not replace a file this process still holds open.
	_ = s.closeLocked(id)

	payload, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read the transcript of session %q to trim it: %w", id, err)
	}

	cut := len(payload) - int(s.maxBytes/2)
	if cut < 0 {
		cut = 0
	}
	for cut < len(payload) && payload[cut] != '\n' {
		cut++
	}
	if cut < len(payload) {
		cut++
	}

	tmp, err := os.CreateTemp(s.dir, ".transcript-*.jsonl")
	if err != nil {
		return fmt.Errorf("create a temporary transcript: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(payload[cut:]); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write the trimmed transcript of session %q: %w", id, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close the trimmed transcript of session %q: %w", id, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace the transcript of session %q: %w", id, err)
	}
	return nil
}

// tail reports the sequence and the host number of the last whole record, or
// zeros for an empty or missing log.
func tail(path string) (seq, hostSeq uint64, err error) {
	records, err := scan(path, func([]byte) bool { return true })
	if err != nil || len(records) == 0 {
		return 0, 0, err
	}
	last := records[len(records)-1]
	return last.Seq, last.HostSeq, nil
}

// seqOfHost finds the sequence an update with this host number was stored
// under. It runs only for an update that is already in the log, which is a
// replay and therefore rare.
func seqOfHost(path string, hostSeq uint64) (uint64, bool) {
	records, err := scan(path, func(line []byte) bool {
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			return false
		}
		return rec.HostSeq == hostSeq
	})
	if err != nil || len(records) == 0 {
		return 0, false
	}
	return records[0].Seq, true
}

// readFile decodes the records of one transcript, skipping lines that do not
// decode. A crash mid-append leaves a partial last line; the rest of the log is
// still true, so one bad line is skipped rather than failing the read.
//
// A Tail or a Budget is applied while reading rather than by loading the log
// and slicing it, so the page never holds more than it will answer with.
func readFile(path string, w Window) (Page, error) {
	records, err := scan(path, func(line []byte) bool {
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			return false
		}
		return rec.Seq > w.After
	})
	if err != nil {
		return Page{}, err
	}

	kept := records
	more := false

	if w.Budget > 0 {
		size := 0
		for i := len(kept) - 1; i >= 0; i-- {
			size += kept[i].lineLen
			if size > w.Budget && len(kept)-i > 1 {
				kept = kept[i+1:]
				more = true
				break
			}
		}
	}
	if w.Tail > 0 && len(kept) > w.Tail {
		kept = kept[len(kept)-w.Tail:]
		more = true
	}

	out := make([]Record, 0, len(kept))
	for _, rec := range kept {
		out = append(out, rec.Record)
	}
	return Page{Records: out, More: more}, nil
}

// sizedRecord is a record with the length it occupied in the file, which is
// what a byte budget is about.
type sizedRecord struct {
	Record
	lineLen int
}

// scan reads whole records from one transcript, keeping those keep accepts.
func scan(path string, keep func([]byte) bool) ([]sizedRecord, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read transcript %q: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	var out []sizedRecord

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLineBytes)

	for scanner.Scan() {
		line := scanner.Bytes()
		if !keep(line) {
			continue
		}

		// The scanner reuses its buffer, so a kept line is decoded now rather
		// than referenced later.
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		out = append(out, sizedRecord{Record: rec, lineLen: len(line)})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan transcript %q: %w", path, err)
	}
	return out, nil
}

// path names a session's file. The id is arbitrary text, so it is encoded
// rather than filtered: an encoding cannot leave the directory and cannot
// collide with another id the way a character replacement can.
func (s *Store) path(id string) string {
	return filepath.Join(s.dir, hex.EncodeToString([]byte(id))+".jsonl")
}
