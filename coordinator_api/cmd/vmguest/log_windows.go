//go:build windows

package main

// A Windows service has no standard error.
func defaultLogFile() string { return `C:\ProgramData\Reactorcide\vmguest.log` }
