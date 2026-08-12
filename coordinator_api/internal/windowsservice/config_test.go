package windowsservice

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.json")
	data := `{"arguments":["worker","--container-runtime","vm"],"environment":{"REACTORCIDE_VM_IMAGE_SOURCE":"local"},"log_file":"worker.log"}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(config.Arguments, " "); got != "worker --container-runtime vm" {
		t.Fatalf("arguments = %q", got)
	}
}

func TestConfigRejectsPlaintextEnrollmentToken(t *testing.T) {
	config := Config{
		Arguments:   []string{"worker"},
		Environment: map[string]string{"REACTORCIDE_WORKER_ENROLLMENT_TOKEN": "secret"},
		LogFile:     "worker.log",
	}
	if err := config.Validate(); err == nil {
		t.Fatal("expected plaintext enrollment token to be rejected")
	}
}

func TestConfigRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.json")
	if err := os.WriteFile(path, []byte(`{"arguments":["worker"],"log_file":"worker.log","extra":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected unknown field to be rejected")
	}
}

func TestLoadConfigAcceptsUTF8ByteOrderMark(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.json")
	data := "\ufeff" + `{"arguments":["worker"],"log_file":"worker.log"}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
}
