package vmguest

import "strconv"

// taskkillArgs are the taskkill.exe arguments that stop a process tree.
func taskkillArgs(pid int) []string {
	return []string{"/T", "/F", "/PID", strconv.Itoa(pid)}
}
