// Package runmigration builds an offline Step10 cohort. It never opens live
// stores or installs its output. The input inventory is explicit and hash-bound.
package runmigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const Schema = "rencrow.identity.run-migration/v1"

// Inventory accounts for every Step10 store, including explicitly absent ones.
// Files maps relative snapshot paths to hashes; Roles maps fixed roles to paths.
// SnapshotAt is evidence time, never the wall clock of a retry.
type Inventory struct {
	SchemaVersion string            `json:"schema_version"`
	SnapshotAt    time.Time         `json:"snapshot_at"`
	Files         map[string]string `json:"files"`
	Roles         map[string]string `json:"roles"`
}

var roles = []string{"tasks", "runs", "contexts", "notifications", "events", "superagent", "browser", "knowledge", "word", "forecast", "story", "dialogue", "checkpoints"}

type Options struct {
	Snapshot  string
	Target    string
	Mode      string
	Inventory Inventory
	Expected  *Receipt
}

type Receipt struct {
	SchemaVersion   string            `json:"schema_version"`
	Status          string            `json:"status"`
	InventorySHA256 string            `json:"inventory_sha256"`
	Inputs          map[string]string `json:"inputs"`
	Outputs         map[string]string `json:"outputs"`
	Counts          map[string]int    `json:"counts"`
	ErrorCode       string            `json:"error_code,omitempty"`
}

func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func Run(ctx context.Context, o Options) (r Receipt, err error) {
	r = Receipt{SchemaVersion: Schema, Status: "blocked"}
	defer func() {
		if err != nil {
			r.Status = "blocked"
			r.ErrorCode = "migration_rejected"
		}
	}()
	if ctx == nil {
		return r, errors.New("context is required")
	}
	if err = ctx.Err(); err != nil {
		return r, err
	}
	if o.Mode != "dry-run" && o.Mode != "apply" {
		return r, errors.New("mode must be dry-run or apply")
	}
	if o.Inventory.SchemaVersion != Schema || o.Inventory.SnapshotAt.IsZero() {
		return r, errors.New("invalid inventory header")
	}
	if len(o.Inventory.Roles) != len(roles) {
		return r, errors.New("every cohort role must be declared, including absent roles")
	}
	seen := map[string]bool{}
	for _, role := range roles {
		p, ok := o.Inventory.Roles[role]
		if !ok {
			return r, fmt.Errorf("missing role %s", role)
		}
		if p != "" {
			if seen[p] {
				return r, errors.New("source role alias")
			}
			seen[p] = true
			if _, ok := o.Inventory.Files[p]; !ok {
				return r, errors.New("role has no source hash")
			}
		}
	}
	if len(seen) != len(o.Inventory.Files) {
		return r, errors.New("unowned source file")
	}
	a, e := filepath.Abs(o.Snapshot)
	if e != nil {
		return r, e
	}
	b, e := filepath.Abs(o.Target)
	if e != nil {
		return r, e
	}
	if strings.TrimSpace(o.Target) == "" || pathContains(a, b) || pathContains(b, a) {
		return r, errors.New("snapshot and output must be disjoint")
	}
	input, e := readSnapshotFiles(o.Snapshot, o.Inventory.Files)
	if e != nil {
		return r, e
	}
	encoded, e := json.Marshal(o.Inventory)
	if e != nil {
		return r, e
	}
	r.InventorySHA256 = digest(encoded)
	r.Inputs = o.Inventory.Files
	outputs, counts, e := buildCohort(ctx, o.Inventory, input)
	if e != nil {
		return r, e
	}
	r.Outputs = map[string]string{}
	for p, data := range outputs {
		r.Outputs[p] = digest(data)
	}
	r.Counts = counts
	// Re-read the exact inventory after transformation; no drift may be hidden by
	// cached source bytes or a successful output build.
	if _, e = readSnapshotFiles(o.Snapshot, o.Inventory.Files); e != nil {
		return r, e
	}
	if e = ctx.Err(); e != nil {
		return r, e
	}
	if o.Mode == "dry-run" {
		r.Status = "ready"
		return r, nil
	}
	if o.Expected == nil || o.Expected.SchemaVersion != Schema || o.Expected.Status != "ready" || o.Expected.ErrorCode != "" {
		return r, errors.New("apply requires a ready dry-run receipt")
	}
	expected := *o.Expected
	actual := r
	expected.Status = ""
	actual.Status = ""
	x, _ := json.Marshal(expected)
	y, _ := json.Marshal(actual)
	if string(x) != string(y) {
		return r, errors.New("dry-run receipt mismatch")
	}
	r.Status, e = publishCohort(o.Target, outputs)
	if e != nil {
		return r, e
	}
	return r, nil
}

func pathContains(root, p string) bool {
	rel, e := filepath.Rel(root, p)
	return e == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}
