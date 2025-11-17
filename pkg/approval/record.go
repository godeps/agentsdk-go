package approval

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cexll/agentsdk-go/pkg/wal"
)

// Decision captures the lifecycle state of a tool approval.
type Decision string

const (
	DecisionPending  Decision = "pending"
	DecisionApproved Decision = "approved"
	DecisionRejected Decision = "rejected"
	DecisionTimeout  Decision = "timeout"
)

// Record stores a single approval decision for auditing and recovery.
type Record struct {
	ID        string         `json:"id"`
	SessionID string         `json:"session_id"`
	Tool      string         `json:"tool"`
	Params    map[string]any `json:"params,omitempty"`
	Decision  Decision       `json:"decision"`
	Requested time.Time      `json:"requested_at"`
	Decided   *time.Time     `json:"decided_at,omitempty"`
	Comment   string         `json:"comment,omitempty"`
	Auto      bool           `json:"auto,omitempty"`
}

// Filter constrains audit log queries.
type Filter struct {
	SessionID string
	Tool      string
	Decision  Decision
	Since     *time.Time
	Limit     int
}

// Store persists approval records and supports queries.
type Store interface {
	Append(Record) error
	All() []Record
	Query(Filter) []Record
	Close() error
}

// RecordLog is a WAL-backed Store for crash recovery.
type RecordLog struct {
	mu           sync.RWMutex
	wal          *wal.WAL
	records      map[string]Record
	positions    map[string]wal.Position
	entrySize    map[string]int64
	nextPosition wal.Position
	gc           gcController
	gcTicker     *time.Ticker
	gcStop       chan struct{}
	gcDone       chan struct{}
}

const (
	walEntryType           = "approval"
	walEntryMeta           = 4 + 1 + 2 + 4 + 4 // header + crc
	walEntryOverhead int64 = int64(walEntryMeta + len(walEntryType))
)

// NewRecordLog opens (or creates) a WAL rooted at dir.
func NewRecordLog(dir string, opts ...wal.Option) (*RecordLog, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("approval: dir is empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("approval: mkdir %s: %w", dir, err)
	}
	w, err := wal.Open(dir, opts...)
	if err != nil {
		return nil, err
	}
	log := &RecordLog{
		wal:          w,
		records:      map[string]Record{},
		positions:    map[string]wal.Position{},
		entrySize:    map[string]int64{},
		nextPosition: 0,
	}
	log.gc.cfg = defaultGCConfig()
	if err := log.reload(); err != nil {
		_ = w.Close()
		return nil, err
	}
	return log, nil
}

// Append writes the latest version of rec to durable storage.
func (l *RecordLog) Append(rec Record) error {
	if l == nil {
		return errors.New("approval: record log is nil")
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	normalized := cloneRecord(rec)
	data, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	pos, err := l.wal.Append(wal.Entry{Type: walEntryType, Data: data})
	if err != nil {
		return err
	}
	if err := l.wal.Sync(); err != nil {
		return err
	}
	l.records[normalized.ID] = normalized
	l.positions[normalized.ID] = pos
	l.entrySize[normalized.ID] = walEntryOverhead + int64(len(data))
	if pos >= l.nextPosition {
		l.nextPosition = pos + 1
	}
	return nil
}

// All returns the latest decision for every known record.
func (l *RecordLog) All() []Record {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()

	out := make([]Record, 0, len(l.records))
	for _, rec := range l.records {
		out = append(out, cloneRecord(rec))
	}
	return out
}

// Query filters the audit log in-memory; callers hold fresh snapshots via All.
func (l *RecordLog) Query(f Filter) []Record {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	var list []Record
	for _, rec := range l.records {
		if f.SessionID != "" && rec.SessionID != f.SessionID {
			continue
		}
		if f.Tool != "" && rec.Tool != f.Tool {
			continue
		}
		if f.Decision != "" && rec.Decision != f.Decision {
			continue
		}
		if f.Since != nil && rec.Requested.Before(f.Since.UTC()) {
			continue
		}
		list = append(list, cloneRecord(rec))
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Requested.Equal(list[j].Requested) {
			return list[i].ID < list[j].ID
		}
		return list[i].Requested.Before(list[j].Requested)
	})
	if f.Limit > 0 && len(list) > f.Limit {
		list = list[:f.Limit]
	}
	return list
}

// Close flushes and releases underlying WAL resources.
func (l *RecordLog) Close() error {
	if l == nil {
		return nil
	}
	l.StopAutoGC()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.wal == nil {
		return nil
	}
	return l.wal.Close()
}

func (l *RecordLog) reload() error {
	l.records = map[string]Record{}
	l.positions = map[string]wal.Position{}
	l.entrySize = map[string]int64{}
	l.nextPosition = 0
	return l.wal.Replay(func(e wal.Entry) error {
		if e.Type != walEntryType {
			return nil
		}
		var rec Record
		if err := json.Unmarshal(e.Data, &rec); err != nil {
			return fmt.Errorf("approval: decode wal: %w", err)
		}
		l.records[rec.ID] = rec
		l.positions[rec.ID] = e.Position
		l.entrySize[rec.ID] = walEntryOverhead + int64(len(e.Data))
		if e.Position >= l.nextPosition {
			l.nextPosition = e.Position + 1
		}
		return nil
	})
}

// NewMemoryStore returns an in-memory store useful for tests or ephemeral agents.
func NewMemoryStore() Store { return newMemoryStore() }

type memoryStore struct {
	mu      sync.RWMutex
	records map[string]Record
}

func newMemoryStore() *memoryStore {
	return &memoryStore{records: map[string]Record{}}
}

func (m *memoryStore) Append(rec Record) error {
	if m == nil {
		return errors.New("approval: memory store is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[rec.ID] = cloneRecord(rec)
	return nil
}

func (m *memoryStore) All() []Record {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Record, 0, len(m.records))
	for _, rec := range m.records {
		out = append(out, cloneRecord(rec))
	}
	return out
}

func (m *memoryStore) Query(f Filter) []Record {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	var list []Record
	for _, rec := range m.records {
		if f.SessionID != "" && rec.SessionID != f.SessionID {
			continue
		}
		if f.Tool != "" && rec.Tool != f.Tool {
			continue
		}
		if f.Decision != "" && rec.Decision != f.Decision {
			continue
		}
		if f.Since != nil && rec.Requested.Before(f.Since.UTC()) {
			continue
		}
		list = append(list, cloneRecord(rec))
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Requested.Equal(list[j].Requested) {
			return list[i].ID < list[j].ID
		}
		return list[i].Requested.Before(list[j].Requested)
	})
	if f.Limit > 0 && len(list) > f.Limit {
		list = list[:f.Limit]
	}
	return list
}

func (m *memoryStore) Close() error { return nil }

func cloneRecord(rec Record) Record {
	cp := rec
	if rec.Params != nil {
		cp.Params = make(map[string]any, len(rec.Params))
		for k, v := range rec.Params {
			cp.Params[k] = v
		}
	}
	if rec.Decided != nil {
		ts := *rec.Decided
		cp.Decided = &ts
	}
	cp.Requested = rec.Requested.UTC()
	return cp
}

// StartAutoGC launches a background goroutine that periodically runs GC with the given interval.
func (l *RecordLog) StartAutoGC(interval time.Duration) {
	if l == nil {
		return
	}
	l.startAutoGC(interval, false)
}

// StopAutoGC stops the background GC goroutine and releases its ticker resources.
func (l *RecordLog) StopAutoGC() {
	if l == nil {
		return
	}
	l.gc.mu.Lock()
	ticker := l.gcTicker
	stop := l.gcStop
	done := l.gcDone
	if ticker == nil {
		l.gc.cfg.interval = 0
		l.gc.mu.Unlock()
		return
	}
	l.gcTicker = nil
	l.gcStop = nil
	l.gcDone = nil
	l.gc.cfg.interval = 0
	l.gc.mu.Unlock()

	ticker.Stop()
	if stop != nil {
		close(stop)
	}
	if done != nil {
		<-done
	}
}

func (l *RecordLog) startAutoGC(interval time.Duration, force bool) {
	if interval <= 0 {
		l.StopAutoGC()
		return
	}

	l.gc.mu.Lock()
	currentTicker := l.gcTicker
	currentStop := l.gcStop
	currentDone := l.gcDone
	sameInterval := currentTicker != nil && l.gc.cfg.interval == interval
	if sameInterval && !force {
		l.gc.mu.Unlock()
		return
	}
	l.gcTicker = nil
	l.gcStop = nil
	l.gcDone = nil
	l.gc.cfg.interval = interval
	l.gc.mu.Unlock()

	if currentTicker != nil {
		currentTicker.Stop()
		if currentStop != nil {
			close(currentStop)
		}
		if currentDone != nil {
			<-currentDone
		}
	}

	ticker := time.NewTicker(interval)
	stop := make(chan struct{})
	done := make(chan struct{})

	l.gc.mu.Lock()
	if l.gc.cfg.interval != interval || l.gcTicker != nil {
		l.gc.mu.Unlock()
		ticker.Stop()
		close(stop)
		close(done)
		return
	}
	l.gcTicker = ticker
	l.gcStop = stop
	l.gcDone = done
	l.gc.mu.Unlock()

	go l.autoGCLoop(ticker, stop, done)
}
