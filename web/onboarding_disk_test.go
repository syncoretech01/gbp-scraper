package web

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestDiskCapacityCheckClassifiesFreeSpace(t *testing.T) {
	t.Parallel()

	low := diskCapacityCheck(1<<30, 100<<30)
	if low.State != "warning" {
		t.Fatalf("state below 2 GB = %q, want warning", low.State)
	}

	if !strings.Contains(low.Message, fmt.Sprintf("%d bytes free", uint64(1<<30))) {
		t.Fatalf("warning message does not report free bytes: %q", low.Message)
	}

	if !strings.Contains(low.Message, "2 GB") {
		t.Fatalf("warning message does not explain the 2 GB threshold: %q", low.Message)
	}

	boundary := diskCapacityCheck(onboardingMinimumFreeDiskBytes, 100<<30)
	if boundary.State != "success" {
		t.Fatalf("state at exactly 2 GB = %q, want success", boundary.State)
	}

	healthy := diskCapacityCheck(50<<30, 100<<30)
	if healthy.State != "success" {
		t.Fatalf("state with 50 GB free = %q, want success", healthy.State)
	}

	if !strings.Contains(healthy.Message, fmt.Sprintf("%d bytes free", uint64(50<<30))) {
		t.Fatalf("success message does not report free bytes: %q", healthy.Message)
	}

	if healthy.Label != "Disk capacity" || low.Label != "Disk capacity" {
		t.Fatalf("labels = %q / %q, want \"Disk capacity\"", healthy.Label, low.Label)
	}
}

func TestOnboardingDiskCheckReadsDataFolder(t *testing.T) {
	t.Parallel()

	check := onboardingDiskCheck(context.Background(), t.TempDir())

	// A real volume backs the temporary directory, so the probe must succeed;
	// whether it warns depends on the machine running the test.
	if check.State != "success" && check.State != "warning" {
		t.Fatalf("state for a real data folder = %q (%s)", check.State, check.Message)
	}

	if !strings.Contains(check.Message, "bytes free") {
		t.Fatalf("check does not report free bytes: %q", check.Message)
	}
}
