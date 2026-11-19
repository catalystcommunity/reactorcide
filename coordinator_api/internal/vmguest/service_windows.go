//go:build windows

package vmguest

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Install copies the running binary to WindowsDaemonPath and registers an
// automatic-start service that runs "vmguest serve" as LocalSystem. It must
// run elevated in the guest. With start false the service first runs at the
// next boot: an image build uses this, because the daemon sets the system
// proxy, and the build VM still needs its own network.
func Install(start bool) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := copyFile(self, WindowsDaemonPath); err != nil {
		return fmt.Errorf("vmguest: copy daemon: %w", err)
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("vmguest: connect to the service manager: %w", err)
	}
	defer m.Disconnect()
	if s, err := m.OpenService(WindowsServiceName); err == nil {
		s.Close()
		return fmt.Errorf("vmguest: service %s already exists", WindowsServiceName)
	}
	s, err := m.CreateService(WindowsServiceName, WindowsDaemonPath, mgr.Config{
		DisplayName: "Reactorcide VM guest daemon",
		Description: "Runs Reactorcide job commands from the host and tunnels guest egress through the host.",
		StartType:   mgr.StartAutomatic,
	}, "serve")
	if err != nil {
		return fmt.Errorf("vmguest: create service: %w", err)
	}
	defer s.Close()
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}, 86400)
	if !start {
		return nil
	}
	return s.Start("serve")
}

// Uninstall stops and removes the service.
func Uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(WindowsServiceName)
	if err != nil {
		return nil
	}
	defer s.Close()
	_, _ = s.Control(svc.Stop)
	return s.Delete()
}

func copyFile(src, dst string) error {
	if strings.EqualFold(filepath.Clean(src), filepath.Clean(dst)) {
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
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// RunService runs run under the service control manager when the process
// is a Windows service. It reports false for a console process.
func RunService(run func(stop <-chan struct{}) error) (bool, error) {
	isService, err := svc.IsWindowsService()
	if err != nil || !isService {
		return false, err
	}
	return true, svc.Run(WindowsServiceName, &serviceHandler{run: run})
}

type serviceHandler struct {
	run func(stop <-chan struct{}) error
}

func (h *serviceHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- h.run(stop) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, os.ErrClosed) {
				return true, 1
			}
			return false, 0
		case req := <-requests:
			switch req.Cmd {
			case svc.Interrogate:
				status <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				close(stop)
				select {
				case <-done:
				case <-time.After(10 * time.Second):
				}
				return false, 0
			}
		}
	}
}
