//go:build darwin

package vmguest

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// Install copies the running binary to MacDaemonPath and writes the launchd
// daemon file. With start true it also loads the daemon now; else launchd
// starts it at the next boot. It must run as root in the guest.
func Install(start bool) error {
	if os.Geteuid() != 0 {
		return errors.New("vmguest: install must run as root")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := copyExecutable(self, MacDaemonPath); err != nil {
		return err
	}
	if err := os.WriteFile(MacLaunchdPlist, []byte(LaunchdPlist(MacDaemonPath)), 0o644); err != nil {
		return fmt.Errorf("vmguest: write launchd file: %w", err)
	}
	if err := os.Chown(MacLaunchdPlist, 0, 0); err != nil {
		return err
	}
	if out, err := exec.Command("/bin/sh", "-c", MacJobDirSetupScript).CombinedOutput(); err != nil {
		return fmt.Errorf("vmguest: set up /job: %v: %s", err, out)
	}
	if !start {
		return nil
	}
	_ = exec.Command("/bin/launchctl", "bootout", "system/"+MacLaunchdLabel).Run()
	if out, err := exec.Command("/bin/launchctl", "bootstrap", "system", MacLaunchdPlist).CombinedOutput(); err != nil {
		return fmt.Errorf("vmguest: launchctl bootstrap: %v: %s", err, out)
	}
	return nil
}

// Uninstall stops the daemon and removes its files.
func Uninstall() error {
	_ = exec.Command("/bin/launchctl", "bootout", "system/"+MacLaunchdLabel).Run()
	if err := os.Remove(MacLaunchdPlist); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(MacDaemonPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func copyExecutable(src, dst string) error {
	if src == dst {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Chown(tmp, 0, 0); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// RunService reports false: launchd runs the daemon as a normal program.
func RunService(func(stop <-chan struct{}) error) (bool, error) { return false, nil }
