package approval

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/cexll/agentsdk-go/pkg/wal"
)

const (
	defaultRetentionDays  = 7
	defaultRetentionCount = 1000
)

type gcController struct {
	mu      sync.Mutex
	cfg     gcConfig
	metrics gcMetrics
}

type gcMetrics struct {
	runs              int64
	totalDropped      int64
	totalDroppedBytes int64
	last              GCStats
	lastErr           error
}

type gcConfig struct {
	interval       time.Duration
	retentionDays  int
	retentionCount int
	retentionBytes int64
	callback       GCCallback
}

// GCStats describes the outcome of a GC run.
type GCStats struct {
	TriggeredAt    time.Time
	Duration       time.Duration
	Auto           bool
	Dropped        int
	DroppedBytes   int64
	BeforeCount    int
	AfterCount     int
	BeforeBytes    int64
	AfterBytes     int64
	OldestDropped  time.Time
	OldestKept     time.Time
	RetentionDays  int
	RetentionCount int
	RetentionBytes int64
	Err            error
}

// GCStatus exposes cumulative metrics.
type GCStatus struct {
	Runs              int64
	TotalDropped      int64
	TotalDroppedBytes int64
	Last              GCStats
	LastError         error
	AutoInterval      time.Duration
	AutoEnabled       bool
}

// GCCallback receives GC results asynchronously.
type GCCallback func(GCStats)

// GCOption customizes GC behaviour.
type GCOption func(*gcConfig)

func defaultGCConfig() gcConfig {
	return gcConfig{
		retentionDays:  defaultRetentionDays,
		retentionCount: defaultRetentionCount,
	}
}

// WithGCInterval configures the periodic GC interval. Non-positive disables automation.
func WithGCInterval(d time.Duration) GCOption {
	return func(cfg *gcConfig) {
		if d <= 0 {
			cfg.interval = 0
			return
		}
		cfg.interval = d
	}
}

// WithRetentionDays overrides how many days of history are kept. Non-positive disables the cutoff.
func WithRetentionDays(days int) GCOption {
	return func(cfg *gcConfig) {
		if days <= 0 {
			cfg.retentionDays = 0
			return
		}
		cfg.retentionDays = days
	}
}

// WithRetentionCount preserves the most recent N records. Non-positive disables the cap.
func WithRetentionCount(count int) GCOption {
	return func(cfg *gcConfig) {
		if count <= 0 {
			cfg.retentionCount = 0
			return
		}
		cfg.retentionCount = count
	}
}

// WithRetentionBytes bounds retained WAL bytes by trimming the oldest entries first.
func WithRetentionBytes(bytes int64) GCOption {
	return func(cfg *gcConfig) {
		if bytes <= 0 {
			cfg.retentionBytes = 0
			return
		}
		cfg.retentionBytes = bytes
	}
}

// WithGCCallback registers a hook invoked after each GC run.
func WithGCCallback(cb GCCallback) GCOption {
	return func(cfg *gcConfig) {
		cfg.callback = cb
	}
}

// ConfigureGC updates RecordLog defaults that future GC runs (manual or automatic) use.
func (l *RecordLog) ConfigureGC(opts ...GCOption) {
	if l == nil {
		return
	}
	l.gc.mu.Lock()
	oldInterval := l.gc.cfg.interval
	running := l.gcTicker != nil
	cfg := l.gc.cfg
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	l.gc.cfg = cfg
	newInterval := cfg.interval
	l.gc.mu.Unlock()

	switch {
	case newInterval <= 0:
		if running {
			l.StopAutoGC()
		}
	case !running:
		l.StartAutoGC(newInterval)
	case oldInterval != newInterval:
		l.startAutoGC(newInterval, true)
	}
}

// GC triggers retention immediately using the configured policy plus optional overrides.
func (l *RecordLog) GC(opts ...GCOption) (GCStats, error) {
	if l == nil {
		return GCStats{}, errors.New("approval: record log is nil")
	}
	cfg := l.snapshotGCConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	stats, err := l.runGCWithConfig(cfg, false)
	stats.Err = err
	if cfg.callback != nil {
		go cfg.callback(stats)
	}
	return stats, err
}

// GCStatus reports accumulated housekeeping metrics.
func (l *RecordLog) GCStatus() GCStatus {
	if l == nil {
		return GCStatus{}
	}
	l.gc.mu.Lock()
	defer l.gc.mu.Unlock()
	return GCStatus{
		Runs:              l.gc.metrics.runs,
		TotalDropped:      l.gc.metrics.totalDropped,
		TotalDroppedBytes: l.gc.metrics.totalDroppedBytes,
		Last:              l.gc.metrics.last,
		LastError:         l.gc.metrics.lastErr,
		AutoInterval:      l.gc.cfg.interval,
		AutoEnabled:       l.gcTicker != nil,
	}
}

func (l *RecordLog) snapshotGCConfig() gcConfig {
	l.gc.mu.Lock()
	defer l.gc.mu.Unlock()
	return l.gc.cfg
}

func (l *RecordLog) autoGCLoop(t *time.Ticker, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-t.C:
			cfg := l.snapshotGCConfig()
			stats, err := l.runGCWithConfig(cfg, true)
			stats.Err = err
			if cfg.callback != nil {
				go cfg.callback(stats)
			}
		case <-stop:
			return
		}
	}
}

type recordMeta struct {
	Record
	position wal.Position
	size     int64
}

func (l *RecordLog) runGCWithConfig(cfg gcConfig, auto bool) (GCStats, error) {
	start := time.Now().UTC()
	stats := GCStats{
		TriggeredAt:    start,
		Auto:           auto,
		RetentionDays:  cfg.retentionDays,
		RetentionCount: cfg.retentionCount,
		RetentionBytes: cfg.retentionBytes,
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.wal == nil {
		return l.finishGC(stats, start, errors.New("approval: wal is closed"))
	}

	entries := l.snapshotRecordsLocked()
	stats.BeforeCount = len(entries)
	var totalBytes int64
	for _, entry := range entries {
		totalBytes += entry.size
	}
	stats.BeforeBytes = totalBytes
	if len(entries) == 0 {
		stats.AfterBytes = 0
		return l.finishGC(stats, start, nil)
	}

	keepStart := computeKeepStart(entries, cfg, start)
	if keepStart == 0 {
		stats.AfterCount = stats.BeforeCount
		stats.AfterBytes = stats.BeforeBytes
		stats.OldestKept = entries[0].Requested
		return l.finishGC(stats, start, nil)
	}

	dropBytes := int64(0)
	dropIDs := make([]string, 0, keepStart)
	for i := 0; i < keepStart && i < len(entries); i++ {
		dropBytes += entries[i].size
		dropIDs = append(dropIDs, entries[i].ID)
	}
	stats.Dropped = len(dropIDs)
	stats.DroppedBytes = dropBytes
	stats.AfterCount = stats.BeforeCount - stats.Dropped
	stats.AfterBytes = stats.BeforeBytes - dropBytes
	if keepStart < len(entries) {
		stats.OldestKept = entries[keepStart].Requested
	}
	if keepStart > 0 {
		stats.OldestDropped = entries[keepStart-1].Requested
	}

	truncatePos := l.nextPosition
	if keepStart < len(entries) {
		truncatePos = entries[keepStart].position
	}
	if err := l.wal.Truncate(truncatePos); err != nil {
		return l.finishGC(stats, start, err)
	}
	for _, id := range dropIDs {
		delete(l.records, id)
		delete(l.positions, id)
		delete(l.entrySize, id)
	}
	return l.finishGC(stats, start, nil)
}

func (l *RecordLog) finishGC(stats GCStats, start time.Time, err error) (GCStats, error) {
	stats.Duration = time.Since(start)
	stats.Err = err
	l.recordGCStats(stats)
	return stats, err
}

func (l *RecordLog) snapshotRecordsLocked() []recordMeta {
	entries := make([]recordMeta, 0, len(l.records))
	for id, rec := range l.records {
		entries = append(entries, recordMeta{
			Record:   rec,
			position: l.positions[id],
			size:     l.entrySize[id],
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Requested.Equal(entries[j].Requested) {
			return entries[i].ID < entries[j].ID
		}
		return entries[i].Requested.Before(entries[j].Requested)
	})
	return entries
}

func computeKeepStart(entries []recordMeta, cfg gcConfig, now time.Time) int {
	keep := 0
	if cfg.retentionDays > 0 {
		cutoff := now.Add(-time.Duration(cfg.retentionDays) * 24 * time.Hour)
		idx := sort.Search(len(entries), func(i int) bool {
			return !entries[i].Requested.Before(cutoff)
		})
		if idx > keep {
			keep = idx
		}
	}
	if cfg.retentionCount > 0 && len(entries) > cfg.retentionCount {
		idx := len(entries) - cfg.retentionCount
		if idx > keep {
			keep = idx
		}
	}
	if cfg.retentionBytes > 0 {
		var total int64
		for _, entry := range entries {
			total += entry.size
		}
		if total > cfg.retentionBytes {
			var prefix int64
			for i := 0; i < len(entries) && total-prefix > cfg.retentionBytes; i++ {
				prefix += entries[i].size
				if i+1 > keep {
					keep = i + 1
				}
			}
		}
	}
	if keep > len(entries) {
		keep = len(entries)
	}
	return keep
}

func (l *RecordLog) recordGCStats(stats GCStats) {
	l.gc.mu.Lock()
	defer l.gc.mu.Unlock()
	l.gc.metrics.runs++
	l.gc.metrics.totalDropped += int64(stats.Dropped)
	l.gc.metrics.totalDroppedBytes += stats.DroppedBytes
	l.gc.metrics.last = stats
	l.gc.metrics.lastErr = stats.Err
}
