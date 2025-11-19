package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cexll/agentsdk-go/pkg/model"
	"github.com/cexll/agentsdk-go/pkg/tool"
)

type stubClock struct {
	mu      sync.Mutex
	current time.Time
	step    time.Duration
}

func newStubClock(start time.Time, step time.Duration) *stubClock {
	return &stubClock{current: start, step: step}
}

func (s *stubClock) Now() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.current
	s.current = s.current.Add(s.step)
	return next
}

type stubStringer string

func (s stubStringer) String() string { return string(s) }

func newTraceMiddlewareForTest(t *testing.T) *TraceMiddleware {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "trace-out")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})

	mw := NewTraceMiddleware(dir)
	clock := newStubClock(time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC), time.Second)
	mw.clock = clock.Now

	t.Cleanup(func() {
		mw.mu.Lock()
		defer mw.mu.Unlock()
		for _, sess := range mw.sessions {
			sess.mu.Lock()
			if sess.jsonFile != nil {
				_ = sess.jsonFile.Close()
			}
			sess.mu.Unlock()
		}
	})

	return mw
}

func getSession(t *testing.T, mw *TraceMiddleware, id string) *traceSession {
	t.Helper()
	mw.mu.Lock()
	defer mw.mu.Unlock()
	sess, ok := mw.sessions[id]
	if !ok {
		t.Fatalf("session %s not found", id)
	}
	return sess
}

func snapshotSession(t *testing.T, sess *traceSession) (jsonPath, htmlPath string, events []TraceEvent) {
	t.Helper()
	sess.mu.Lock()
	defer sess.mu.Unlock()
	jsonPath = sess.jsonPath
	htmlPath = sess.htmlPath
	events = append([]TraceEvent(nil), sess.events...)
	return
}

func assertJSONLValid(t *testing.T, path string, want int) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		if want != 0 {
			t.Fatalf("jsonl %s is empty", path)
		}
		return nil
	}
	lines := strings.Split(text, "\n")
	events := make([]map[string]any, 0, len(lines))
	for idx, line := range lines {
		var payload map[string]any
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			t.Fatalf("line %d invalid json: %v", idx, err)
		}
		events = append(events, payload)
	}
	if want >= 0 && len(events) != want {
		t.Fatalf("jsonl %s lines=%d want=%d", path, len(events), want)
	}
	return events
}

func assertHTMLContains(t *testing.T, path, needle string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read html %s: %v", path, err)
	}
	if !strings.Contains(string(raw), needle) {
		t.Fatalf("html %s missing %q", path, needle)
	}
}

func TestNewTraceMiddlewareCreatesDirectory(t *testing.T) {
	cases := []struct {
		name    string
		dirFunc func(t *testing.T) (string, string)
		verify  func(t *testing.T, mw *TraceMiddleware, expect string)
	}{
		{
			name: "custom output directory",
			dirFunc: func(t *testing.T) (string, string) {
				root := t.TempDir()
				dir := filepath.Join(root, "trace-custom")
				return fmt.Sprintf("  %s  ", dir), dir
			},
			verify: func(t *testing.T, mw *TraceMiddleware, expect string) {
				if mw.outputDir != expect {
					t.Fatalf("output dir mismatch: %s", mw.outputDir)
				}
				if _, err := os.Stat(expect); err != nil {
					t.Fatalf("expected directory: %v", err)
				}
			},
		},
		{
			name: "default dot trace",
			dirFunc: func(t *testing.T) (string, string) {
				root := t.TempDir()
				prev, err := os.Getwd()
				if err != nil {
					t.Fatalf("getwd: %v", err)
				}
				if err := os.Chdir(root); err != nil {
					t.Fatalf("chdir: %v", err)
				}
				t.Cleanup(func() {
					if err := os.Chdir(prev); err != nil {
						t.Errorf("cleanup chdir: %v", err)
					}
				})
				return "", filepath.Join(root, ".trace")
			},
			verify: func(t *testing.T, mw *TraceMiddleware, expect string) {
				if !strings.HasSuffix(mw.outputDir, ".trace") {
					t.Fatalf("expected default dir, got %s", mw.outputDir)
				}
				if _, err := os.Stat(expect); err != nil {
					t.Fatalf("default dir missing: %v", err)
				}
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			dirArg, expect := tc.dirFunc(t)
			mw := NewTraceMiddleware(dirArg)
			if mw == nil {
				t.Fatalf("middleware nil")
			}
			if mw.Name() != "trace" {
				t.Fatalf("unexpected name: %s", mw.Name())
			}
			if mw.sessions == nil {
				t.Fatalf("sessions map not initialized")
			}
			tc.verify(t, mw, expect)
		})
	}
}

func TestTraceMiddlewareRecordsStages(t *testing.T) {
	t.Parallel()

	type stageCase struct {
		name   string
		stage  Stage
		invoke func(context.Context, *TraceMiddleware, *State) error
		build  func() (context.Context, *State, string)
		mutate func(*TraceMiddleware)
		assert func(*testing.T, TraceEvent)
	}

	cases := []stageCase{
		{
			name:  "before_agent",
			stage: StageBeforeAgent,
			invoke: func(ctx context.Context, mw *TraceMiddleware, st *State) error {
				return mw.BeforeAgent(ctx, st)
			},
			build: func() (context.Context, *State, string) {
				st := &State{
					Iteration: 1,
					Agent:     stubStringer("agent-ready"),
					Values:    map[string]any{"trace.session_id": "stage-before-agent"},
				}
				return context.Background(), st, "stage-before-agent"
			},
			assert: func(t *testing.T, evt TraceEvent) {
				t.Helper()
				if fmt.Sprint(evt.Input) != "agent-ready" {
					t.Fatalf("before_agent input mismatch: %#v", evt.Input)
				}
				if evt.Output != nil {
					t.Fatalf("before_agent output should be nil")
				}
			},
		},
		{
			name:  "before_model",
			stage: StageBeforeModel,
			invoke: func(ctx context.Context, mw *TraceMiddleware, st *State) error {
				return mw.BeforeModel(ctx, st)
			},
			build: func() (context.Context, *State, string) {
				session := "stage-before-model"
				st := &State{
					Iteration:  2,
					ModelInput: []byte(`{"prompt":"hi"}`),
					Values:     map[string]any{"session_id": []byte(session)},
				}
				return context.Background(), st, session
			},
			assert: func(t *testing.T, evt TraceEvent) {
				t.Helper()
				raw, ok := evt.Input.(json.RawMessage)
				if !ok || string(raw) != `{"prompt":"hi"}` {
					t.Fatalf("before_model input mismatch: %#v", evt.Input)
				}
			},
		},
		{
			name:  "after_model",
			stage: StageAfterModel,
			invoke: func(ctx context.Context, mw *TraceMiddleware, st *State) error {
				return mw.AfterModel(ctx, st)
			},
			build: func() (context.Context, *State, string) {
				session := "stage-after-model"
				st := &State{
					Iteration:   3,
					ModelInput:  map[string]any{"k": "v"},
					ModelOutput: errors.New("model failure"),
					Values:      map[string]any{"sessionID": stubStringer(session)},
				}
				return context.Background(), st, session
			},
			assert: func(t *testing.T, evt TraceEvent) {
				t.Helper()
				if got, ok := evt.Input.(map[string]any); !ok || got["k"] != "v" {
					t.Fatalf("after_model input mismatch: %#v", evt.Input)
				}
				if got, ok := evt.Output.(string); !ok || got != "model failure" {
					t.Fatalf("after_model output mismatch: %#v", evt.Output)
				}
			},
		},
		{
			name:  "before_tool",
			stage: StageBeforeTool,
			invoke: func(ctx context.Context, mw *TraceMiddleware, st *State) error {
				return mw.BeforeTool(ctx, st)
			},
			build: func() (context.Context, *State, string) {
				session := "stage-before-tool"
				st := &State{
					Iteration: 4,
					ToolCall:  []byte(" raw-params "),
					Values:    map[string]any{"session": []byte(session)},
				}
				return context.Background(), st, session
			},
			assert: func(t *testing.T, evt TraceEvent) {
				t.Helper()
				got, ok := evt.Input.(string)
				if !ok || strings.TrimSpace(got) != "raw-params" {
					t.Fatalf("before_tool input mismatch: %#v", evt.Input)
				}
			},
		},
		{
			name:  "after_tool",
			stage: StageAfterTool,
			invoke: func(ctx context.Context, mw *TraceMiddleware, st *State) error {
				return mw.AfterTool(ctx, st)
			},
			build: func() (context.Context, *State, string) {
				session := "stage-after-tool"
				st := &State{
					Iteration:  5,
					ToolCall:   map[string]any{"name": "do"},
					ToolResult: make(chan int),
				}
				ctx := context.WithValue(context.Background(), TraceSessionIDContextKey, session)
				return ctx, st, session
			},
			assert: func(t *testing.T, evt TraceEvent) {
				t.Helper()
				if _, ok := evt.Input.(map[string]any); !ok {
					t.Fatalf("after_tool input mismatch: %#v", evt.Input)
				}
				if got, ok := evt.Output.(string); !ok || !strings.Contains(got, "chan int") {
					t.Fatalf("after_tool output mismatch: %#v", evt.Output)
				}
			},
		},
		{
			name:  "after_agent",
			stage: StageAfterAgent,
			invoke: func(ctx context.Context, mw *TraceMiddleware, st *State) error {
				return mw.AfterAgent(ctx, st)
			},
			build: func() (context.Context, *State, string) {
				st := &State{
					Iteration:   6,
					Agent:       map[string]string{"id": "agent"},
					ModelOutput: []byte(`{"ok":true}`),
				}
				sessionID := fmt.Sprintf("session-%p", st)
				return context.Background(), st, sessionID
			},
			mutate: func(mw *TraceMiddleware) {
				mw.tmpl = nil
			},
			assert: func(t *testing.T, evt TraceEvent) {
				t.Helper()
				if _, ok := evt.Input.(map[string]string); !ok {
					t.Fatalf("after_agent input mismatch: %#v", evt.Input)
				}
				raw, ok := evt.Output.(json.RawMessage)
				if !ok || string(raw) != `{"ok":true}` {
					t.Fatalf("after_agent output mismatch: %#v", evt.Output)
				}
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mw := newTraceMiddlewareForTest(t)
			if tc.mutate != nil {
				tc.mutate(mw)
			}
			ctx, st, sessionID := tc.build()
			if err := tc.invoke(ctx, mw, st); err != nil {
				t.Fatalf("stage %s error: %v", tc.name, err)
			}
			sess := getSession(t, mw, sessionID)
			jsonPath, htmlPath, events := snapshotSession(t, sess)
			if len(events) != 1 {
				t.Fatalf("expected 1 event, got %d", len(events))
			}
			evt := events[0]
			if evt.Stage != stageName(tc.stage) {
				t.Fatalf("stage name mismatch: %s", evt.Stage)
			}
			if evt.Iteration != st.Iteration {
				t.Fatalf("iteration mismatch: %d vs %d", evt.Iteration, st.Iteration)
			}
			if evt.SessionID != sessionID {
				t.Fatalf("session mismatch: %s vs %s", evt.SessionID, sessionID)
			}
			tc.assert(t, evt)
			assertJSONLValid(t, jsonPath, 1)
			assertHTMLContains(t, htmlPath, sessionID)
		})
	}
}

func TestTraceMiddlewareCapturesEnrichedFields(t *testing.T) {
	mw := newTraceMiddlewareForTest(t)
	temp := 0.2
	req := &model.Request{
		Messages:    []model.Message{{Role: "user", Content: "ping"}},
		Tools:       []model.ToolDefinition{{Name: "bash"}},
		System:      "system:core",
		MaxTokens:   64,
		Model:       "claude-test",
		Temperature: &temp,
	}
	state := &State{
		Iteration:  1,
		Values:     map[string]any{"trace.session_id": "enriched"},
		ModelInput: req,
	}
	if err := mw.BeforeModel(context.Background(), state); err != nil {
		t.Fatalf("before_model: %v", err)
	}
	state.ModelOutput = &model.Response{
		Message:    model.Message{Content: "pong"},
		Usage:      model.Usage{TotalTokens: 42},
		StopReason: "end_turn",
	}
	state.Values["model.stream"] = []string{"delta"}
	if err := mw.AfterModel(context.Background(), state); err != nil {
		t.Fatalf("after_model: %v", err)
	}
	state.ToolCall = tool.Call{Name: "bash", Params: map[string]any{"cmd": "ls"}, Host: "localhost"}
	if err := mw.BeforeTool(context.Background(), state); err != nil {
		t.Fatalf("before_tool: %v", err)
	}
	state.ToolResult = &tool.CallResult{
		Call:   tool.Call{Name: "bash"},
		Result: &tool.ToolResult{Output: "ok"},
		Err:    errors.New("boom"),
	}
	if err := mw.AfterTool(context.Background(), state); err != nil {
		t.Fatalf("after_tool: %v", err)
	}

	sess := getSession(t, mw, "enriched")
	_, _, events := snapshotSession(t, sess)
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d", len(events))
	}
	afterModel := events[1]
	if afterModel.ModelRequest == nil || afterModel.ModelRequest["messages"] == nil {
		t.Fatalf("model request not captured: %+v", afterModel.ModelRequest)
	}
	if afterModel.ModelResponse == nil || afterModel.ModelResponse["usage"] == nil {
		t.Fatalf("model response usage missing: %+v", afterModel.ModelResponse)
	}
	if afterModel.DurationMS == 0 {
		t.Fatalf("model duration should be tracked")
	}
	afterTool := events[3]
	if afterTool.ToolCall == nil || afterTool.ToolCall["name"] != "bash" {
		t.Fatalf("tool call not captured: %+v", afterTool.ToolCall)
	}
	if afterTool.ToolResult == nil || afterTool.ToolResult["error"] == nil {
		t.Fatalf("tool result missing error: %+v", afterTool.ToolResult)
	}
	if strings.TrimSpace(afterTool.Error) == "" {
		t.Fatalf("event error should be populated")
	}
}

func TestTraceMiddlewareConcurrentWrites(t *testing.T) {
	mw := newTraceMiddlewareForTest(t)
	ctx := context.WithValue(context.Background(), TraceSessionIDContextKey, "concurrent")
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			st := &State{Iteration: i, Agent: fmt.Sprintf("agent-%d", i)}
			if err := mw.BeforeAgent(ctx, st); err != nil {
				t.Errorf("before_agent goroutine %d: %v", i, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	sess := getSession(t, mw, "concurrent")
	jsonPath, htmlPath, events := snapshotSession(t, sess)
	if len(events) != 5 {
		t.Fatalf("expected 5 events, got %d", len(events))
	}
	assertJSONLValid(t, jsonPath, 5)
	assertHTMLContains(t, htmlPath, "Trace Session: concurrent")
}

func TestTraceMiddlewareSessionIsolation(t *testing.T) {
	mw := newTraceMiddlewareForTest(t)

	stA := &State{Iteration: 1, Agent: "alpha", Values: map[string]any{"trace.session_id": "session-state"}}
	if err := mw.AfterAgent(context.Background(), stA); err != nil {
		t.Fatalf("after_agent state session: %v", err)
	}

	ctxB := context.WithValue(context.Background(), SessionIDContextKey, "session-ctx")
	stB := &State{Iteration: 2, Agent: "beta"}
	if err := mw.AfterAgent(ctxB, stB); err != nil {
		t.Fatalf("after_agent ctx session: %v", err)
	}

	sessA := getSession(t, mw, "session-state")
	sessB := getSession(t, mw, "session-ctx")

	jsonA, htmlA, _ := snapshotSession(t, sessA)
	jsonB, htmlB, _ := snapshotSession(t, sessB)
	if jsonA == jsonB {
		t.Fatalf("different sessions should not share json file")
	}
	eventsA := assertJSONLValid(t, jsonA, 1)
	eventsB := assertJSONLValid(t, jsonB, 1)
	if eventsA[0]["session_id"] != "session-state" {
		t.Fatalf("session-state json incorrect: %v", eventsA[0])
	}
	if eventsB[0]["session_id"] != "session-ctx" {
		t.Fatalf("session-ctx json incorrect: %v", eventsB[0])
	}
	assertHTMLContains(t, htmlA, "session-state")
	assertHTMLContains(t, htmlB, "session-ctx")
}

func TestTraceMiddlewareSameSessionMultipleWrites(t *testing.T) {
	mw := newTraceMiddlewareForTest(t)
	sessionID := "same-session"

	for i := 1; i <= 3; i++ {
		st := &State{
			Iteration:   i,
			Agent:       fmt.Sprintf("agent-%d", i),
			Values:      map[string]any{"trace.session_id": sessionID},
			ModelOutput: fmt.Sprintf("output-%d", i),
		}
		if err := mw.AfterAgent(context.Background(), st); err != nil {
			t.Fatalf("after_agent iteration %d: %v", i, err)
		}
	}

	sess := getSession(t, mw, sessionID)
	jsonPath, htmlPath, events := snapshotSession(t, sess)
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	for idx, evt := range events {
		if evt.SessionID != sessionID {
			t.Fatalf("event %d session mismatch: %s", idx, evt.SessionID)
		}
	}

	entries, err := os.ReadDir(filepath.Dir(jsonPath))
	if err != nil {
		t.Fatalf("readdir %s: %v", filepath.Dir(jsonPath), err)
	}
	var jsonCount, htmlCount int
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		switch {
		case strings.HasSuffix(entry.Name(), ".jsonl"):
			jsonCount++
		case strings.HasSuffix(entry.Name(), ".html"):
			htmlCount++
		}
	}
	if jsonCount != 1 {
		t.Fatalf("expected 1 jsonl file, got %d", jsonCount)
	}
	if htmlCount != 1 {
		t.Fatalf("expected 1 html file, got %d", htmlCount)
	}
	if base := filepath.Base(jsonPath); !strings.Contains(base, sessionID) {
		t.Fatalf("jsonl filename should contain session id")
	}
	if base := filepath.Base(htmlPath); !strings.Contains(base, sessionID) {
		t.Fatalf("html filename should contain session id")
	}

	fileEvents := assertJSONLValid(t, jsonPath, 3)
	for idx, evt := range fileEvents {
		if session, _ := evt["session_id"].(string); session != sessionID {
			t.Fatalf("json event %d session mismatch: %v", idx, evt["session_id"])
		}
	}
	assertHTMLContains(t, htmlPath, sessionID)
}

func TestTraceMiddlewareAppendHandlesErrors(t *testing.T) {
	mw := newTraceMiddlewareForTest(t)
	sess := mw.sessionFor("append-error")
	if sess == nil {
		t.Fatalf("session should not be nil")
	}
	evt := TraceEvent{
		Timestamp: mw.now(),
		Stage:     "custom",
		Iteration: 1,
		SessionID: "append-error",
		Input:     make(chan int),
	}
	sess.append(evt, mw)
	_, htmlPath, events := snapshotSession(t, sess)
	if len(events) == 0 {
		t.Fatalf("expected at least one event after append")
	}
	assertHTMLContains(t, htmlPath, "append-error")

	sess.mu.Lock()
	if sess.jsonFile != nil {
		_ = sess.jsonFile.Close()
	}
	sess.jsonFile = nil
	sess.mu.Unlock()

	sess.append(TraceEvent{Timestamp: mw.now(), Stage: "custom", SessionID: "append-error"}, mw)
	if _, _, events := snapshotSession(t, sess); len(events) != 2 {
		t.Fatalf("expected two events after second append")
	}
}

func TestTraceMiddlewareSessionFallbacks(t *testing.T) {
	mw := newTraceMiddlewareForTest(t)
	defaultSess := mw.sessionFor("")
	if defaultSess == nil || defaultSess.id != "session" {
		t.Fatalf("default session id mismatch: %+v", defaultSess)
	}

	var nilMW *TraceMiddleware
	nilMW.record(context.Background(), StageBeforeAgent, &State{})
	mw.record(context.Background(), StageBeforeAgent, nil)

	generated := mw.resolveSessionID(context.TODO(), nil)
	if !strings.HasPrefix(generated, "session-") {
		t.Fatalf("unexpected generated session id: %s", generated)
	}

	root := t.TempDir()
	file := filepath.Join(root, "fs-entry")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	broken := &TraceMiddleware{
		outputDir: filepath.Join(file, "nested"),
		sessions:  map[string]*traceSession{},
		clock:     time.Now,
	}
	if sess := broken.sessionFor("broken"); sess != nil {
		t.Fatalf("expected nil session when mkdir fails")
	}
}

func TestTraceMiddlewareHelperFunctions(t *testing.T) {
	t.Run("stageIO", func(t *testing.T) {
		state := &State{
			Agent:       "agent",
			ModelInput:  "input",
			ModelOutput: "output",
			ToolCall:    "call",
			ToolResult:  "result",
		}
		cases := []struct {
			stage Stage
			st    *State
			in    any
			out   any
		}{
			{StageBeforeAgent, state, "agent", nil},
			{StageBeforeModel, state, "input", nil},
			{StageBeforeModel, &State{Agent: "fallback"}, "fallback", nil},
			{StageAfterModel, state, "input", "output"},
			{StageBeforeTool, state, "call", nil},
			{StageAfterTool, state, "call", "result"},
			{StageAfterAgent, state, "agent", "output"},
			{Stage(99), state, nil, nil},
			{StageBeforeAgent, nil, nil, nil},
		}
		for _, tc := range cases {
			in, out := stageIO(tc.stage, tc.st)
			if in != tc.in || out != tc.out {
				t.Fatalf("stageIO stage=%d input=%v/%v expected %v/%v", tc.stage, in, out, tc.in, tc.out)
			}
		}
	})

	if got := stageName(Stage(77)); got != "stage_77" {
		t.Fatalf("stageName fallback mismatch: %s", got)
	}

	firstCases := []struct {
		values map[string]any
		want   string
	}{
		{map[string]any{"session_id": " ok "}, "ok"},
		{map[string]any{"sessionID": stubStringer(" from-stringer ")}, "from-stringer"},
		{map[string]any{"session": []byte(" bytes ")}, "bytes"},
		{nil, ""},
	}
	for _, fc := range firstCases {
		if got := firstString(fc.values, "session_id", "sessionID", "session"); got != fc.want {
			t.Fatalf("firstString mismatch for %v: %q", fc.values, got)
		}
	}
	if got := firstString(map[string]any{"session_id": "value"}); got != "" {
		t.Fatalf("firstString should honor empty keys: %q", got)
	}

	if got := anyToString(12345); got != "" {
		t.Fatalf("anyToString default mismatch: %q", got)
	}

	if err := writeJSONLine(nil, TraceEvent{}); err != nil {
		t.Fatalf("writeJSONLine nil file: %v", err)
	}

	var nilMW *TraceMiddleware
	if nilMW.now().IsZero() {
		t.Fatalf("expected non-zero time from nil middleware")
	}
}

func TestAggregateStats(t *testing.T) {
	events := []TraceEvent{
		{DurationMS: 5, ModelResponse: map[string]any{"usage": map[string]any{"total_tokens": 3}}},
		{DurationMS: 7, ModelResponse: map[string]any{"usage": model.Usage{TotalTokens: 2}}},
	}
	tokens, duration := aggregateStats(events)
	if tokens != 5 {
		t.Fatalf("expected tokens=5 got %d", tokens)
	}
	if duration != 12 {
		t.Fatalf("expected duration=12 got %d", duration)
	}
}

func TestWriteAtomicError(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name string
		path string
		prep func() string
	}{
		{
			name: "parent not directory",
			prep: func() string {
				file := filepath.Join(root, "data.bin")
				if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
					t.Fatalf("write file: %v", err)
				}
				return filepath.Join(file, "trace.html")
			},
		},
		{
			name: "target exists as directory",
			prep: func() string {
				dir := filepath.Join(root, "existing-dir")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				return dir
			},
		},
	}

	for _, tc := range cases {
		path := tc.prep()
		if err := writeAtomic(path, []byte("oops")); err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
	}
}

func TestWriteJSONLineFailures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "readonly.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o644)
	if err != nil {
		t.Fatalf("open file: %v", err)
	}
	defer f.Close()
	if err := writeJSONLine(f, TraceEvent{}); err == nil {
		t.Fatalf("expected write error for read-only file")
	}
}

func TestTraceSessionAppendNilOwner(t *testing.T) {
	sess := &traceSession{}
	sess.append(TraceEvent{}, nil)
	sess.append(TraceEvent{}, (*TraceMiddleware)(nil))
}

func TestTraceMiddlewareRenderTemplateError(t *testing.T) {
	mw := newTraceMiddlewareForTest(t)
	mw.tmpl = template.Must(template.New("bad").Parse("{{call .SessionID}}"))
	sess := mw.sessionFor("tmpl-error")
	sess.mu.Lock()
	sess.events = append(sess.events, TraceEvent{SessionID: "tmpl-error"})
	sess.mu.Unlock()
	if err := mw.renderHTML(sess); err == nil {
		t.Fatalf("expected template execution error")
	}
}
