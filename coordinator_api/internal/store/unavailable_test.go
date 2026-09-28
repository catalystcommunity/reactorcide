package store

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsUnavailable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"not found", ErrNotFound, false},
		{"wrapped not found", fmt.Errorf("load: %w", ErrNotFound), false},
		{"denial", errors.New("secret access denied for path x"), false},
		{"unique violation", &pgconn.PgError{Code: "23505"}, false},
		{"connection refused", fmt.Errorf("mint: %w", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}), true},
		{"admin shutdown", fmt.Errorf("save: %w", &pgconn.PgError{Code: "57P01"}), true},
		{"connection exception", &pgconn.PgError{Code: "08006"}, true},
		{"too many connections", &pgconn.PgError{Code: "53300"}, true},
		{"serialization failure", &pgconn.PgError{Code: "40001"}, true},
		{"service unavailable", fmt.Errorf("x: %w", ErrServiceUnavailable), true},
		{"flattened chain", fmt.Errorf("resolve: %v", errors.New("dial tcp 10.0.0.1:5432: connect: connection refused")), true},
	}
	for _, tc := range cases {
		if got := IsUnavailable(tc.err); got != tc.want {
			t.Errorf("%s: IsUnavailable(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}
