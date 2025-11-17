package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cexll/agentsdk-go/pkg/wal"
)

var channelOrder = []Channel{ChannelProgress, ChannelControl, ChannelMonitor}

// WAL groups per-channel WAL instances to isolate traffic and simplify replay.
type WAL struct {
	mu     sync.RWMutex
	root   string
	logs   map[Channel]*wal.WAL
	latest Cursors
}

// NewWAL opens a channel-separated WAL hierarchy rooted at dir.
func NewWAL(dir string, opts ...wal.Option) (*WAL, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("session: wal root is empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("session: mkdir wal root: %w", err)
	}
	w := &WAL{
		root:   dir,
		logs:   make(map[Channel]*wal.WAL, len(channelOrder)),
		latest: make(Cursors, len(channelOrder)),
	}
	for _, ch := range channelOrder {
		subdir := filepath.Join(dir, string(ch))
		log, err := wal.Open(subdir, opts...)
		if err != nil {
			w.closeAll()
			return nil, err
		}
		w.logs[ch] = log
		var last wal.Position = -1
		if err := log.Replay(func(e wal.Entry) error {
			if e.Position > last {
				last = e.Position
			}
			return nil
		}); err != nil {
			w.closeAll()
			return nil, err
		}
		if last >= 0 {
			w.latest[ch] = last
		}
	}
	return w, nil
}

// Append writes entry to the channel WAL and tracks its cursor.
func (w *WAL) Append(ch Channel, entry wal.Entry) (wal.Position, error) {
	log, err := w.logFor(ch)
	if err != nil {
		return 0, err
	}
	pos, err := log.Append(entry)
	if err != nil {
		return 0, err
	}
	w.mu.Lock()
	w.latest[ch] = pos
	w.mu.Unlock()
	return pos, nil
}

// ReadSince streams entries starting at the cursor for the given channel.
func (w *WAL) ReadSince(ch Channel, start wal.Position, apply func(wal.Entry) error) error {
	log, err := w.logFor(ch)
	if err != nil {
		return err
	}
	return log.ReadSince(start, apply)
}

// Truncate removes channel entries below upto.
func (w *WAL) Truncate(ch Channel, upto wal.Position) error {
	log, err := w.logFor(ch)
	if err != nil {
		return err
	}
	return log.Truncate(upto)
}

// Rotate forces a segment rotation on the channel WAL.
func (w *WAL) Rotate(ch Channel) error {
	log, err := w.logFor(ch)
	if err != nil {
		return err
	}
	return log.Rotate()
}

// Sync flushes buffered data for the channel WAL.
func (w *WAL) Sync(ch Channel) error {
	log, err := w.logFor(ch)
	if err != nil {
		return err
	}
	return log.Sync()
}

// Fsync enforces durability for the channel WAL.
func (w *WAL) Fsync(ch Channel) error {
	log, err := w.logFor(ch)
	if err != nil {
		return err
	}
	return log.Fsync()
}

// Snapshot returns the latest known cursors.
func (w *WAL) Snapshot() Cursors {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.latest.Clone()
}

// Close releases all channel WALs.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	var err error
	for _, log := range w.logs {
		if log == nil {
			continue
		}
		if closeErr := log.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}
	return err
}

func (w *WAL) logFor(ch Channel) (*wal.WAL, error) {
	w.mu.RLock()
	log := w.logs[ch]
	w.mu.RUnlock()
	if log == nil {
		return nil, fmt.Errorf("session: unknown wal channel %q", ch)
	}
	return log, nil
}

func (w *WAL) closeAll() {
	_ = w.Close()
}
