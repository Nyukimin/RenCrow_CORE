package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr, time.Now))
}

func runCLI(args []string, out, errOut io.Writer, now func() time.Time) int {
	flags := flag.NewFlagSet("rencrow-check-plan-runner", flag.ContinueOnError)
	flags.SetOutput(errOut)
	manifest := flags.String("manifest", defaultManifestPath(), "CORE-owned check manifest JSON")
	coreURL := flags.String("core-url", defaultCoreURL, "loopback CORE base URL")
	planner := flags.String("planner", defaultPlanner, "fixed rencrow-check-plan executable")
	phase := flags.String("phase", "runtime", "requested check phase")
	snapshotDir := flags.String("snapshot-dir", "", "offline backup snapshot directory")
	nowText := flags.String("now", "", "explicit UTC evaluation time (RFC3339)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(errOut, "positional arguments are not accepted")
		return 2
	}

	evaluatedAt := now().UTC()
	if strings.TrimSpace(*nowText) != "" {
		parsed, err := time.Parse(time.RFC3339, *nowText)
		if err != nil {
			fmt.Fprintf(errOut, "invalid --now: %v\n", err)
			return 2
		}
		evaluatedAt = parsed.UTC()
	}
	receipt, err := runRunner(nil, runnerOptions{
		ManifestPath: *manifest,
		CoreURL:      *coreURL,
		Phase:        *phase,
		PlannerPath:  *planner,
		SnapshotDir:  *snapshotDir,
		Now:          evaluatedAt,
	}, out)
	if err == nil {
		return 0
	}
	if receipt.Status == "blocked" {
		return 3
	}
	return 1
}
