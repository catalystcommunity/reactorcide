package worker

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	RunnerUser = "1001:1001"
	RootUser   = "0:0"
)

// NormalizeRunAsUser converts a job run_as.user value into a runtime user
// string. Deployed workers intentionally do not support "host" because there
// is no stable host user to map to across VM and Kubernetes runtimes.
func NormalizeRunAsUser(user string) (string, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return "", nil
	}

	switch strings.ToLower(user) {
	case "runner":
		return RunnerUser, nil
	case "root":
		return RootUser, nil
	case "host":
		return "", fmt.Errorf("run_as.user %q is only valid under run_local.user", user)
	}

	parts := strings.SplitN(user, ":", 2)
	uid, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return "", fmt.Errorf("invalid run_as.user %q: uid must be an integer", user)
	}
	gid := uid
	if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
		gid, err = strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return "", fmt.Errorf("invalid run_as.user %q: gid must be an integer", user)
		}
	}

	return fmt.Sprintf("%d:%d", uid, gid), nil
}

func DefaultRunAsUser(user string) (string, error) {
	normalized, err := NormalizeRunAsUser(user)
	if err != nil {
		return "", err
	}
	if normalized == "" {
		return RunnerUser, nil
	}
	return normalized, nil
}
