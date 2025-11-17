package session

import "errors"

var (
	// ErrSessionClosed indicates the session can no longer be mutated.
	ErrSessionClosed = errors.New("session: closed")
	// ErrCheckpointNotFound indicates the requested checkpoint name does not exist.
	ErrCheckpointNotFound = errors.New("session: checkpoint not found")
	// ErrInvalidCheckpointName indicates the provided checkpoint identifier is empty or malformed.
	ErrInvalidCheckpointName = errors.New("session: invalid checkpoint name")
	// ErrCheckpointTooLarge indicates the serialized checkpoint exceeds the allowed size.
	ErrCheckpointTooLarge = errors.New("session: checkpoint exceeds maximum payload size")
	// ErrInvalidSessionID indicates the provided session identifier is empty or malformed.
	ErrInvalidSessionID = errors.New("session: invalid session id")
	// ErrInvalidMessage signals that the supplied message is structurally invalid.
	ErrInvalidMessage = errors.New("session: invalid message")
)

// Session abstracts conversation persistence and branching semantics.
type Session interface {
	// ID returns the globally unique identifier for the session.
	ID() string

	// Append stores a message at the end of the session transcript.
	Append(msg Message) error

	// List retrieves messages that satisfy the filter constraints.
	List(filter Filter) ([]Message, error)

	// Checkpoint captures the current session transcript under the provided name.
	Checkpoint(name string) error

	// Resume restores the transcript saved under the provided checkpoint name.
	Resume(name string) error

	// Fork clones the session state into a new branch identified by name.
	Fork(name string) (Session, error)

	// Close releases resources associated with the session.
	Close() error
}
