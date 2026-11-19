package vmguest

import (
	"fmt"
	"strings"
)

// Hello is the first frame in each direction. The worker names the guest
// account that owns the files and runs the command (User); the guest names its
// operating system (OS).
//
// CreateAdministrator (worker to guest) applies only when the Windows daemon
// creates the account because the image does not have it: nil or true puts
// the new account in Administrators, false makes a standard user. The daemon
// does not change an account that the image has.
//
// In the reply, User and Administrator tell the worker which account runs
// the command and if it is an administrator. Both are optional, so a worker
// and a daemon of different builds with the same protocol version interoperate.
type Hello struct {
	Version             int    `json:"version"`
	OS                  string `json:"os,omitempty"`
	User                string `json:"user,omitempty"`
	CreateAdministrator *bool  `json:"create_administrator,omitempty"`
	Administrator       *bool  `json:"administrator,omitempty"`
}

// AccountLine is the line that tells which account runs a job command.
func AccountLine(user string, administrator bool) string {
	is := "is not"
	if administrator {
		is = "is"
	}
	return fmt.Sprintf("running as %s which %s an administrator", user, is)
}

// FileHeader starts a file upload. Mode is the POSIX permission bits; the
// Windows daemon ignores it.
type FileHeader struct {
	Path string `json:"path"`
	Mode uint32 `json:"mode"`
}

// TreeHeader starts a tar stream that the guest extracts below Dest.
type TreeHeader struct {
	Dest string `json:"dest"`
}

// ResultSpec names a file that the guest sends back after the command exits.
type ResultSpec struct {
	Path     string `json:"path"`
	MaxBytes int64  `json:"max_bytes"`
}

// StartRequest starts the command. Env can contain secrets: the daemon never
// logs it.
type StartRequest struct {
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
	Dir     string            `json:"dir,omitempty"`
	User    string            `json:"user,omitempty"`
	Results []ResultSpec      `json:"results,omitempty"`
}

// SignalRequest asks the daemon to signal the process group of the command.
type SignalRequest struct {
	Signal string `json:"signal"`
}

// ResultHeader starts a result file. Data frames follow only when Missing and
// TooLarge are both false.
type ResultHeader struct {
	Index    int    `json:"index"`
	Missing  bool   `json:"missing,omitempty"`
	TooLarge bool   `json:"too_large,omitempty"`
	Error    string `json:"error,omitempty"`
}

// ExitStatus is the last frame of a command.
type ExitStatus struct {
	Code  int    `json:"code"`
	Error string `json:"error,omitempty"`
}

// Signals are the signal names that the protocol accepts.
var Signals = []string{"HUP", "INT", "QUIT", "KILL", "TERM", "USR1", "USR2"}

// NormalizeSignal returns the protocol name of sig ("SIGTERM", "term" ->
// "TERM"), or an error for a signal that the protocol does not accept.
func NormalizeSignal(sig string) (string, error) {
	name := strings.ToUpper(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(sig)), "SIG"))
	for _, s := range Signals {
		if s == name {
			return s, nil
		}
	}
	return "", fmt.Errorf("vmguest: unsupported signal %q", sig)
}

// ValidEnvName reports whether name is a portable environment variable name.
func ValidEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
