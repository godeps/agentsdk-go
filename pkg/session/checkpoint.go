package session

import (
	"encoding/json"
	"time"

	"github.com/cexll/agentsdk-go/pkg/wal"
)

// Channel enumerates the three physical WAL streams used by the session.
type Channel string

const (
	ChannelProgress Channel = "progress"
	ChannelControl  Channel = "control"
	ChannelMonitor  Channel = "monitor"

	// MaxCheckpointBytes bounds serialized checkpoint payloads to 1MB.
	MaxCheckpointBytes = 1 << 20
)

// Cursors tracks the latest acknowledged WAL position per channel.
type Cursors map[Channel]wal.Position

// Clone returns a deep copy of the cursor map.
func (c Cursors) Clone() Cursors {
	if len(c) == 0 {
		return nil
	}
	out := make(Cursors, len(c))
	for ch, pos := range c {
		out[ch] = pos
	}
	return out
}

// Checkpoint encapsulates resumable execution metadata.
type Checkpoint struct {
	Name      string          `json:"name"`
	Timestamp time.Time       `json:"timestamp"`
	State     json.RawMessage `json:"state"`
	Cursors   Cursors         `json:"cursors,omitempty"`
}

// Clone duplicates the checkpoint and its buffers.
func (c Checkpoint) Clone() Checkpoint {
	clone := c
	if len(c.State) > 0 {
		clone.State = append(json.RawMessage(nil), c.State...)
	}
	clone.Cursors = c.Cursors.Clone()
	return clone
}

// Size returns the size of the serialized state payload.
func (c Checkpoint) Size() int {
	return len(c.State)
}
