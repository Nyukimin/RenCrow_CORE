// Command rencrow-core-migration-package converts a verified CORE backup
// cohort into a RenCrow Workspace owner state package.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Nyukimin/RenCrow_CORE/internal/migrationpackage"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr, migrationpackage.Build); err != nil {
		fmt.Fprintln(os.Stderr, "[NG] CORE migration package unavailable")
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer, build func(string, string) error) error {
	flags := flag.NewFlagSet("rencrow-core-migration-package", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var snapshotDir, outputDir string
	flags.StringVar(&snapshotDir, "snapshot-dir", "", "restore-checked CORE snapshot cohort")
	flags.StringVar(&outputDir, "output-dir", "", "existing empty owner-only output directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || snapshotDir == "" || outputDir == "" {
		return fmt.Errorf("--snapshot-dir and --output-dir are required")
	}
	if build == nil {
		return fmt.Errorf("packager unavailable")
	}
	if err := build(snapshotDir, outputDir); err != nil {
		return err
	}
	_, err := fmt.Fprintln(stdout, `{"contract_version":"rencrow-core-migration-package/v1","status":"completed"}`)
	return err
}
