package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/twilight-project/dropin-miner/internal/selfupdate"
)

const upgradeOperationTimeout = 3 * time.Minute

func cmdUpgrade(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	flags.SetOutput(stderr)
	requestedRaw := flags.String("version", "", "install exact stable version X.Y.Z")
	rollback := flags.Bool("rollback", false, "restore the one preserved previous version")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || (*rollback && *requestedRaw != "") {
		fmt.Fprintln(stderr, "usage: dropin-miner upgrade [-version X.Y.Z | -rollback]")
		return 2
	}
	var requested *selfupdate.Version
	if *requestedRaw != "" {
		parsed, parseErr := selfupdate.ParseVersion(*requestedRaw)
		if parseErr != nil {
			fmt.Fprintf(stderr, "dropin-miner upgrade: invalid -version: %v\n", parseErr)
			return 2
		}
		requested = &parsed
	}
	current, err := selfupdate.ParseVersion(buildVersion())
	if err != nil {
		fmt.Fprintf(stderr, "dropin-miner upgrade: this is not a release build (%s); install a released binary before using self-upgrade\n", buildVersion())
		return 1
	}
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "dropin-miner upgrade: locate executable: %v\n", err)
		return 1
	}
	ownership, resolved, err := selfupdate.DetectOwnership(executable)
	if ownershipErr := requireNativeUpgradeOwnership(ownership, err); ownershipErr != nil {
		fmt.Fprintf(stderr, "dropin-miner upgrade: %v\n", ownershipErr)
		return 1
	}
	releaseLock, err := selfupdate.AcquireLifecycleLock(resolved)
	if err != nil {
		fmt.Fprintf(stderr, "dropin-miner upgrade: %v\n", err)
		return 1
	}
	defer releaseLock()

	ctx, cancel := context.WithTimeout(context.Background(), upgradeOperationTimeout)
	defer cancel()
	strategy := selfupdate.ReplacePOSIX
	if runtime.GOOS == "windows" {
		strategy = selfupdate.ReplaceWindows
	}
	updater := selfupdate.Updater{
		Source:   selfupdate.NewHTTPSource(nil),
		Verifier: selfupdate.SHA256Verifier{},
		Runner:   selfupdate.ExecRunner{},
		GOOS:     runtime.GOOS,
		GOARCH:   runtime.GOARCH,
		Strategy: strategy,
	}
	var result selfupdate.Result
	if *rollback {
		result, err = updater.Rollback(ctx, resolved, current)
	} else {
		result, err = updater.Upgrade(ctx, resolved, current, requested)
	}
	if err != nil {
		fmt.Fprintf(stderr, "dropin-miner upgrade: %v\n", err)
		return 1
	}
	if result.NoChange {
		fmt.Fprintf(stdout, "dropin-miner %s is already installed\n", result.To)
	} else if result.RolledBack {
		fmt.Fprintf(stdout, "dropin-miner rolled back from %s to %s\n", result.From, result.To)
	} else {
		fmt.Fprintf(stdout, "dropin-miner upgraded from %s to %s\n", result.From, result.To)
	}
	return 0
}

func requireNativeUpgradeOwnership(ownership selfupdate.InstallationOwnership, detectionErr error) error {
	switch ownership {
	case selfupdate.OwnershipNative:
		return detectionErr
	case selfupdate.OwnershipNPM:
		return fmt.Errorf("this binary is managed by npm; upgrade the npm package instead (globally: npm install -g dropin-miner@latest)")
	default:
		return fmt.Errorf("installation ownership is ambiguous; refusing self-replacement: %w", detectionErr)
	}
}
