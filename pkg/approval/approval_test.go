package approval

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQueueApproveAndWhitelist(t *testing.T) {
	dir := t.TempDir()
	store, err := NewRecordLog(dir)
	if err != nil {
		t.Fatalf("new record log: %v", err)
	}
	q := NewQueue(store, NewWhitelist())

	rec, auto, err := q.Request("session-1", "echo", map[string]any{"k": "v"})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if auto {
		t.Fatalf("expected manual approval, got auto")
	}
	if rec.Decision != DecisionPending {
		t.Fatalf("decision = %s", rec.Decision)
	}
	if pending := q.Pending("session-1"); len(pending) != 1 {
		t.Fatalf("pending length = %d", len(pending))
	}

	approved, err := q.Approve(rec.ID, "")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approved.Decision != DecisionApproved {
		t.Fatalf("state after approve = %s", approved.Decision)
	}

	again, auto, err := q.Request("session-1", "echo", map[string]any{"k": "v"})
	if err != nil {
		t.Fatalf("whitelist request: %v", err)
	}
	if !auto {
		t.Fatalf("expected whitelist auto approval")
	}
	if again.Decision != DecisionApproved || !again.Auto {
		t.Fatalf("whitelist decision mismatch: %+v", again)
	}
}

func TestQueueRejectAndTimeout(t *testing.T) {
	q := NewQueue(NewMemoryStore(), NewWhitelist())
	rec, auto, err := q.Request("sess", "tool", map[string]any{"x": 1})
	if err != nil || auto {
		t.Fatalf("request err=%v auto=%v", err, auto)
	}

	denied, err := q.Reject(rec.ID, "nope")
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if denied.Decision != DecisionRejected {
		t.Fatalf("decision=%s", denied.Decision)
	}
	if _, ok := q.Lookup(rec.ID); !ok {
		t.Fatalf("record missing after reject")
	}

	rec2, _, _ := q.Request("sess", "tool", map[string]any{"y": 2})
	timed, err := q.Timeout(rec2.ID)
	if err != nil {
		t.Fatalf("timeout: %v", err)
	}
	if timed.Decision != DecisionTimeout {
		t.Fatalf("decision=%s", timed.Decision)
	}
	if len(q.Pending("")) != 0 {
		t.Fatalf("pending still present")
	}
}

func TestRecordLogRecovery(t *testing.T) {
	dir := t.TempDir()
	store, err := NewRecordLog(dir)
	if err != nil {
		t.Fatalf("log open: %v", err)
	}
	q := NewQueue(store, NewWhitelist())
	rec, _, err := q.Request("sess", "echo", map[string]any{"k": "v"})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if _, err := q.Approve(rec.ID, "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := q.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Re-open to simulate crash recovery.
	store2, err := NewRecordLog(dir)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	q2 := NewQueue(store2, NewWhitelist())
	again, auto, err := q2.Request("sess", "echo", map[string]any{"k": "v"})
	if err != nil {
		t.Fatalf("post-recovery request: %v", err)
	}
	if !auto {
		t.Fatalf("expected whitelist auto approval after recovery")
	}
	if again.Decision != DecisionApproved {
		t.Fatalf("unexpected decision %s", again.Decision)
	}
}

func TestWhitelistDeterministicHash(t *testing.T) {
	w := NewWhitelist()
	a := map[string]any{"b": 2, "a": 1}
	b := map[string]any{"a": 1, "b": 2}
	now := time.Now()
	w.Add("sess", "echo", a, now)
	if !w.Allowed("sess", "echo", b) {
		t.Fatalf("whitelist should ignore map order")
	}
}

func TestHashParamsHandlesNestedSlices(t *testing.T) {
	complex := map[string]any{
		"list": []any{
			[]string{"a", "b"},
			map[string]any{"nested": []any{1, "two"}},
		},
	}
	if got := hashParams(complex); got == "" {
		t.Fatalf("expected digest for nested params")
	} else if got != hashParams(complex) {
		t.Fatalf("hash should be deterministic for nested structures")
	}
}

func TestRecordLogQueryFiltersAndLimit(t *testing.T) {
	dir := t.TempDir()
	log, err := NewRecordLog(dir)
	if err != nil {
		t.Fatalf("record log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	now := time.Now().UTC()
	records := []Record{
		{ID: "a", SessionID: "s1", Tool: "echo", Decision: DecisionApproved, Requested: now.Add(-2 * time.Minute)},
		{ID: "b", SessionID: "s2", Tool: "grep", Decision: DecisionPending, Requested: now.Add(-1 * time.Minute)},
		{ID: "c", SessionID: "s2", Tool: "grep", Decision: DecisionApproved, Requested: now},
	}
	for _, rec := range records {
		if err := log.Append(rec); err != nil {
			t.Fatalf("append %s: %v", rec.ID, err)
		}
	}
	since := now.Add(-90 * time.Second)
	results := log.Query(Filter{
		SessionID: "s2",
		Tool:      "grep",
		Since:     &since,
		Limit:     1,
	})
	if len(results) != 1 || results[0].ID != "b" {
		t.Fatalf("expected earliest matching record due to limit, got %+v", results)
	}
	results = log.Query(Filter{Decision: DecisionApproved})
	if len(results) != 2 || results[len(results)-1].ID != "c" {
		t.Fatalf("expected two approved decisions ending with most recent, got %+v", results)
	}
}

func TestMemoryStoreQuerySortsAndLimits(t *testing.T) {
	store := NewMemoryStore()
	now := time.Now()
	for i := 0; i < 3; i++ {
		rec := Record{
			ID:        fmt.Sprintf("rec-%d", i),
			SessionID: "sess",
			Tool:      "echo",
			Decision:  DecisionApproved,
			Requested: now.Add(time.Duration(i) * time.Minute),
		}
		if err := store.Append(rec); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	limit := 2
	results := store.Query(Filter{Limit: limit})
	if len(results) != limit || results[0].ID != "rec-0" || results[1].ID != "rec-1" {
		t.Fatalf("unexpected query ordering %+v", results)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("memory store close: %v", err)
	}
}

func TestWhitelistSnapshotIsolated(t *testing.T) {
	w := NewWhitelist()
	now := time.Now()
	w.Add("s1", "echo", map[string]any{"x": 1}, now)
	w.Add("s1", "exec", map[string]any{"x": 2}, now)

	snapshot := w.Snapshot()
	if len(snapshot) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(snapshot))
	}
	snapshot[0].SessionID = "mutated"
	if snapshot[0].SessionID == "" {
		t.Fatalf("mutation guard failed")
	}
	if w.Snapshot()[0].SessionID == "mutated" {
		t.Fatalf("snapshot should not share memory with whitelist entries")
	}
}

func TestRecordLogValidationAndClose(t *testing.T) {
	if _, err := NewRecordLog(" "); err == nil {
		t.Fatal("expected empty dir error")
	}
	dir := t.TempDir()
	log, err := NewRecordLog(dir)
	if err != nil {
		t.Fatalf("record log: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	var nilLog *RecordLog
	if err := nilLog.Close(); err != nil {
		t.Fatalf("nil close: %v", err)
	}
	var nilQueue *Queue
	if err := nilQueue.Close(); err != nil {
		t.Fatalf("nil queue close: %v", err)
	}
}

func TestRecordLogAppendNilGuard(t *testing.T) {
	var nilLog *RecordLog
	if err := nilLog.Append(Record{}); err == nil || !strings.Contains(err.Error(), "record log is nil") {
		t.Fatalf("expected nil guard error, got %v", err)
	}
	var nilStore *memoryStore
	if err := nilStore.Append(Record{}); err == nil || !strings.Contains(err.Error(), "memory store is nil") {
		t.Fatalf("expected memory store nil guard, got %v", err)
	}
	if recs := nilLog.All(); recs != nil {
		t.Fatalf("expected nil slice, got %+v", recs)
	}
	if recs := nilStore.Query(Filter{}); recs != nil {
		t.Fatalf("expected nil slice from nil store")
	}
	if err := nilStore.Close(); err != nil {
		t.Fatalf("nil memory store close: %v", err)
	}
}

func TestRecordLogDirValidation(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := NewRecordLog(file); err == nil {
		t.Fatal("expected mkdir failure when file already exists")
	}
}

func TestRecordLogReloadPreservesHistory(t *testing.T) {
	dir := t.TempDir()
	log, err := NewRecordLog(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	rec := Record{ID: "abc", SessionID: "s1", Tool: "echo", Decision: DecisionApproved, Requested: time.Now()}
	if err := log.Append(rec); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	log, err = NewRecordLog(dir)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer log.Close()
	all := log.All()
	if len(all) != 1 || all[0].ID != rec.ID {
		t.Fatalf("replay mismatch: %+v", all)
	}
}

func TestQueueDefaultsAndErrors(t *testing.T) {
	q := NewQueue(nil, nil)
	if q == nil || q.whitelist == nil || q.store == nil {
		t.Fatal("defaults not initialized")
	}
	if _, err := q.Approve("missing", ""); err == nil {
		t.Fatal("expected approve missing error")
	}
	if _, err := q.Reject("missing", ""); err == nil {
		t.Fatal("expected reject missing error")
	}
	if _, err := q.Timeout("missing"); err == nil {
		t.Fatal("expected timeout missing error")
	}
	if _, ok := q.Lookup("missing"); ok {
		t.Fatal("expected missing lookup")
	}
}

func TestNewQueueRestoresPendingAndWhitelist(t *testing.T) {
	now := time.Now().UTC()
	decided := now.Add(time.Minute)
	store := &stubStore{
		records: []Record{
			{ID: "pending", SessionID: "s1", Tool: "rm -rf", Decision: DecisionPending, Requested: now},
			{ID: "approved", SessionID: "s1", Tool: "safe", Decision: DecisionApproved, Requested: now.Add(-time.Minute), Decided: &decided},
		},
	}
	wl := NewWhitelist()
	q := NewQueue(store, wl)
	if len(q.Pending("s1")) != 1 {
		t.Fatalf("expected restored pending request, got %d", len(q.Pending("s1")))
	}
	if !wl.Allowed("s1", "safe", map[string]any{}) {
		t.Fatalf("approved record should seed whitelist")
	}
}

func TestQueueRequestValidationAndStoreError(t *testing.T) {
	q := NewQueue(NewMemoryStore(), NewWhitelist())
	if _, _, err := q.Request("", "echo", nil); err == nil || !strings.Contains(err.Error(), "session id") {
		t.Fatalf("expected session validation error, got %v", err)
	}
	if _, _, err := q.Request("sess", "", nil); err == nil || !strings.Contains(err.Error(), "tool name") {
		t.Fatalf("expected tool validation error, got %v", err)
	}
	store := &stubStore{appendErr: errors.New("boom")}
	q = NewQueue(store, NewWhitelist())
	if _, _, err := q.Request("sess", "echo", nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected store error, got %v", err)
	}
}

type stubStore struct {
	records   []Record
	appendErr error
}

func (s *stubStore) Append(rec Record) error {
	if s.appendErr != nil {
		return s.appendErr
	}
	s.records = append(s.records, cloneRecord(rec))
	return nil
}

func (s *stubStore) All() []Record {
	return append([]Record(nil), s.records...)
}

func (s *stubStore) Query(Filter) []Record { return s.All() }
func (s *stubStore) Close() error          { return nil }
