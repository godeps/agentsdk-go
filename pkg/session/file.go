package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cexll/agentsdk-go/pkg/approval"
	"github.com/cexll/agentsdk-go/pkg/wal"
)

const (
	recordMessage    = "message"
	recordCheckpoint = "checkpoint"
	recordResume     = "resume"
	recordApproval   = "approval"
)

// FileSession persists conversation transcripts through a WAL.
type FileSession struct {
	id      string
	root    string
	dir     string
	walDir  string
	log     *wal.WAL
	walOpts []wal.Option

	mu          sync.RWMutex
	messages    []Message
	checkpoints map[string]*checkpointState
	approvals   map[string]approval.Record
	seq         uint64
	cursors     Cursors
	next        wal.Position
	closed      bool
	now         func() time.Time
}

type checkpointState struct {
	position wal.Position
	payload  Checkpoint
	snapshot []Message
}

// NewFileSession creates (or re-opens) a durable session located at root/id.
func NewFileSession(id, root string, opts ...wal.Option) (*FileSession, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return nil, ErrInvalidSessionID
	}
	sessionDir := filepath.Join(root, trimmed)
	walDir := filepath.Join(sessionDir, "wal")
	if err := os.MkdirAll(walDir, 0o755); err != nil {
		return nil, fmt.Errorf("session: mkdir wal dir: %w", err)
	}
	log, err := wal.Open(walDir, opts...)
	if err != nil {
		return nil, err
	}
	fs := &FileSession{
		id:          trimmed,
		root:        root,
		dir:         sessionDir,
		walDir:      walDir,
		log:         log,
		walOpts:     append([]wal.Option(nil), opts...),
		checkpoints: make(map[string]*checkpointState),
		approvals:   make(map[string]approval.Record),
		cursors:     make(Cursors),
		now:         time.Now,
	}
	if err := fs.reload(); err != nil {
		_ = log.Close()
		return nil, err
	}
	return fs, nil
}

// ID returns the session identifier.
func (s *FileSession) ID() string { return s.id }

// Append appends a message to the persistent transcript.
func (s *FileSession) Append(msg Message) error {
	if strings.TrimSpace(msg.Role) == "" {
		return fmt.Errorf("%w: role is required", ErrInvalidMessage)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSessionClosed
	}

	clone := cloneMessage(msg)
	s.seq++
	if clone.ID == "" {
		clone.ID = fmt.Sprintf("%s-%06d", s.id, s.seq)
	}
	if clone.Timestamp.IsZero() {
		clone.Timestamp = s.now().UTC()
	} else {
		clone.Timestamp = clone.Timestamp.UTC()
	}
	clone.ToolCalls = cloneToolCalls(clone.ToolCalls)

	record := walRecord{
		Kind:    recordMessage,
		Message: &clone,
	}
	if _, err := s.appendRecord(record); err != nil {
		return err
	}
	s.messages = append(s.messages, cloneMessage(clone))
	return nil
}

// AppendApproval persists an approval decision alongside the transcript WAL.
func (s *FileSession) AppendApproval(rec approval.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSessionClosed
	}

	clone := cloneApprovalRecord(rec)
	if strings.TrimSpace(clone.SessionID) == "" {
		clone.SessionID = s.id
	}
	if clone.Requested.IsZero() {
		clone.Requested = s.now().UTC()
	} else {
		clone.Requested = clone.Requested.UTC()
	}
	if clone.Decided != nil {
		decided := clone.Decided.UTC()
		clone.Decided = &decided
	}
	if clone.ID == "" {
		clone.ID = fmt.Sprintf("%s-approval-%06d", s.id, len(s.approvals)+1)
	}

	recWrapper := walRecord{Kind: recordApproval, Approval: &clone}
	if _, err := s.appendRecord(recWrapper); err != nil {
		return err
	}
	s.approvals[clone.ID] = clone
	return nil
}

// ListApprovals returns persisted approval records matching the filter.
func (s *FileSession) ListApprovals(filter approval.Filter) ([]approval.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ErrSessionClosed
	}
	var result []approval.Record
	for _, rec := range s.approvals {
		if filter.SessionID != "" && rec.SessionID != filter.SessionID {
			continue
		}
		if filter.Tool != "" && rec.Tool != filter.Tool {
			continue
		}
		if filter.Decision != "" && rec.Decision != filter.Decision {
			continue
		}
		if filter.Since != nil && rec.Requested.Before(filter.Since.UTC()) {
			continue
		}
		result = append(result, cloneApprovalRecord(rec))
	}
	return result, nil
}

// List returns messages matching the filter.
func (s *FileSession) List(filter Filter) ([]Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ErrSessionClosed
	}
	role := strings.TrimSpace(filter.Role)
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	limit := filter.Limit
	if limit < 0 {
		limit = 0
	}
	var (
		start time.Time
		end   time.Time
	)
	hasStart := filter.StartTime != nil
	if hasStart {
		start = filter.StartTime.UTC()
	}
	hasEnd := filter.EndTime != nil
	if hasEnd {
		end = filter.EndTime.UTC()
	}
	var (
		result  []Message
		skipped int
	)
	for _, msg := range s.messages {
		if role != "" && msg.Role != role {
			continue
		}
		if hasStart && msg.Timestamp.Before(start) {
			continue
		}
		if hasEnd && msg.Timestamp.After(end) {
			continue
		}
		if skipped < offset {
			skipped++
			continue
		}
		result = append(result, cloneMessage(msg))
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

// Checkpoint captures the current transcript for future resuming.
func (s *FileSession) Checkpoint(name string) error {
	normalized, err := normalizeCheckpointName(name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSessionClosed
	}
	snapshot := cloneMessages(s.messages)
	statePayload, err := encodeCheckpointMessages(snapshot)
	if err != nil {
		return err
	}
	if len(statePayload) > MaxCheckpointBytes {
		return fmt.Errorf("%w: %d bytes > %d", ErrCheckpointTooLarge, len(statePayload), MaxCheckpointBytes)
	}
	cp := Checkpoint{
		Name:      normalized,
		Timestamp: s.now().UTC(),
		State:     statePayload,
		Cursors:   s.pendingCursors(recordCheckpoint),
	}
	record := walRecord{
		Kind:       recordCheckpoint,
		Checkpoint: &cp,
	}
	pos, err := s.appendRecord(record)
	if err != nil {
		return err
	}
	s.checkpoints[normalized] = &checkpointState{
		position: pos,
		payload:  cp.Clone(),
		snapshot: snapshot,
	}
	s.gcLocked()
	return nil
}

// Resume rewinds the session to a previously created checkpoint.
func (s *FileSession) Resume(name string) error {
	normalized, err := normalizeCheckpointName(name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSessionClosed
	}
	cp, ok := s.checkpoints[normalized]
	if !ok {
		return fmt.Errorf("%w: %s", ErrCheckpointNotFound, normalized)
	}
	restore := cloneMessages(cp.snapshot)
	record := walRecord{
		Kind:   recordResume,
		Resume: normalized,
	}
	if _, err := s.appendRecord(record); err != nil {
		return err
	}
	s.messages = restore
	s.seq = uint64(len(s.messages))
	s.gcLocked()
	return nil
}

// Fork clones the transcript into a new session rooted at the same directory.
func (s *FileSession) Fork(id string) (Session, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return nil, ErrInvalidSessionID
	}
	s.mu.RLock()
	snapshot := cloneMessages(s.messages)
	s.mu.RUnlock()

	child, err := NewFileSession(trimmed, s.root, s.walOpts...)
	if err != nil {
		return nil, err
	}
	for _, msg := range snapshot {
		if err := child.Append(msg); err != nil {
			_ = child.Close()
			return nil, err
		}
	}
	return child, nil
}

// Close releases underlying resources.
func (s *FileSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.log.Close()
}

func (s *FileSession) appendRecord(rec walRecord) (wal.Position, error) {
	payload, err := json.Marshal(rec)
	if err != nil {
		return 0, err
	}
	pos, err := s.log.Append(wal.Entry{Type: rec.Kind, Data: payload})
	if err != nil {
		return 0, err
	}
	if err := s.log.Sync(); err != nil {
		return 0, err
	}
	if s.cursors == nil {
		s.cursors = make(Cursors)
	}
	s.cursors[channelForKind(rec.Kind)] = pos
	s.next = pos + 1
	return pos, nil
}

func (s *FileSession) reload() error {
	var (
		messages    []Message
		checkpoints = make(map[string]*checkpointState)
		approvals   = make(map[string]approval.Record)
		cursors     = make(Cursors)
		seq         uint64
		lastPos     wal.Position = -1
	)
	err := s.log.Replay(func(e wal.Entry) error {
		var rec walRecord
		if err := json.Unmarshal(e.Data, &rec); err != nil {
			return err
		}
		if e.Position > lastPos {
			lastPos = e.Position
		}
		cursors[channelForKind(rec.Kind)] = e.Position
		switch rec.Kind {
		case recordMessage:
			if rec.Message == nil {
				return fmt.Errorf("session: message record missing payload")
			}
			msg := cloneMessage(*rec.Message)
			msg.Timestamp = msg.Timestamp.UTC()
			messages = append(messages, msg)
			seq++
		case recordCheckpoint:
			if rec.Checkpoint == nil {
				return fmt.Errorf("session: checkpoint payload missing")
			}
			cpSnapshot, err := decodeCheckpointMessages(rec.Checkpoint.State)
			if err != nil {
				return err
			}
			messages = cloneMessages(cpSnapshot)
			seq = uint64(len(messages))
			cp := rec.Checkpoint.Clone()
			cp.Timestamp = cp.Timestamp.UTC()
			checkpoints[cp.Name] = &checkpointState{
				position: e.Position,
				payload:  cp,
				snapshot: cpSnapshot,
			}
		case recordResume:
			name := strings.TrimSpace(rec.Resume)
			if name == "" {
				return fmt.Errorf("session: resume references unknown checkpoint %q", rec.Resume)
			}
			cp, ok := checkpoints[name]
			if !ok {
				return fmt.Errorf("session: resume references unknown checkpoint %s", name)
			}
			messages = cloneMessages(cp.snapshot)
			seq = uint64(len(messages))
		case recordApproval:
			if rec.Approval == nil {
				return fmt.Errorf("session: approval record missing payload")
			}
			cloned := cloneApprovalRecord(*rec.Approval)
			approvals[cloned.ID] = cloned
		default:
			return fmt.Errorf("session: unknown wal record %s", rec.Kind)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.messages = messages
	s.checkpoints = checkpoints
	s.approvals = approvals
	s.seq = seq
	s.cursors = cursors
	if lastPos >= 0 {
		s.next = lastPos + 1
	} else {
		s.next = 0
	}
	return nil
}

func (s *FileSession) pendingCursors(kind string) Cursors {
	c := s.cursors.Clone()
	if c == nil {
		c = make(Cursors)
	}
	pos := s.next
	if pos < 0 {
		pos = 0
	}
	c[channelForKind(kind)] = pos
	return c
}

func (s *FileSession) gcLocked() {
	if len(s.approvals) > 0 {
		// Preserve audit records: approval history should remain intact.
		return
	}
	if len(s.checkpoints) == 0 {
		return
	}
	var earliest *checkpointState
	for _, cp := range s.checkpoints {
		if earliest == nil || cp.position < earliest.position {
			earliest = cp
		}
	}
	if earliest != nil && earliest.position > 0 {
		_ = s.log.Truncate(earliest.position)
	}
}

func cloneApprovalRecord(rec approval.Record) approval.Record {
	clone := rec
	clone.Requested = rec.Requested.UTC()
	if rec.Params != nil {
		clone.Params = make(map[string]any, len(rec.Params))
		for k, v := range rec.Params {
			clone.Params[k] = v
		}
	}
	if rec.Decided != nil {
		ts := rec.Decided.UTC()
		clone.Decided = &ts
	}
	return clone
}

type walRecord struct {
	Kind       string           `json:"kind"`
	Message    *Message         `json:"message,omitempty"`
	Checkpoint *Checkpoint      `json:"checkpoint,omitempty"`
	Resume     string           `json:"resume,omitempty"`
	Approval   *approval.Record `json:"approval,omitempty"`
}

func encodeCheckpointMessages(msgs []Message) (json.RawMessage, error) {
	if len(msgs) == 0 {
		return json.RawMessage([]byte("[]")), nil
	}
	data, err := json.Marshal(msgs)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

func decodeCheckpointMessages(raw json.RawMessage) ([]Message, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var msgs []Message
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return nil, err
	}
	for i := range msgs {
		msgs[i].Timestamp = msgs[i].Timestamp.UTC()
		msgs[i].ToolCalls = cloneToolCalls(msgs[i].ToolCalls)
	}
	return cloneMessages(msgs), nil
}

func channelForKind(kind string) Channel {
	switch kind {
	case recordMessage:
		return ChannelProgress
	case recordCheckpoint, recordResume:
		return ChannelControl
	case recordApproval:
		return ChannelMonitor
	default:
		return ChannelProgress
	}
}

var _ Session = (*FileSession)(nil)
