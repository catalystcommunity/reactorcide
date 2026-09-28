package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5/pgconn"
)

// unavailableMessages match a connection failure after a caller flattened the
// error chain with %v instead of %w.
var unavailableMessages = []string{
	"connection refused",
	"connection reset",
	"broken pipe",
	"unexpected eof",
	"failed to connect",
	"server closed the connection",
	"the database system is",
	"conn closed",
	"i/o timeout",
}

// IsUnavailable reports whether err means the database could not answer
// (connection lost, server restarting, overloaded, or a retryable
// serialization failure), as opposed to a definite answer such as "not found"
// or "denied". A caller that sees true must leave its state retryable: it
// must not turn the error into a terminal decision.
func IsUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrServiceUnavailable) ||
		errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) {
		return true
	}
	var connectErr *pgconn.ConnectError
	if errors.As(err, &connectErr) || pgconn.Timeout(err) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		code := pgErr.Code
		switch {
		case strings.HasPrefix(code, "08"), // connection exception
			strings.HasPrefix(code, "53"),    // insufficient resources
			strings.HasPrefix(code, "57P0"),  // admin/crash shutdown, cannot connect now
			code == "40001", code == "40P01": // serialization failure, deadlock
			return true
		}
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, m := range unavailableMessages {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}
