package approval

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cexll/agentsdk-go/pkg/wal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordLogManualGCRespectsPolicies(t *testing.T) {
	dir := t.TempDir()
	log, err := NewRecordLog(dir, wal.WithDisabledSync())
	require.NoError(t, err)
	t.Cleanup(func() {
		if log != nil {
			_ = log.Close()
		}
	})

	start := time.Now().UTC().Add(-9 * 24 * time.Hour)
	for i := 0; i < 5; i++ {
		rec := Record{
			ID:        fmt.Sprintf("rec-%d", i),
			SessionID: "sess",
			Tool:      "echo",
			Decision:  DecisionApproved,
			Requested: start.Add(time.Duration(i) * 48 * time.Hour),
		}
		require.NoErrorf(t, log.Append(rec), "append %d", i)
	}

	stats, err := log.GC(WithRetentionDays(4), WithRetentionCount(2))
	require.NoError(t, err)
	assert.Equal(t, 3, stats.Dropped)
	assert.Equal(t, 2, stats.AfterCount)

	require.Len(t, log.All(), 2)
	status := log.GCStatus()
	assert.NotZero(t, status.Runs)
	assert.Equal(t, int64(stats.Dropped), status.TotalDropped)

	require.NoError(t, log.Close())
	log = nil

	reopened, err := NewRecordLog(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	require.Len(t, reopened.All(), 2)
}

func TestRecordLogGCRespectsSizeLimit(t *testing.T) {
	dir := t.TempDir()
	log, err := NewRecordLog(dir, wal.WithDisabledSync())
	require.NoError(t, err)
	t.Cleanup(func() { _ = log.Close() })

	now := time.Now().UTC()
	payload := map[string]any{"blob": string(make([]byte, 256))}
	for i := 0; i < 3; i++ {
		rec := Record{ID: fmt.Sprintf("size-%d", i), SessionID: "s", Tool: "cat", Decision: DecisionApproved, Params: payload, Requested: now.Add(time.Duration(i) * time.Minute)}
		require.NoError(t, log.Append(rec))
	}

	limit := walEntryOverhead + 100
	stats, err := log.GC(WithRetentionBytes(limit))
	require.NoError(t, err)
	assert.Greater(t, stats.Dropped, 0, "expected drop due to size limit")
}

func TestRecordLogAutoGC(t *testing.T) {
	dir := t.TempDir()
	log, err := NewRecordLog(dir, wal.WithDisabledSync())
	require.NoError(t, err)
	t.Cleanup(func() { _ = log.Close() })

	ch := make(chan GCStats, 2)
	log.ConfigureGC(WithRetentionCount(1), WithGCInterval(20*time.Millisecond), WithGCCallback(func(s GCStats) { ch <- s }))

	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		rec := Record{ID: fmt.Sprintf("auto-%d", i), SessionID: "sess", Tool: "echo", Decision: DecisionApproved, Requested: now.Add(time.Duration(i) * time.Second)}
		require.NoError(t, log.Append(rec))
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatalf("auto gc timeout")
	case stats := <-ch:
		assert.Greater(t, stats.Dropped, 0, "expected drop in auto gc")
	}

	log.ConfigureGC(WithGCInterval(0))
	status := log.GCStatus()
	assert.False(t, status.AutoEnabled, "auto gc should be disabled")
}

func TestRecordLogStartAutoGCCleansUpExpiredRecords(t *testing.T) {
	dir := t.TempDir()
	log, err := NewRecordLog(dir, wal.WithDisabledSync())
	require.NoError(t, err)
	t.Cleanup(func() {
		log.StopAutoGC()
		_ = log.Close()
	})

	statsCh := make(chan GCStats, 4)
	log.ConfigureGC(
		WithRetentionDays(2),
		WithRetentionCount(2),
		WithGCCallback(func(s GCStats) { statsCh <- s }),
	)

	start := time.Now().UTC().Add(-72 * time.Hour)
	for i := 0; i < 6; i++ {
		rec := Record{
			ID:        fmt.Sprintf("autogc-%d", i),
			SessionID: "sess",
			Tool:      "echo",
			Decision:  DecisionApproved,
			Requested: start.Add(time.Duration(i) * 12 * time.Hour),
		}
		require.NoError(t, log.Append(rec))
	}

	log.StartAutoGC(100 * time.Millisecond)

	var stats GCStats
	require.Eventually(t, func() bool {
		select {
		case stats = <-statsCh:
			return stats.Dropped > 0
		default:
			return false
		}
	}, 1500*time.Millisecond, 25*time.Millisecond, "auto gc never trimmed records")
	assert.True(t, stats.Auto)
	assert.Equal(t, 2, stats.AfterCount)

	all := log.All()
	require.Len(t, all, 2)

	kept := make(map[string]struct{}, 2)
	cutoff := time.Now().UTC().Add(-48 * time.Hour)
	for _, rec := range all {
		kept[rec.ID] = struct{}{}
		assert.False(t, rec.Requested.Before(cutoff), "found expired record %v", rec.Requested)
	}
	assert.Contains(t, kept, "autogc-4")
	assert.Contains(t, kept, "autogc-5")
}

func TestRecordLogStopAutoGCStopsTicker(t *testing.T) {
	dir := t.TempDir()
	log, err := NewRecordLog(dir, wal.WithDisabledSync())
	require.NoError(t, err)
	t.Cleanup(func() {
		log.StopAutoGC()
		_ = log.Close()
	})

	statsCh := make(chan GCStats, 8)
	log.ConfigureGC(WithRetentionCount(1), WithGCCallback(func(s GCStats) { statsCh <- s }))

	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		rec := Record{ID: fmt.Sprintf("stop-%d", i), SessionID: "sess", Tool: "echo", Decision: DecisionApproved, Requested: now.Add(time.Duration(i) * time.Second)}
		require.NoError(t, log.Append(rec))
	}

	log.StartAutoGC(80 * time.Millisecond)
	require.Eventually(t, func() bool {
		select {
		case <-statsCh:
			return true
		default:
			return false
		}
	}, time.Second, 20*time.Millisecond, "auto gc never fired")

	log.StopAutoGC()

	drain := time.After(150 * time.Millisecond)
DrainLoop:
	for {
		select {
		case <-statsCh:
		case <-drain:
			break DrainLoop
		}
	}

	select {
	case <-statsCh:
		t.Fatalf("received gc stats after StopAutoGC")
	case <-time.After(250 * time.Millisecond):
	}

	status := log.GCStatus()
	assert.False(t, status.AutoEnabled)
	assert.Zero(t, status.AutoInterval)
}

func TestRecordLogAutoGCConcurrentStartStop(t *testing.T) {
	dir := t.TempDir()
	log, err := NewRecordLog(dir, wal.WithDisabledSync())
	require.NoError(t, err)
	t.Cleanup(func() {
		log.StopAutoGC()
		_ = log.Close()
	})

	statsCh := make(chan GCStats, 32)
	log.ConfigureGC(WithRetentionCount(1), WithGCCallback(func(s GCStats) {
		select {
		case statsCh <- s:
		default:
		}
	}))

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				interval := time.Duration(40+idx*10) * time.Millisecond
				log.StartAutoGC(interval)
				if j%2 == 0 {
					log.StopAutoGC()
				}
			}
		}(i)
	}
	wg.Wait()

	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		rec := Record{ID: fmt.Sprintf("flap-%d", i), SessionID: "sess", Tool: "echo", Decision: DecisionApproved, Requested: now.Add(time.Duration(i) * time.Millisecond)}
		require.NoError(t, log.Append(rec))
	}

	log.StartAutoGC(60 * time.Millisecond)
	require.Eventually(t, func() bool {
		select {
		case <-statsCh:
			return true
		default:
			return false
		}
	}, time.Second, 20*time.Millisecond, "auto gc failed after concurrent start/stop")
	log.StopAutoGC()

	status := log.GCStatus()
	assert.False(t, status.AutoEnabled)
}

func TestRecordLogGCConcurrentSafety(t *testing.T) {
	dir := t.TempDir()
	log, err := NewRecordLog(dir, wal.WithDisabledSync())
	require.NoError(t, err)
	t.Cleanup(func() { _ = log.Close() })

	var wg sync.WaitGroup
	wg.Add(2)
	errCh := make(chan error, 2)

	go func() {
		defer wg.Done()
		now := time.Now().UTC()
		for i := 0; i < 200; i++ {
			rec := Record{ID: fmt.Sprintf("con-%d", i), SessionID: "sess", Tool: "echo", Decision: DecisionApproved, Requested: now.Add(time.Duration(i) * time.Millisecond)}
			if err := log.Append(rec); err != nil {
				errCh <- fmt.Errorf("append: %w", err)
				return
			}
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			if _, err := log.GC(WithRetentionCount(50)); err != nil {
				errCh <- fmt.Errorf("gc: %w", err)
				return
			}
		}
	}()

	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}

	_, err = log.GC(WithRetentionCount(10))
	require.NoError(t, err)
}

func TestRecordLogGCNilGuard(t *testing.T) {
	var nilLog *RecordLog
	_, err := nilLog.GC()
	require.Error(t, err)
}
