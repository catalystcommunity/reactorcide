//go:build windows

package windowsservice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type runner func(context.Context, []string) error

type handler struct {
	config Config
	run    runner
}

func RunIfService(run runner) (bool, error) {
	isService, err := svc.IsWindowsService()
	if err != nil || !isService {
		return false, err
	}
	configPath, err := serviceConfigPath(os.Args)
	if err != nil {
		return true, err
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		return true, err
	}
	if err := configureLogging(config.LogFile); err != nil {
		return true, err
	}
	for key, value := range config.Environment {
		if err := os.Setenv(key, value); err != nil {
			return true, fmt.Errorf("set service environment %q: %w", key, err)
		}
	}
	return true, svc.Run(Name, &handler{config: config, run: run})
}

func serviceConfigPath(args []string) (string, error) {
	for index := 1; index+1 < len(args); index++ {
		if args[index] == "--config" {
			return args[index+1], nil
		}
	}
	return "", errors.New("Windows service: --config is required")
}

func configureLogging(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return fmt.Errorf("create service log directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		return fmt.Errorf("open service log: %w", err)
	}
	logging.Log.SetOutput(file)
	return nil
}

func (h *handler) Execute(_ []string, changes <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.run(ctx, h.config.Arguments) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for {
		select {
		case request := <-changes:
			switch request.Cmd {
			case svc.Interrogate:
				status <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				err := <-done
				if err != nil && !errors.Is(err, context.Canceled) {
					logging.Log.WithError(err).Error("Windows worker service stopped with an error")
					return false, 1
				}
				return false, 0
			}
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				logging.Log.WithError(err).Error("Windows worker service stopped with an error")
				return false, 1
			}
			return false, 0
		}
	}
}

func Install(executablePath, configPath string) error {
	for label, path := range map[string]string{"executable": executablePath, "config": configPath} {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("resolve %s path: %w", label, err)
		}
		if _, err := os.Stat(absolute); err != nil {
			return fmt.Errorf("open %s path: %w", label, err)
		}
		if label == "executable" {
			executablePath = absolute
		} else {
			configPath = absolute
		}
	}
	if _, err := LoadConfig(configPath); err != nil {
		return err
	}
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to Windows service manager: %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.CreateService(Name, executablePath, mgr.Config{
		DisplayName:      DisplayName,
		Description:      "Runs Reactorcide jobs on this Windows host.",
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true,
	}, "windows-service", "run", "--config", configPath)
	if err != nil {
		return fmt.Errorf("install Windows service: %w", err)
	}
	defer service.Close()
	actions := []mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: 5 * time.Second}}
	if err := service.SetRecoveryActions(actions, 86400); err != nil {
		return fmt.Errorf("set Windows service recovery action: %w", err)
	}
	if err := service.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("enable Windows service recovery action: %w", err)
	}
	return nil
}

func withService(action func(*mgr.Service) error) error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to Windows service manager: %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(Name)
	if err != nil {
		return fmt.Errorf("open Windows service: %w", err)
	}
	defer service.Close()
	return action(service)
}

func Start() error {
	return withService(func(service *mgr.Service) error {
		if err := service.Start(); err != nil {
			return fmt.Errorf("start Windows service: %w", err)
		}
		return waitForState(service, svc.Running, 30*time.Second)
	})
}

func Stop() error {
	return withService(func(service *mgr.Service) error {
		state, err := service.Query()
		if err != nil {
			return fmt.Errorf("query Windows service: %w", err)
		}
		if state.State == svc.Stopped {
			return nil
		}
		if _, err := service.Control(svc.Stop); err != nil {
			return fmt.Errorf("stop Windows service: %w", err)
		}
		return waitForState(service, svc.Stopped, 30*time.Second)
	})
}

func waitForState(service *mgr.Service, wanted svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state, err := service.Query()
		if err != nil {
			return fmt.Errorf("query Windows service: %w", err)
		}
		if state.State == wanted {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("Windows service did not reach state %s before timeout", stateName(wanted))
}

func Uninstall() error {
	return withService(func(service *mgr.Service) error {
		state, err := service.Query()
		if err == nil && state.State != svc.Stopped {
			return errors.New("stop the Windows service before uninstall")
		}
		if err := service.Delete(); err != nil {
			return fmt.Errorf("uninstall Windows service: %w", err)
		}
		return nil
	})
}

func Status() (string, error) {
	var name string
	err := withService(func(service *mgr.Service) error {
		state, err := service.Query()
		if err != nil {
			return fmt.Errorf("query Windows service: %w", err)
		}
		name = stateName(state.State)
		return nil
	})
	return name, err
}

func stateName(state svc.State) string {
	switch state {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "start-pending"
	case svc.StopPending:
		return "stop-pending"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "continue-pending"
	case svc.PausePending:
		return "pause-pending"
	case svc.Paused:
		return "paused"
	default:
		return strings.ToLower(fmt.Sprintf("state-%d", state))
	}
}
