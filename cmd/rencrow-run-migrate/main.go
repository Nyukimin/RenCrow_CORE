// rencrow-run-migrate builds an offline, hash-bound Step10 cohort. Installing
// this cohort is a separate writer-stopped cutover operation.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	migration "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/runmigration"
)

var cutoverOperation = migration.Cutover

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }
func run(ctx context.Context, args []string, out, errOut io.Writer) int {
	f := flag.NewFlagSet("rencrow-run-migrate", flag.ContinueOnError)
	f.SetOutput(errOut)
	mode := f.String("mode", "dry-run", "dry-run, apply, quarantine, quarantine-apply, or cutover")
	snapshot := f.String("snapshot", "", "dedicated immutable snapshot directory")
	target := f.String("output", "", "fresh offline cohort directory")
	cohort := f.String("cohort", "", "published offline cohort required by cutover")
	inventory := f.String("inventory", "", "strict source inventory JSON outside snapshot")
	prior := f.String("dry-run-receipt", "", "ready receipt required by apply")
	quarantinePrior := f.String("quarantine-receipt", "", "quarantined receipt required by quarantine-apply")
	expectedQuarantineReceiptSHA256 := f.String("expected-quarantine-receipt-sha256", "", "expected quarantine receipt SHA-256")
	activeManifest := f.String("active-manifest", "", "hash-bound active Step10 store manifest")
	expectedActiveManifestSHA256 := f.String("expected-active-manifest-sha256", "", "expected active manifest SHA-256")
	rollbackDir := f.String("rollback-dir", "", "fresh rollback directory")
	cutoverReceipt := f.String("cutover-receipt", "", "fresh durable cutover receipt")
	installedRuntime := f.String("installed-runtime", "", "installed CORE runtime")
	expectedRuntimeSHA256 := f.String("expected-runtime-sha256", "", "expected installed runtime SHA-256")
	activeConfig := f.String("active-config", "", "active CORE config")
	if e := f.Parse(args); e != nil {
		return 2
	}
	if f.NArg() != 0 {
		return 2
	}
	if *mode == "cutover" {
		values := []string{*snapshot, *cohort, *inventory, *quarantinePrior, *expectedQuarantineReceiptSHA256, *activeManifest, *expectedActiveManifestSHA256, *rollbackDir, *cutoverReceipt, *installedRuntime, *expectedRuntimeSHA256, *activeConfig}
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				fmt.Fprintln(errOut, "cutover requires all owner flags")
				return 2
			}
		}
		if *target != "" || *prior != "" {
			fmt.Fprintln(errOut, "cutover rejects offline apply flags")
			return 2
		}
		var inv migration.Inventory
		var plan migration.Receipt
		var active migration.RunCutoverActiveManifest
		if readJSON(*inventory, &inv) != nil || readJSON(*quarantinePrior, &plan) != nil || readJSON(*activeManifest, &active) != nil {
			fmt.Fprintln(errOut, "invalid cutover owner input")
			return 2
		}
		receipt, err := cutoverOperation(ctx, migration.CutoverOptions{
			Cohort: *cohort, Snapshot: *snapshot, Inventory: inv, PlanReceipt: plan,
			ExpectedPlanReceiptSHA256: *expectedQuarantineReceiptSHA256,
			Active:                    active, ExpectedActiveManifestSHA256: *expectedActiveManifestSHA256,
			RollbackDir: *rollbackDir, CutoverReceipt: *cutoverReceipt,
			InstalledRuntime: *installedRuntime, ExpectedRuntimeSHA256: *expectedRuntimeSHA256,
			ActiveConfig: *activeConfig,
		})
		if writeErr := json.NewEncoder(out).Encode(receipt); writeErr != nil {
			return 1
		}
		if err != nil {
			fmt.Fprintln(errOut, receipt.ErrorCode)
			return 1
		}
		return 0
	}
	cutoverOnly := []string{*cohort, *expectedQuarantineReceiptSHA256, *activeManifest, *expectedActiveManifestSHA256, *rollbackDir, *cutoverReceipt, *installedRuntime, *expectedRuntimeSHA256, *activeConfig}
	for _, value := range cutoverOnly {
		if strings.TrimSpace(value) != "" {
			fmt.Fprintln(errOut, "cutover owner flags require --mode cutover")
			return 2
		}
	}
	if (*prior != "" && *quarantinePrior != "") || (*prior != "" && *mode != "apply") || (*quarantinePrior != "" && *mode != "quarantine-apply") {
		fmt.Fprintln(errOut, "receipt flag does not match mode")
		return 2
	}
	var inv migration.Inventory
	if e := readJSON(*inventory, &inv); e != nil {
		fmt.Fprintln(errOut, "invalid inventory")
		return 2
	}
	o := migration.Options{Snapshot: *snapshot, Target: *target, Mode: *mode, Inventory: inv}
	if *prior != "" {
		var r migration.Receipt
		if e := readJSON(*prior, &r); e != nil {
			fmt.Fprintln(errOut, "invalid dry-run receipt")
			return 2
		}
		o.Expected = &r
	}
	if *quarantinePrior != "" {
		var r migration.Receipt
		if e := readJSON(*quarantinePrior, &r); e != nil {
			fmt.Fprintln(errOut, "invalid quarantine receipt")
			return 2
		}
		o.Expected = &r
	}
	r, e := migration.Run(ctx, o)
	if writeErr := json.NewEncoder(out).Encode(r); writeErr != nil {
		return 1
	}
	if e != nil {
		fmt.Fprintln(errOut, e)
		return 1
	}
	return 0
}
func readJSON(path string, v any) error {
	info, e := os.Lstat(path)
	if e != nil {
		return e
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return fmt.Errorf("unsafe input")
	}
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if e != nil {
		return e
	}
	if len(b) > 4<<20 {
		return fmt.Errorf("input exceeds bound")
	}
	opened, e := f.Stat()
	if e != nil || !os.SameFile(info, opened) || opened.Size() != int64(len(b)) {
		return fmt.Errorf("input changed")
	}
	latest, e := os.Lstat(path)
	if e != nil || !os.SameFile(info, latest) || latest.Size() != info.Size() || !latest.ModTime().Equal(info.ModTime()) {
		return fmt.Errorf("input changed")
	}
	return migration.DecodeJSON(b, v)
}
