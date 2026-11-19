package vmrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// runGuestCommand runs one command to completion and copies its output to
// log. The image builders use it.
func runGuestCommand(ctx context.Context, transport GuestTransport, addr GuestAddr, creds GuestCreds, command GuestCommand, log io.Writer) (int, error) {
	session, err := transport.Start(ctx, addr, creds, command)
	if err != nil {
		return -1, err
	}
	defer session.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(log, session.Stdout()) }()
	go func() { defer wg.Done(); _, _ = io.Copy(log, session.Stderr()) }()
	code, err := session.Wait()
	wg.Wait()
	return code, err
}

func validateGuestFile(file GuestFile) error {
	if strings.TrimSpace(file.Path) == "" || strings.ContainsAny(file.Path, "\x00\r\n") {
		return errors.New("vmrunner: guest file has an invalid path")
	}
	return nil
}

// writeGuestResult installs a result file from the guest with mode 0600.
func writeGuestResult(destination string, data []byte) error {
	dir := filepath.Dir(destination)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("vmrunner: create result directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".reactorcide-result-*")
	if err != nil {
		return fmt.Errorf("vmrunner: create result file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("vmrunner: secure result file: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("vmrunner: write result file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("vmrunner: close result file: %w", err)
	}
	if err := os.Rename(tempPath, destination); err != nil {
		if removeErr := os.Remove(destination); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("vmrunner: replace result file: %w", err)
		}
		if retryErr := os.Rename(tempPath, destination); retryErr != nil {
			return fmt.Errorf("vmrunner: install result file: %w", retryErr)
		}
	}
	return nil
}
