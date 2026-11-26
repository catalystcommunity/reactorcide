package worker

import (
	"fmt"
	"strings"
)

// maxLoggedCommand is the length of a job command in a log line. A job
// script can be many kilobytes; the full text is in the job definition.
const maxLoggedCommand = 160

// LogCommand gives a job command for a log line: on one line, and cut to
// maxLoggedCommand characters with the full length after it.
func LogCommand(command []string) string {
	text := strings.Join(strings.Fields(strings.Join(command, " ")), " ")
	runes := []rune(text)
	if len(runes) <= maxLoggedCommand {
		return text
	}
	return fmt.Sprintf("%s... (%d characters)", string(runes[:maxLoggedCommand]), len(runes))
}
