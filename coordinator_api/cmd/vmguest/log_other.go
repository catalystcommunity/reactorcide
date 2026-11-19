//go:build !windows

package main

// launchd sends standard error to the log file of the daemon.
func defaultLogFile() string { return "" }
