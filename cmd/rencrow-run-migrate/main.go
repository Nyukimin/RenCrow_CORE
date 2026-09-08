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

	migration "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/runmigration"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }
func run(ctx context.Context, args []string, out, errOut io.Writer) int {
	f := flag.NewFlagSet("rencrow-run-migrate", flag.ContinueOnError)
	f.SetOutput(errOut)
	mode := f.String("mode", "dry-run", "dry-run or apply (offline output only)")
	snapshot := f.String("snapshot", "", "dedicated immutable snapshot directory")
	target := f.String("output", "", "fresh offline cohort directory")
	inventory := f.String("inventory", "", "strict source inventory JSON outside snapshot")
	prior := f.String("dry-run-receipt", "", "ready receipt required by apply")
	if e := f.Parse(args); e != nil {
		return 2
	}
	if f.NArg() != 0 {
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
