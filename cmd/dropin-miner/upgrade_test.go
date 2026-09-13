package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/twilight-project/dropin-miner/internal/selfupdate"
)

func TestUpgradeRefusesMalformedFlagsBeforeAnyOperation(t *testing.T) {
	for _, args := range [][]string{{"-version", "bad"}, {"-version", "0.3.0", "-rollback"}, {"extra"}} {
		var stdout, stderr bytes.Buffer
		if code := cmdUpgrade(args, &stdout, &stderr); code != 2 {
			t.Fatalf("cmdUpgrade(%v) code = %d, stderr=%q", args, code, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Fatalf("cmdUpgrade(%v) wrote stdout %q", args, stdout.String())
		}
	}
}

func TestUpgradeOwnershipPolicyRefusesNPMAndAmbiguousInstallations(t *testing.T) {
	if err := requireNativeUpgradeOwnership(selfupdate.OwnershipNative, nil); err != nil {
		t.Fatalf("native install refused: %v", err)
	}
	if err := requireNativeUpgradeOwnership(selfupdate.OwnershipNPM, nil); err == nil || !strings.Contains(err.Error(), "managed by npm") {
		t.Fatalf("npm refusal = %v", err)
	}
	detectionErr := errors.New("partial metadata")
	if err := requireNativeUpgradeOwnership(selfupdate.OwnershipAmbiguous, detectionErr); !errors.Is(err, detectionErr) {
		t.Fatalf("ambiguous refusal = %v", err)
	}
}

func TestDevelopmentBuildRefusesSelfUpgrade(t *testing.T) {
	oldVersion := version
	version = "dev"
	t.Cleanup(func() { version = oldVersion })
	var stdout, stderr bytes.Buffer
	if code := cmdUpgrade(nil, &stdout, &stderr); code != 1 {
		t.Fatalf("code = %d, stderr=%q", code, stderr.String())
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "not a release build") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
