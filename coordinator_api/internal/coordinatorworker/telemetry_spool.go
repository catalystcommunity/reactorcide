package coordinatorworker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

type spoolRecord struct {
	Kind    string                            `json:"kind"`
	Log     *csilapi.AppendLogBatchRequest    `json:"log,omitempty"`
	Metrics *csilapi.AppendMetricBatchRequest `json:"metrics,omitempty"`
}

func telemetrySpoolRoot(dataDir string) string {
	if strings.TrimSpace(dataDir) == "" {
		return ""
	}
	return filepath.Join(dataDir, "telemetry")
}

func persistAndSendLog(c client, dataDir string, req csilapi.AppendLogBatchRequest) error {
	record := spoolRecord{Kind: "log", Log: &req}
	path, err := writeSpoolRecord(dataDir, req.LeaseId, "log-"+req.Stream, req.Sequence, record)
	if err != nil {
		return err
	}
	if _, err := c.AppendLogBatch(context.Background(), req); err != nil {
		return err
	}
	return removeSpoolRecord(path)
}

func persistAndSendMetrics(c client, dataDir string, req csilapi.AppendMetricBatchRequest) error {
	record := spoolRecord{Kind: "metrics", Metrics: &req}
	path, err := writeSpoolRecord(dataDir, req.LeaseId, "metrics", req.Sequence, record)
	if err != nil {
		return err
	}
	if _, err := c.AppendMetricBatch(context.Background(), req); err != nil {
		return err
	}
	return removeSpoolRecord(path)
}

func writeSpoolRecord(dataDir, leaseID, kind string, sequence int64, record spoolRecord) (string, error) {
	root := telemetrySpoolRoot(dataDir)
	if root == "" {
		return "", nil
	}
	dir := filepath.Join(root, safeSpoolName(leaseID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create telemetry spool: %w", err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		return "", fmt.Errorf("encode telemetry spool: %w", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%020d.json", safeSpoolName(kind), sequence))
	tmp, err := os.CreateTemp(dir, ".telemetry-*")
	if err != nil {
		return "", fmt.Errorf("create telemetry spool record: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write telemetry spool record: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", fmt.Errorf("sync telemetry spool record: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", fmt.Errorf("publish telemetry spool record: %w", err)
	}
	if directory, openErr := os.Open(dir); openErr == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return path, nil
}

func removeSpoolRecord(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = os.Remove(filepath.Dir(path))
	return nil
}

func replayTelemetrySpool(c client, dataDir string) {
	root := telemetrySpoolRoot(dataDir)
	if root == "" {
		return
	}
	paths := []string{}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var record spoolRecord
		if json.Unmarshal(data, &record) != nil {
			logging.Log.WithField("spool_file", filepath.Base(path)).Warn("ignored invalid telemetry spool record")
			continue
		}
		sent := false
		switch {
		case record.Kind == "log" && record.Log != nil:
			_, err = c.AppendLogBatch(context.Background(), *record.Log)
			sent = err == nil
		case record.Kind == "metrics" && record.Metrics != nil:
			_, err = c.AppendMetricBatch(context.Background(), *record.Metrics)
			sent = err == nil
		}
		if sent {
			_ = removeSpoolRecord(path)
		}
	}
}

func telemetryReplayLoop(ctx context.Context, c client, dataDir string, interval time.Duration) {
	if telemetrySpoolRoot(dataDir) == "" {
		return
	}
	if interval <= 0 {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			replayTelemetrySpool(c, dataDir)
		}
	}
}

func safeSpoolName(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, value)
}
