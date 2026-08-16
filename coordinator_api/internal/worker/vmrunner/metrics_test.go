package vmrunner

import (
	"encoding/base64"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/stretchr/testify/require"
)

func TestEncodePowerShellCommand(t *testing.T) {
	want := `$value="quoted"; Write-Output $value`
	raw, err := base64.StdEncoding.DecodeString(encodePowerShellCommand(want))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw)%2 != 0 {
		t.Fatalf("encoded command has odd byte length %d", len(raw))
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = uint16(raw[i*2]) | uint16(raw[i*2+1])<<8
	}
	if got := string(utf16.Decode(units)); got != want {
		t.Fatalf("decoded command = %q, want %q", got, want)
	}
}

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

func TestParseResourceSampleWithWindowsCommitAndSwap(t *testing.T) {
	sample, err := parseResourceSample("50\t0\t100\t200\t300\t400\t150\t50\t4", "job-windows", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if sample.MemoryCommittedBytes != 150 || sample.SwapUsedBytes != 50 {
		t.Fatalf("unexpected Windows memory values: %+v", sample)
	}
	if sample.CPUCount != 4 {
		t.Fatalf("CPUCount = %d, want 4", sample.CPUCount)
	}
}

func TestParseResourceSampleWithCPUCount(t *testing.T) {
	sample, err := parseResourceSample("50\t0\t100\t200\t300\t400\t6", "job-macos", time.Now())
	require.NoError(t, err)
	require.Equal(t, uint64(6), sample.CPUCount)
}
