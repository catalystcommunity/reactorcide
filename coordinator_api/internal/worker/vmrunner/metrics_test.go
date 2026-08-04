package vmrunner

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseResourceSample(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	sample, err := parseResourceSample("125.5\t2.25\t4294967296\t8589934592\t10737418240\t68719476736", "job-1", now)
	require.NoError(t, err)
	require.Equal(t, now, sample.Timestamp)
	require.Equal(t, "job-1", sample.JobID)
	require.Equal(t, 125.5, sample.CPUPercent)
	require.Equal(t, 2.25, sample.Load1)
	require.Equal(t, uint64(4294967296), sample.MemoryUsedBytes)
	require.Equal(t, uint64(8589934592), sample.MemoryTotalBytes)
	require.Equal(t, uint64(10737418240), sample.StorageUsedBytes)
	require.Equal(t, uint64(68719476736), sample.StorageTotalBytes)
}

func TestParseResourceSampleRejectsInvalidInput(t *testing.T) {
	_, err := parseResourceSample("not-a-sample", "job-1", time.Now())
	require.Error(t, err)
}
