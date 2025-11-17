package approval

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"
)

// Queue coordinates approval requests, whitelist checks, and persistence.
type Queue struct {
	mu    sync.RWMutex
	store Store

	whitelist *Whitelist
	now       func() time.Time

	index   map[string]Record
	pending map[string]Record
}

// NewQueue restores queue state from store and seed whitelist.
func NewQueue(store Store, wl *Whitelist) *Queue {
	if store == nil {
		store = NewMemoryStore()
	}
	if wl == nil {
		wl = NewWhitelist()
	}
	q := &Queue{
		store:     store,
		whitelist: wl,
		now:       time.Now,
		index:     map[string]Record{},
		pending:   map[string]Record{},
	}
	for _, rec := range store.All() {
		q.index[rec.ID] = cloneRecord(rec)
		switch rec.Decision {
		case DecisionApproved:
			q.whitelist.Add(rec.SessionID, rec.Tool, rec.Params, rec.Requested)
		case DecisionPending:
			q.pending[rec.ID] = cloneRecord(rec)
		}
	}
	return q
}

// Request enqueues a tool invocation for approval. Auto-approved entries skip the queue.
func (q *Queue) Request(sessionID, tool string, params map[string]any) (Record, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	tool = strings.TrimSpace(tool)
	if sessionID == "" {
		return Record{}, false, errors.New("approval: session id required")
	}
	if tool == "" {
		return Record{}, false, errors.New("approval: tool name required")
	}

	normalized := cloneMap(params)

	q.mu.Lock()
	defer q.mu.Unlock()

	if q.whitelist.Allowed(sessionID, tool, normalized) {
		now := q.now().UTC()
		rec := Record{
			ID:        newID(),
			SessionID: sessionID,
			Tool:      tool,
			Params:    normalized,
			Decision:  DecisionApproved,
			Requested: now,
			Decided:   &now,
			Comment:   "whitelisted",
			Auto:      true,
		}
		q.index[rec.ID] = rec
		_ = q.store.Append(rec)
		return rec, true, nil
	}

	rec := Record{
		ID:        newID(),
		SessionID: sessionID,
		Tool:      tool,
		Params:    normalized,
		Decision:  DecisionPending,
		Requested: q.now().UTC(),
	}
	q.index[rec.ID] = rec
	q.pending[rec.ID] = rec
	if err := q.store.Append(rec); err != nil {
		return Record{}, false, err
	}
	return rec, false, nil
}

// Approve marks a pending request as approved and refreshes the session whitelist.
func (q *Queue) Approve(id, comment string) (Record, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	rec, ok := q.pending[id]
	if !ok {
		return Record{}, fmt.Errorf("approval: %s not pending", id)
	}
	now := q.now().UTC()
	rec.Decision = DecisionApproved
	rec.Decided = &now
	if strings.TrimSpace(comment) != "" {
		rec.Comment = comment
	} else {
		rec.Comment = "approved"
	}
	q.index[id] = rec
	delete(q.pending, id)
	q.whitelist.Add(rec.SessionID, rec.Tool, rec.Params, now)
	if err := q.store.Append(rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// Reject records a denial for the pending request.
func (q *Queue) Reject(id, comment string) (Record, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	rec, ok := q.pending[id]
	if !ok {
		return Record{}, fmt.Errorf("approval: %s not pending", id)
	}
	rec.Decision = DecisionRejected
	rec.Decided = ptr(q.now().UTC())
	if strings.TrimSpace(comment) != "" {
		rec.Comment = comment
	} else {
		rec.Comment = "rejected"
	}
	q.index[id] = rec
	delete(q.pending, id)
	if err := q.store.Append(rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// Timeout marks a decision as expired when no reviewer responded in time.
func (q *Queue) Timeout(id string) (Record, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	rec, ok := q.pending[id]
	if !ok {
		return Record{}, fmt.Errorf("approval: %s not pending", id)
	}
	rec.Decision = DecisionTimeout
	rec.Decided = ptr(q.now().UTC())
	rec.Comment = "timeout"
	q.index[id] = rec
	delete(q.pending, id)
	if err := q.store.Append(rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// Pending returns snapshot of unreviewed requests. If sessionID is empty all sessions are returned.
func (q *Queue) Pending(sessionID string) []Record {
	q.mu.RLock()
	defer q.mu.RUnlock()

	var out []Record
	for _, rec := range q.pending {
		if sessionID != "" && rec.SessionID != sessionID {
			continue
		}
		out = append(out, cloneRecord(rec))
	}
	return out
}

// Lookup returns the latest known record by id.
func (q *Queue) Lookup(id string) (Record, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	rec, ok := q.index[id]
	if !ok {
		return Record{}, false
	}
	return cloneRecord(rec), true
}

// Close propagates close to the underlying store when supported.
func (q *Queue) Close() error {
	if q == nil || q.store == nil {
		return nil
	}
	return q.store.Close()
}

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func cloneMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	return maps.Clone(src)
}

func ptr(t time.Time) *time.Time {
	return &t
}
