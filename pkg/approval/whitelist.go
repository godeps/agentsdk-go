package approval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Entry captures one whitelist admission scoped to a session and tool+params.
type Entry struct {
	SessionID string
	Tool      string
	Signature string
	CreatedAt time.Time
}

// Whitelist caches approvals within a session to avoid duplicate prompts.
type Whitelist struct {
	mu      sync.RWMutex
	entries map[string]Entry
}

// NewWhitelist constructs an empty whitelist.
func NewWhitelist() *Whitelist {
	return &Whitelist{entries: map[string]Entry{}}
}

// Allowed reports whether the exact tool+params has already been approved in this session.
func (w *Whitelist) Allowed(sessionID, tool string, params map[string]any) bool {
	key := w.key(sessionID, tool, params)
	w.mu.RLock()
	_, ok := w.entries[key]
	w.mu.RUnlock()
	return ok
}

// Add records a new whitelist admission while remaining idempotent.
func (w *Whitelist) Add(sessionID, tool string, params map[string]any, now time.Time) Entry {
	key := w.key(sessionID, tool, params)
	entry := Entry{SessionID: sessionID, Tool: tool, Signature: key, CreatedAt: now.UTC()}
	w.mu.Lock()
	if _, exists := w.entries[key]; !exists {
		w.entries[key] = entry
	}
	w.mu.Unlock()
	return entry
}

// Snapshot returns a copy of all whitelist entries.
func (w *Whitelist) Snapshot() []Entry {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]Entry, 0, len(w.entries))
	for _, e := range w.entries {
		out = append(out, e)
	}
	return out
}

func (w *Whitelist) key(sessionID, tool string, params map[string]any) string {
	// Use tool name + deterministic hash over params for session-level uniqueness.
	buf := bytes.NewBufferString(sessionID)
	buf.WriteString("|")
	buf.WriteString(tool)
	buf.WriteString("|")
	buf.WriteString(hashParams(params))
	return buf.String()
}

func hashParams(params map[string]any) string {
	if len(params) == 0 {
		return "empty"
	}
	var buf bytes.Buffer
	encodeValue(&buf, params)
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:])
}

// encodeValue produces deterministic traversal over maps and slices so hashes are stable.
func encodeValue(buf *bytes.Buffer, v any) {
	switch val := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteString("{")
		for _, k := range keys {
			buf.WriteString(k)
			buf.WriteString(":")
			encodeValue(buf, val[k])
			buf.WriteString(";")
		}
		buf.WriteString("}")
	case []any:
		buf.WriteString("[")
		for _, item := range val {
			encodeValue(buf, item)
			buf.WriteByte(',')
		}
		buf.WriteString("]")
	case []string:
		buf.WriteString("[")
		for _, item := range val {
			buf.WriteString(item)
			buf.WriteByte(',')
		}
		buf.WriteString("]")
	default:
		fmt.Fprintf(buf, "%v", val)
	}
}
