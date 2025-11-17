package event

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/cexll/agentsdk-go/pkg/session"
	"github.com/cexll/agentsdk-go/pkg/wal"
)

var (
	errStoreClosed     = errors.New("event: store closed")
	errStoreNil        = errors.New("event: store is nil")
	errMissingBookmark = errors.New("event: bookmark missing on event")
)

const legacyEnvVar = "EVENT_STORE_LEGACY"

// FileEventStore 使用 WAL 提供 crash-safe 事件持久化，并在必要时降级到 JSONL 实现。
type FileEventStore struct {
	mu           sync.RWMutex
	path         string
	walRoot      string
	wal          *session.WAL
	legacy       *legacyFileStore
	useLegacy    bool
	closed       bool
	lastBookmark *Bookmark
}

// NewFileEventStore 创建事件存储，优先使用 WAL，不可用时降级为 JSONL。
func NewFileEventStore(path string) (*FileEventStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("event: file store path is empty")
	}
	if legacyModeEnabled() {
		legacy, err := newLegacyFileStore(path)
		if err != nil {
			return nil, err
		}
		return &FileEventStore{path: path, legacy: legacy, useLegacy: true}, nil
	}

	walDir := path + ".wal"
	walStore, err := session.NewWAL(walDir)
	if err != nil {
		legacy, legacyErr := newLegacyFileStore(path)
		if legacyErr != nil {
			return nil, err
		}
		return &FileEventStore{path: path, legacy: legacy, useLegacy: true}, nil
	}

	store := &FileEventStore{
		path:    path,
		walRoot: walDir,
		wal:     walStore,
	}
	if err := store.bootstrapLegacy(); err != nil {
		_ = walStore.Close()
		return nil, err
	}
	if err := store.refreshLastBookmark(); err != nil {
		_ = walStore.Close()
		return nil, err
	}
	return store, nil
}

// Append 追加事件到持久化日志。
func (s *FileEventStore) Append(evt Event) error {
	if s == nil {
		return errStoreNil
	}
	if evt.Bookmark == nil {
		return errMissingBookmark
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errStoreClosed
	}
	if s.useLegacy {
		return s.legacy.Append(evt)
	}
	normalized := normalizeEvent(evt)
	if err := s.appendWALLocked(normalized); err != nil {
		return err
	}
	s.updateLastBookmarkLocked(normalized.Bookmark)
	return nil
}

// ReadSince 返回大于 bookmark 的所有事件。
func (s *FileEventStore) ReadSince(bookmark *Bookmark) ([]Event, error) {
	if s == nil {
		return nil, errStoreNil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.useLegacy {
		return s.legacy.ReadSince(bookmark)
	}
	if s.closed {
		return nil, errStoreClosed
	}
	events, err := s.walEventsLocked()
	if err != nil {
		return nil, err
	}
	return filterEvents(events, bookmark, nil), nil
}

// ReadRange 返回 (start, end] 区间内的事件。
func (s *FileEventStore) ReadRange(start, end *Bookmark) ([]Event, error) {
	if s == nil {
		return nil, errStoreNil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.useLegacy {
		return s.legacy.ReadRange(start, end)
	}
	if s.closed {
		return nil, errStoreClosed
	}
	events, err := s.walEventsLocked()
	if err != nil {
		return nil, err
	}
	return filterEvents(events, start, end), nil
}

// LastBookmark 返回最新的书签。
func (s *FileEventStore) LastBookmark() (*Bookmark, error) {
	if s == nil {
		return nil, errStoreNil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.useLegacy {
		return s.legacy.LastBookmark()
	}
	if s.lastBookmark == nil {
		return nil, nil
	}
	return s.lastBookmark.Clone(), nil
}

// Close 关闭存储资源。
func (s *FileEventStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.useLegacy {
		return s.legacy.Close()
	}
	if s.wal != nil {
		return s.wal.Close()
	}
	return nil
}

func (s *FileEventStore) appendWALLocked(evt Event) error {
	ch, ok := channelForType(evt.Type)
	if !ok {
		return fmt.Errorf("event: unknown type %q", evt.Type)
	}
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("event: marshal wal entry: %w", err)
	}
	if _, err := s.wal.Append(convertChannel(ch), wal.Entry{Type: string(evt.Type), Data: payload}); err != nil {
		return err
	}
	return s.wal.Sync(convertChannel(ch))
}

func (s *FileEventStore) walEventsLocked() ([]Event, error) {
	var events []Event
	for _, ch := range []session.Channel{session.ChannelProgress, session.ChannelControl, session.ChannelMonitor} {
		err := s.wal.ReadSince(ch, 0, func(entry wal.Entry) error {
			var evt Event
			if err := json.Unmarshal(entry.Data, &evt); err != nil {
				return nil
			}
			events = append(events, evt)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(events, func(i, j int) bool {
		var seqI, seqJ int64
		if events[i].Bookmark != nil {
			seqI = events[i].Bookmark.Seq
		}
		if events[j].Bookmark != nil {
			seqJ = events[j].Bookmark.Seq
		}
		if seqI == seqJ {
			return events[i].Timestamp.Before(events[j].Timestamp)
		}
		return seqI < seqJ
	})
	return events, nil
}

func (s *FileEventStore) bootstrapLegacy() error {
	if s.useLegacy {
		return nil
	}
	if _, err := os.Stat(s.path); err != nil {
		return nil
	}
	snapshot := s.wal.Snapshot()
	if len(snapshot) > 0 {
		return nil
	}
	events, err := readLegacyFile(s.path)
	if err != nil {
		return err
	}
	for _, evt := range events {
		if evt.Bookmark == nil {
			continue
		}
		if err := s.appendWALLocked(evt); err != nil {
			return err
		}
		s.updateLastBookmarkLocked(evt.Bookmark)
	}
	return nil
}

func (s *FileEventStore) refreshLastBookmark() error {
	if s.useLegacy {
		return nil
	}
	events, err := s.walEventsLocked()
	if err != nil {
		return err
	}
	var max *Bookmark
	for _, evt := range events {
		if evt.Bookmark == nil {
			continue
		}
		if max == nil || evt.Bookmark.Seq > max.Seq {
			max = evt.Bookmark.Clone()
		}
	}
	s.lastBookmark = max
	return nil
}

func (s *FileEventStore) updateLastBookmarkLocked(bm *Bookmark) {
	if bm == nil {
		return
	}
	if s.lastBookmark == nil || bm.Seq >= s.lastBookmark.Seq {
		s.lastBookmark = bm.Clone()
	}
}

func filterEvents(events []Event, start, end *Bookmark) []Event {
	var filtered []Event
	for _, evt := range events {
		bm := evt.Bookmark
		if bm == nil {
			continue
		}
		if start != nil && bm.Seq <= start.Seq {
			continue
		}
		if end != nil && bm.Seq > end.Seq {
			break
		}
		filtered = append(filtered, copyEvent(evt))
	}
	return filtered
}

func convertChannel(ch Channel) session.Channel {
	switch ch {
	case ChannelProgress:
		return session.ChannelProgress
	case ChannelControl:
		return session.ChannelControl
	case ChannelMonitor:
		return session.ChannelMonitor
	default:
		return session.ChannelProgress
	}
}

func copyEvent(evt Event) Event {
	cloned := evt
	if evt.Bookmark != nil {
		cloned.Bookmark = evt.Bookmark.Clone()
	}
	return cloned
}

func legacyModeEnabled() bool {
	val := strings.TrimSpace(os.Getenv(legacyEnvVar))
	return strings.EqualFold(val, "1") || strings.EqualFold(val, "true")
}

// ===== Legacy JSONL implementation =====

type legacyFileStore struct {
	mu   sync.RWMutex
	path string
	file *os.File
}

func newLegacyFileStore(path string) (*legacyFileStore, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("event: create dir: %w", err)
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("event: open store: %w", err)
	}
	return &legacyFileStore{path: path, file: file}, nil
}

func (s *legacyFileStore) Append(evt Event) error {
	if s == nil {
		return errStoreNil
	}
	if evt.Bookmark == nil {
		return errMissingBookmark
	}
	data, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("event: marshal event: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return errStoreClosed
	}
	if _, err := s.file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("event: append: %w", err)
	}
	if err := s.file.Sync(); err != nil {
		return fmt.Errorf("event: sync: %w", err)
	}
	return nil
}

func (s *legacyFileStore) ReadSince(bookmark *Bookmark) ([]Event, error) {
	events, err := s.readAll()
	if err != nil {
		return nil, err
	}
	if bookmark == nil {
		return events, nil
	}
	filtered := make([]Event, 0, len(events))
	for _, evt := range events {
		if evt.Bookmark == nil {
			continue
		}
		if compareBookmark(bookmark, evt.Bookmark) < 0 {
			filtered = append(filtered, evt)
		}
	}
	return filtered, nil
}

func (s *legacyFileStore) ReadRange(start, end *Bookmark) ([]Event, error) {
	events, err := s.readAll()
	if err != nil {
		return nil, err
	}
	filtered := make([]Event, 0, len(events))
	for _, evt := range events {
		bm := evt.Bookmark
		if bm == nil {
			continue
		}
		if start != nil && compareBookmark(bm, start) <= 0 {
			continue
		}
		if end != nil && compareBookmark(bm, end) > 0 {
			break
		}
		filtered = append(filtered, evt)
	}
	return filtered, nil
}

func (s *legacyFileStore) LastBookmark() (*Bookmark, error) {
	events, err := s.readAll()
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}
	last := events[len(events)-1]
	if last.Bookmark == nil {
		return nil, nil
	}
	return last.Bookmark.Clone(), nil
}

func (s *legacyFileStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

func (s *legacyFileStore) readAll() ([]Event, error) {
	if s == nil {
		return nil, errStoreNil
	}
	s.mu.RLock()
	path := s.path
	s.mu.RUnlock()
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("event: read store: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1<<20)
	var events []Event
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var evt Event
		if err := json.Unmarshal(line, &evt); err != nil {
			continue
		}
		events = append(events, evt)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("event: scan store: %w", err)
	}
	return events, nil
}

func readLegacyFile(path string) ([]Event, error) {
	store, err := newLegacyFileStore(path)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	return store.readAll()
}
