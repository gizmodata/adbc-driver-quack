package quack

import (
	"context"
	"errors"
	"strings"

	"github.com/apache/arrow-adbc/go/adbc"

	"github.com/gizmodata/adbc-driver-quack/internal/transport"
)

// errPrefix starts every error message the driver returns, per the ADBC
// driver style guide ("[driver] message", the driver's slug as installed:
// `quack`). The C wrapper (pkg/quack) uses the same prefix and does not add
// it twice.
const errPrefix = "[quack] "

// prefixed returns msg with errPrefix in front, once.
func prefixed(msg string) string {
	if strings.HasPrefix(msg, errPrefix) {
		return msg
	}
	return errPrefix + msg
}

// newError builds an adbc.Error whose message carries errPrefix.
func newError(code adbc.Status, msg string, state string) adbc.Error {
	e := adbc.Error{Code: code, Msg: prefixed(msg)}
	if state != "" {
		e.SqlState = sqlState(state)
	}
	return e
}

// fromTransportError turns a session/transport failure into an adbc.Error.
// A Quack 2.0 server reports DuckDB's exception type with every error
// ("Catalog", "Parser", "Constraint", ...), which picks the ADBC status
// and a SQLSTATE, so callers can tell a missing table from a syntax error
// from a constraint violation.
func fromTransportError(err error) error {
	if err == nil {
		return nil
	}
	var ae adbc.Error
	if errors.As(err, &ae) {
		ae.Msg = prefixed(ae.Msg)
		return ae
	}
	if errors.Is(err, context.Canceled) {
		return newError(adbc.StatusCancelled, err.Error(), "57014")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return newError(adbc.StatusTimeout, err.Error(), "HYT00")
	}
	var se *transport.ErrServerError
	if errors.As(err, &se) {
		code, state := classifyServerError(se)
		return newError(code, se.Error(), state)
	}
	return newError(adbc.StatusIO, err.Error(), "08006")
}

func sqlState(s string) [5]byte {
	var out [5]byte
	copy(out[:], s)
	return out
}

// classifyServerError maps DuckDB's exception type (and, where the type is
// too broad, the message) to an ADBC status and SQLSTATE.
func classifyServerError(se *transport.ErrServerError) (adbc.Status, string) {
	msg := strings.ToLower(se.Message)
	switch {
	case strings.Contains(msg, "authentication failed"):
		return adbc.StatusUnauthenticated, "28000"
	case strings.Contains(msg, "authorization failed"):
		return adbc.StatusUnauthorized, "42501"
	case strings.Contains(msg, "heartbeat lease expired"), strings.Contains(msg, "invalid connection id"):
		return adbc.StatusInvalidState, "08003"
	}
	switch strings.ToLower(se.ExceptionType) {
	case "catalog":
		switch {
		case strings.Contains(msg, "already exists"):
			return adbc.StatusAlreadyExists, "42S01"
		case strings.Contains(msg, "does not exist"):
			if strings.Contains(msg, "table") || strings.Contains(msg, "view") {
				return adbc.StatusNotFound, "42S02"
			}
			if strings.Contains(msg, "column") {
				return adbc.StatusNotFound, "42S22"
			}
			return adbc.StatusNotFound, "42000"
		}
		return adbc.StatusNotFound, "42000"
	case "binder":
		if strings.Contains(msg, "column") && (strings.Contains(msg, "not found") || strings.Contains(msg, "does not exist")) {
			return adbc.StatusNotFound, "42S22"
		}
		if strings.Contains(msg, "table") && strings.Contains(msg, "does not exist") {
			return adbc.StatusNotFound, "42S02"
		}
		return adbc.StatusInvalidArgument, "42000"
	case "parser", "syntax":
		return adbc.StatusInvalidArgument, "42601"
	case "constraint":
		return adbc.StatusIntegrity, "23000"
	case "conversion", "invalid input", "out of range", "mismatch type", "decimal", "unknown type",
		"invalid type", "divide by zero", "object size":
		if strings.HasPrefix(strings.ToLower(se.ExceptionType), "divide") {
			return adbc.StatusInvalidData, "22012"
		}
		if strings.Contains(strings.ToLower(se.ExceptionType), "range") {
			return adbc.StatusInvalidData, "22003"
		}
		return adbc.StatusInvalidData, "22000"
	case "transactioncontext", "transaction":
		return adbc.StatusInvalidState, "25000"
	case "permission":
		return adbc.StatusUnauthorized, "42501"
	case "interrupt":
		return adbc.StatusCancelled, "57014"
	case "not implemented":
		return adbc.StatusNotImplemented, "0A000"
	case "io", "http", "network", "connection":
		return adbc.StatusIO, "58030"
	case "dependency":
		return adbc.StatusInvalidState, "2BP01"
	case "out of memory":
		return adbc.StatusInternal, "53200"
	case "", "internal", "fatal":
		return adbc.StatusInternal, "XX000"
	}
	return adbc.StatusInternal, "HY000"
}
