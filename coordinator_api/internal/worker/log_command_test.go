package worker

import (
	"strings"
	"testing"
)

func TestLogCommand(t *testing.T) {
	if got := LogCommand([]string{"sh", "-c", "make\n  test"}); got != "sh -c make test" {
		t.Fatalf("a short command is kept on one line: %q", got)
	}
	long := LogCommand([]string{"sh", "-c", strings.Repeat("echo step\n", 500)})
	if len([]rune(long)) > maxLoggedCommand+40 || !strings.HasSuffix(long, "characters)") || strings.Contains(long, "\n") {
		t.Fatalf("a long command must be cut: %q", long)
	}
}
