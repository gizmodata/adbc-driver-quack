package codec

import (
	"errors"
	"fmt"
)

// Error reports a wire-format problem in the Quack BinarySerializer
// codec — typically truncated input, an unexpected field id, or a value
// outside the protocol's expected range.
type Error struct {
	Op     string
	Offset int
	Msg    string
}

func (e *Error) Error() string {
	if e.Op == "" {
		return fmt.Sprintf("codec: %s (offset %d)", e.Msg, e.Offset)
	}
	return fmt.Sprintf("codec: %s: %s (offset %d)", e.Op, e.Msg, e.Offset)
}

// AsError unwraps a *codec.Error if present.
func AsError(err error) *Error {
	var ce *Error
	if errors.As(err, &ce) {
		return ce
	}
	return nil
}

func newError(op, msg string, offset int) *Error {
	return &Error{Op: op, Msg: msg, Offset: offset}
}
