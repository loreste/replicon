package replication

import "testing"

func TestPromoteStandbyDryRun(t *testing.T) {
	result, err := PromoteStandby(testConfig(), OperationOptions{Execute: false})
	if err != nil {
		t.Fatalf("promote dry run: %v", err)
	}
	if result.Action != "promote" {
		t.Fatalf("unexpected action: %#v", result)
	}
}

func TestRejoinOldPrimaryDryRun(t *testing.T) {
	result, err := RejoinOldPrimary(testConfig(), OperationOptions{Execute: false})
	if err != nil {
		t.Fatalf("rejoin dry run: %v", err)
	}
	if result.Action != "rejoin" {
		t.Fatalf("unexpected action: %#v", result)
	}
}
