package cmd

import (
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/runshifts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/urfave/cli/v2"
)

// shiftFlags are the optional shifts of a retry or a re-run (D18, D25). The
// new run keeps the authority of the original run; nothing else can change.
func shiftFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "cpu", Usage: "New CPU limit, for example 4 or 2500m"},
		&cli.StringFlag{Name: "cpu-request", Usage: "New CPU request"},
		&cli.StringFlag{Name: "memory", Usage: "New memory limit, for example 8Gi"},
		&cli.StringFlag{Name: "storage", Usage: "New ephemeral storage limit, for example 20Gi"},
		&cli.StringFlag{Name: "storage-request", Usage: "New ephemeral storage request"},
		&cli.StringFlag{Name: "timeout", Usage: "New timeout, for example 2h or 7200 (seconds)"},
	}
}

// shiftsFromFlags returns the shifts object of the flags, or nil when no
// shift flag is set. It validates the values with the coordinator's parser,
// so a typing error stops before the request.
func shiftsFromFlags(ctx *cli.Context) (map[string]any, error) {
	raw := map[string]any{}
	for flag, key := range map[string]string{"cpu": "cpu", "cpu-request": "cpu_request", "memory": "memory",
		"storage": "storage", "storage-request": "storage_request", "timeout": "timeout"} {
		if value := ctx.String(flag); value != "" {
			raw[key] = value
		}
	}
	if len(raw) == 0 {
		return nil, nil
	}
	if _, err := runshifts.ParseMap(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// shiftsBody is the REST body of a retry or a re-run: nil, or {"shifts": ...}.
func shiftsBody(ctx *cli.Context) (any, error) {
	shifts, err := shiftsFromFlags(ctx)
	if err != nil || shifts == nil {
		return nil, err
	}
	return map[string]any{"shifts": shifts}, nil
}

// csilShifts is the CSIL form of a shifts object, or nil.
func csilShifts(shifts map[string]any) *csilapi.RunShifts {
	if shifts == nil {
		return nil
	}
	text := func(key string) *string {
		if value, ok := shifts[key].(string); ok && value != "" {
			return &value
		}
		return nil
	}
	return &csilapi.RunShifts{Cpu: text("cpu"), CpuRequest: text("cpu_request"), Memory: text("memory"),
		Storage: text("storage"), StorageRequest: text("storage_request"), Timeout: text("timeout")}
}
