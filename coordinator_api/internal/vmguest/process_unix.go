//go:build !windows

package vmguest

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
)

// account is the guest user that owns uploaded files and runs the command.
// The zero value means the daemon user.
type account struct {
	set    bool
	uid    uint32
	gid    uint32
	groups []uint32
	name   string
	home   string
	admin  bool
}

// lookupAccount resolves the guest account. The daemon must run as root to
// switch to another account. It never creates an account (the image must have
// it), so it ignores createAdmin.
func lookupAccount(name string, _ bool) (account, error) {
	if name == "" {
		return account{}, nil
	}
	u, err := user.Lookup(name)
	if err != nil {
		return account{}, fmt.Errorf("vmguest: guest account %q: %w", name, err)
	}
	uid, err1 := strconv.ParseUint(u.Uid, 10, 32)
	gid, err2 := strconv.ParseUint(u.Gid, 10, 32)
	if err1 != nil || err2 != nil {
		return account{}, fmt.Errorf("vmguest: guest account %q has a non-numeric id", name)
	}
	if os.Geteuid() != 0 {
		if uint64(os.Geteuid()) == uid {
			return account{name: u.Username, home: u.HomeDir, admin: unixAdmin(u)}, nil
		}
		return account{}, fmt.Errorf("vmguest: the daemon is not root and cannot run commands as %q", name)
	}
	a := account{set: true, uid: uint32(uid), gid: uint32(gid), name: u.Username, home: u.HomeDir, admin: unixAdmin(u)}
	if ids, err := u.GroupIds(); err == nil {
		for _, id := range ids {
			if g, err := strconv.ParseUint(id, 10, 32); err == nil {
				a.groups = append(a.groups, uint32(g))
			}
		}
	}
	return a, nil
}

// unixAdmin reports if the account is root or in an administrator group
// (admin on macOS, wheel or sudo on other systems).
func unixAdmin(u *user.User) bool {
	if u.Uid == "0" {
		return true
	}
	ids, err := u.GroupIds()
	if err != nil {
		return false
	}
	for _, id := range ids {
		if g, err := user.LookupGroupId(id); err == nil && isAdminGroup(g.Name) {
			return true
		}
	}
	return false
}

func (a account) chown(p string) error {
	if !a.set {
		return nil
	}
	return os.Lchown(p, int(a.uid), int(a.gid))
}

// env returns the login variables of the account.
func (a account) env() []string {
	if a.name == "" {
		return nil
	}
	out := []string{"USER=" + a.name, "LOGNAME=" + a.name}
	if a.home != "" {
		out = append(out, "HOME="+a.home)
	}
	return out
}

func configureCmd(cmd *exec.Cmd, a account) {
	attr := &syscall.SysProcAttr{Setpgid: true}
	if a.set {
		attr.Credential = &syscall.Credential{Uid: a.uid, Gid: a.gid, Groups: a.groups}
	}
	cmd.SysProcAttr = attr
}

var signalNumbers = map[string]syscall.Signal{
	"HUP":  syscall.SIGHUP,
	"INT":  syscall.SIGINT,
	"QUIT": syscall.SIGQUIT,
	"KILL": syscall.SIGKILL,
	"TERM": syscall.SIGTERM,
	"USR1": syscall.SIGUSR1,
	"USR2": syscall.SIGUSR2,
}

// signalGroup signals the process group of the command.
func signalGroup(cmd *exec.Cmd, sig string) error {
	if cmd.Process == nil {
		return nil
	}
	n, ok := signalNumbers[sig]
	if !ok {
		return fmt.Errorf("vmguest: unsupported signal %q", sig)
	}
	err := syscall.Kill(-cmd.Process.Pid, n)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// cleanupGroup stops the processes that the command left in its process
// group after the command exited.
func cleanupGroup(cmd *exec.Cmd) { killGroup(cmd) }

// stateExitCode is the exit status, or 128 plus the signal number for a
// command that a signal stopped (the shell convention).
func stateExitCode(state *os.ProcessState) int {
	if state == nil {
		return -1
	}
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return state.ExitCode()
}

func executableNames(p string) []string { return []string{p} }

func isExecutable(st fs.FileInfo) bool { return st.Mode().Perm()&0o111 != 0 }
