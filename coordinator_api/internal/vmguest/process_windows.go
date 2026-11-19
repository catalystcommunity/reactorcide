//go:build windows

package vmguest

import (
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// account is the guest account that runs the command (see account_windows.go).
// The zero value means the daemon account (LocalSystem).
type account struct {
	set     bool
	name    string
	home    string
	sid     *windows.SID
	token   windows.Token
	environ []string
	admin   bool
}

// chown gives the account full control of an uploaded file or directory.
// Windows has no owner change without SeRestorePrivilege, and the access
// entry is what lets the job read, write, and delete the file.
func (a account) chown(p string) error {
	if !a.set {
		return nil
	}
	st, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if st.Mode()&fs.ModeSymlink != 0 {
		return nil
	}
	return grantFile(p, a.sid, st.IsDir())
}

// env returns the user environment of the account (USERPROFILE, APPDATA,
// LOCALAPPDATA, TEMP, TMP, USERNAME, the user PATH, and so on), with HOME set
// to the profile directory when the profile has no HOME.
func (a account) env() []string {
	if !a.set {
		return nil
	}
	return append([]string{}, a.environ...)
}

func configureCmd(cmd *exec.Cmd, a account) {
	attr := &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	if a.set {
		attr.Token = syscall.Token(a.token)
	}
	cmd.SysProcAttr = attr
}

// signalGroup stops the process tree of the command. Windows has no POSIX
// signals, and console programs do not stop for a window close message, so
// every signal stops the tree.
func signalGroup(cmd *exec.Cmd, _ string) error {
	killGroup(cmd)
	return nil
}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	kill := exec.Command("taskkill.exe", taskkillArgs(cmd.Process.Pid)...)
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if kill.Run() != nil {
		_ = cmd.Process.Kill()
	}
}

// cleanupGroup does nothing on Windows. After Wait, Go has closed the process
// handle, so Windows can give the PID to a new process, and taskkill could
// stop an unrelated process tree. Left processes stop with the disposable VM.
func cleanupGroup(*exec.Cmd) {}

func stateExitCode(state *os.ProcessState) int {
	if state == nil {
		return -1
	}
	return state.ExitCode()
}

func executableNames(p string) []string {
	if strings.Contains(strings.ToLower(p[strings.LastIndexAny(p, `/\`)+1:]), ".") {
		return []string{p}
	}
	exts := strings.Split(strings.ToLower(os.Getenv("PATHEXT")), ";")
	if len(exts) == 0 || exts[0] == "" {
		exts = []string{".com", ".exe", ".bat", ".cmd"}
	}
	out := make([]string, 0, len(exts))
	for _, e := range exts {
		if e != "" {
			out = append(out, p+e)
		}
	}
	return out
}

func isExecutable(fs.FileInfo) bool { return true }
