package dcimigration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestPrepareCutoverBuildCohortBindsReadyBuildWithoutMutation(t *testing.T) {
	prepared := newBuildOutputsPreparedFixture(t)
	buildReceipt, err := Build(context.Background(), buildOptionsFromPrepared(prepared))
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	options, runtimePaths := newCutoverBuildCohortFixture(t, prepared.paths.buildDir, buildReceipt)
	before := cutoverBuildCohortHashes(t, options, runtimePaths)

	bundle, err := prepareCutoverBuildCohort(context.Background(), options)
	if err != nil {
		t.Fatalf("prepareCutoverBuildCohort() error = %v", err)
	}
	if bundle.buildReceiptSHA256 != options.ExpectedBuildReceiptSHA256 || len(bundle.outputFiles) != 4 || len(bundle.files) != 7 {
		t.Fatalf("prepared bundle = %#v", bundle)
	}
	if !samePath(bundle.paths.buildRoot, options.BuildRoot) || !samePath(bundle.paths.buildReceipt, options.BuildReceipt) {
		t.Fatalf("prepared paths = %#v", bundle.paths)
	}
	for _, path := range []string{options.RollbackDir, options.CutoverReceipt} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("preparation created prospective path %q: %v", path, err)
		}
	}
	if after := cutoverBuildCohortHashes(t, options, runtimePaths); !mapsEqual(before, after) {
		t.Fatalf("preparation changed inputs: before=%#v after=%#v", before, after)
	}
}

func TestPrepareCutoverBuildCohortRejectsBuildReceiptAndOutputTamper(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, options cutoverArtifactOptions)
	}{
		{
			name: "receipt bytes",
			mutate: func(t *testing.T, options cutoverArtifactOptions) {
				if err := os.WriteFile(options.BuildReceipt, append(mustReadFile(t, options.BuildReceipt), '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "output bytes",
			mutate: func(t *testing.T, options cutoverArtifactOptions) {
				path := filepath.Join(options.BuildRoot, buildOutputDCIFilename)
				if err := os.WriteFile(path, append(mustReadFile(t, path), 'x'), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "output owner binding",
			mutate: func(t *testing.T, options cutoverArtifactOptions) {
				var receipt BuildReceipt
				data := mustReadFile(t, options.BuildReceipt)
				if err := json.Unmarshal(data, &receipt); err != nil {
					t.Fatal(err)
				}
				receipt.OutputArtifacts[buildOutputDCIRole] = receipt.OutputArtifacts[buildOutputEventStoreRole]
				writeCutoverTestReceipt(t, options.BuildReceipt, receipt)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prepared := newBuildOutputsPreparedFixture(t)
			buildReceipt, err := Build(context.Background(), buildOptionsFromPrepared(prepared))
			if err != nil {
				t.Fatal(err)
			}
			options, _ := newCutoverBuildCohortFixture(t, prepared.paths.buildDir, buildReceipt)
			tt.mutate(t, options)
			if _, err := prepareCutoverBuildCohort(context.Background(), options); err == nil {
				t.Fatal("prepareCutoverBuildCohort() unexpectedly accepted tampering")
			} else if containsCutoverPrivateValue(err, options) {
				t.Fatalf("error leaked private value: %v", err)
			}
			if _, err := os.Lstat(options.RollbackDir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("tampered preparation created rollback path: %v", err)
			}
			if _, err := os.Lstat(options.CutoverReceipt); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("tampered preparation created receipt path: %v", err)
			}
		})
	}
}

func TestPrepareCutoverBuildCohortRejectsExpectedHashAndRuntimeDrift(t *testing.T) {
	prepared := newBuildOutputsPreparedFixture(t)
	buildReceipt, err := Build(context.Background(), buildOptionsFromPrepared(prepared))
	if err != nil {
		t.Fatal(err)
	}
	base, runtimePaths := newCutoverBuildCohortFixture(t, prepared.paths.buildDir, buildReceipt)
	tests := []struct {
		name   string
		mutate func(*cutoverArtifactOptions)
	}{
		{name: "build receipt hash", mutate: func(value *cutoverArtifactOptions) { value.ExpectedBuildReceiptSHA256 = strings.Repeat("0", 64) }},
		{name: "installed runtime", mutate: func(value *cutoverArtifactOptions) { value.ExpectedInstalledRuntimeSHA256 = strings.Repeat("0", 64) }},
		{name: "staged runtime", mutate: func(value *cutoverArtifactOptions) { value.ExpectedStagedRuntimeSHA256 = strings.Repeat("0", 64) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := base
			tt.mutate(&options)
			if _, err := prepareCutoverBuildCohort(context.Background(), options); err == nil {
				t.Fatal("prepareCutoverBuildCohort() unexpectedly accepted drift")
			}
		})
	}
	for _, path := range []string{runtimePaths.installed, runtimePaths.staged} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPrepareCutoverBuildCohortRejectsUnsafeRuntimeModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("runtime permission contract is exercised on Unix")
	}
	tests := []struct {
		name string
		path func(cutoverRuntimeTestPaths) string
		mode os.FileMode
	}{
		{name: "installed non executable", path: func(paths cutoverRuntimeTestPaths) string { return paths.installed }, mode: 0o600},
		{name: "staged non executable", path: func(paths cutoverRuntimeTestPaths) string { return paths.staged }, mode: 0o600},
		{name: "installed group writable", path: func(paths cutoverRuntimeTestPaths) string { return paths.installed }, mode: 0o720},
		{name: "staged group writable", path: func(paths cutoverRuntimeTestPaths) string { return paths.staged }, mode: 0o720},
		{name: "installed other writable", path: func(paths cutoverRuntimeTestPaths) string { return paths.installed }, mode: 0o702},
		{name: "staged other writable", path: func(paths cutoverRuntimeTestPaths) string { return paths.staged }, mode: 0o702},
		{name: "installed setuid", path: func(paths cutoverRuntimeTestPaths) string { return paths.installed }, mode: os.ModeSetuid | 0o700},
		{name: "staged setuid", path: func(paths cutoverRuntimeTestPaths) string { return paths.staged }, mode: os.ModeSetuid | 0o700},
		{name: "installed setgid", path: func(paths cutoverRuntimeTestPaths) string { return paths.installed }, mode: os.ModeSetgid | 0o700},
		{name: "staged setgid", path: func(paths cutoverRuntimeTestPaths) string { return paths.staged }, mode: os.ModeSetgid | 0o700},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prepared := newBuildOutputsPreparedFixture(t)
			receipt, err := Build(context.Background(), buildOptionsFromPrepared(prepared))
			if err != nil {
				t.Fatal(err)
			}
			options, paths := newCutoverBuildCohortFixture(t, prepared.paths.buildDir, receipt)
			if err := os.Chmod(tt.path(paths), tt.mode); err != nil {
				t.Fatal(err)
			}
			if tt.mode&(os.ModeSetuid|os.ModeSetgid) != 0 {
				info, err := os.Lstat(tt.path(paths))
				if err != nil {
					t.Fatal(err)
				}
				want := os.FileMode(0)
				if tt.mode&os.ModeSetuid != 0 {
					want = os.ModeSetuid
				} else {
					want = os.ModeSetgid
				}
				if info.Mode()&want == 0 {
					t.Skip("filesystem did not retain special permission bit")
				}
			}
			if _, err := prepareCutoverBuildCohort(context.Background(), options); err == nil {
				t.Fatal("unsafe runtime mode was accepted")
			}
			if _, err := os.Lstat(options.RollbackDir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("runtime-mode rejection created rollback path: %v", err)
			}
		})
	}
}

func TestPrepareCutoverBuildCohortRejectsSidecarRootPermissionAndEntryDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, options cutoverArtifactOptions)
	}{
		{
			name: "sidecar",
			mutate: func(t *testing.T, options cutoverArtifactOptions) {
				if err := os.WriteFile(filepath.Join(options.BuildRoot, buildOutputDCIFilename+"-wal"), []byte("sidecar"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unexpected entry",
			mutate: func(t *testing.T, options cutoverArtifactOptions) {
				if err := os.WriteFile(filepath.Join(options.BuildRoot, "unexpected"), []byte("entry"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "root permission",
			mutate: func(t *testing.T, options cutoverArtifactOptions) {
				if runtime.GOOS != "windows" {
					if err := os.Chmod(options.BuildRoot, 0o755); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newBuildOutputsPreparedFixture(t)
			receipt, err := Build(context.Background(), buildOptionsFromPrepared(fixture))
			if err != nil {
				t.Fatal(err)
			}
			options, _ := newCutoverBuildCohortFixture(t, fixture.paths.buildDir, receipt)
			tt.mutate(t, options)
			if _, err := prepareCutoverBuildCohort(context.Background(), options); err == nil {
				t.Fatal("prepareCutoverBuildCohort() unexpectedly accepted root drift")
			}
		})
	}
}

func TestPrepareCutoverBuildCohortRejectsMissingSymlinkAndHardlinkAliases(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink and hardlink path contracts are exercised on Unix")
	}
	tests := []struct {
		name   string
		mutate func(t *testing.T, options *cutoverArtifactOptions, paths cutoverRuntimeTestPaths)
	}{
		{
			name: "missing output",
			mutate: func(t *testing.T, options *cutoverArtifactOptions, _ cutoverRuntimeTestPaths) {
				if err := os.Remove(filepath.Join(options.BuildRoot, buildOutputDCIFilename)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink output",
			mutate: func(t *testing.T, options *cutoverArtifactOptions, _ cutoverRuntimeTestPaths) {
				path := filepath.Join(options.BuildRoot, buildOutputDCIFilename)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(options.BuildRoot, buildOutputEventStoreFilename), path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "hardlink runtimes",
			mutate: func(t *testing.T, options *cutoverArtifactOptions, paths cutoverRuntimeTestPaths) {
				if err := os.Remove(paths.staged); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(paths.installed, paths.staged); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prepared := newBuildOutputsPreparedFixture(t)
			buildReceipt, err := Build(context.Background(), buildOptionsFromPrepared(prepared))
			if err != nil {
				t.Fatal(err)
			}
			options, paths := newCutoverBuildCohortFixture(t, prepared.paths.buildDir, buildReceipt)
			tt.mutate(t, &options, paths)
			if _, err := prepareCutoverBuildCohort(context.Background(), options); err == nil {
				t.Fatal("prepareCutoverBuildCohort() unexpectedly accepted unsafe alias")
			}
		})
	}
}

func TestPrepareCutoverBuildCohortRejectsSymlinkedRootsAndParents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink path contracts are exercised on Unix")
	}
	tests := []struct {
		name   string
		mutate func(t *testing.T, options *cutoverArtifactOptions)
	}{
		{
			name: "build root final symlink",
			mutate: func(t *testing.T, options *cutoverArtifactOptions) {
				link := filepath.Join(t.TempDir(), "build-link")
				if err := os.Symlink(options.BuildRoot, link); err != nil {
					t.Fatal(err)
				}
				options.BuildRoot = link
			},
		},
		{
			name: "build root symlinked parent",
			mutate: func(t *testing.T, options *cutoverArtifactOptions) {
				link := filepath.Join(t.TempDir(), "parent-link")
				if err := os.Symlink(filepath.Dir(options.BuildRoot), link); err != nil {
					t.Fatal(err)
				}
				options.BuildRoot = filepath.Join(link, filepath.Base(options.BuildRoot))
			},
		},
		{
			name: "runtime final symlink",
			mutate: func(t *testing.T, options *cutoverArtifactOptions) {
				link := filepath.Join(t.TempDir(), "runtime-link")
				if err := os.Symlink(options.InstalledRuntime, link); err != nil {
					t.Fatal(err)
				}
				options.InstalledRuntime = link
			},
		},
		{
			name: "runtime symlinked parent",
			mutate: func(t *testing.T, options *cutoverArtifactOptions) {
				link := filepath.Join(t.TempDir(), "runtime-parent-link")
				if err := os.Symlink(filepath.Dir(options.InstalledRuntime), link); err != nil {
					t.Fatal(err)
				}
				options.InstalledRuntime = filepath.Join(link, filepath.Base(options.InstalledRuntime))
			},
		},
		{
			name: "rollback symlinked parent",
			mutate: func(t *testing.T, options *cutoverArtifactOptions) {
				link := filepath.Join(t.TempDir(), "rollback-parent-link")
				if err := os.Symlink(filepath.Dir(options.RollbackDir), link); err != nil {
					t.Fatal(err)
				}
				options.RollbackDir = filepath.Join(link, filepath.Base(options.RollbackDir))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prepared := newBuildOutputsPreparedFixture(t)
			receipt, err := Build(context.Background(), buildOptionsFromPrepared(prepared))
			if err != nil {
				t.Fatal(err)
			}
			options, _ := newCutoverBuildCohortFixture(t, prepared.paths.buildDir, receipt)
			tt.mutate(t, &options)
			if _, err := prepareCutoverBuildCohort(context.Background(), options); err == nil {
				t.Fatal("prepareCutoverBuildCohort() unexpectedly accepted symlinked path")
			}
		})
	}
}

func TestPrepareCutoverBuildCohortRejectsNonFreshProspectivePathsAndAliases(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*cutoverArtifactOptions)
	}{
		{name: "existing rollback", mutate: func(value *cutoverArtifactOptions) { _ = os.Mkdir(value.RollbackDir, 0o700) }},
		{name: "existing receipt", mutate: func(value *cutoverArtifactOptions) { _ = os.WriteFile(value.CutoverReceipt, []byte("occupied"), 0o600) }},
		{name: "prospective alias", mutate: func(value *cutoverArtifactOptions) { value.CutoverReceipt = value.RollbackDir }},
		{name: "rollback inside build root", mutate: func(value *cutoverArtifactOptions) { value.RollbackDir = filepath.Join(value.BuildRoot, "rollback") }},
		{name: "receipt inside build root", mutate: func(value *cutoverArtifactOptions) {
			value.CutoverReceipt = filepath.Join(value.BuildRoot, "cutover.json")
		}},
		{name: "runtime alias", mutate: func(value *cutoverArtifactOptions) { value.StagedRuntime = value.InstalledRuntime }},
		{name: "build receipt outside root", mutate: func(value *cutoverArtifactOptions) { value.BuildReceipt = value.InstalledRuntime }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prepared := newBuildOutputsPreparedFixture(t)
			buildReceipt, err := Build(context.Background(), buildOptionsFromPrepared(prepared))
			if err != nil {
				t.Fatal(err)
			}
			options, _ := newCutoverBuildCohortFixture(t, prepared.paths.buildDir, buildReceipt)
			tt.mutate(&options)
			if _, err := prepareCutoverBuildCohort(context.Background(), options); err == nil {
				t.Fatal("prepareCutoverBuildCohort() unexpectedly accepted non-fresh path")
			}
		})
	}
}

func TestValidateCutoverProspectiveAliasesRejectsNestedBuildRootDescendants(t *testing.T) {
	root := filepath.Join(t.TempDir(), "build")
	base := cutoverArtifactPaths{
		buildRoot: root, buildReceipt: filepath.Join(root, BuildReceiptFilename),
		installedRuntime: filepath.Join(t.TempDir(), "installed"), stagedRuntime: filepath.Join(t.TempDir(), "staged"),
		rollbackDir: filepath.Join(t.TempDir(), "rollback"), cutoverReceipt: filepath.Join(t.TempDir(), "cutover.json"),
	}
	for _, descendant := range []string{
		filepath.Join(root, "rollback"),
		filepath.Join(root, "nested", "rollback"),
		filepath.Join(root, "nested", "deeper", "cutover.json"),
	} {
		paths := base
		paths.rollbackDir = descendant
		if err := validateCutoverProspectiveAliases(paths); err == nil {
			t.Fatalf("prospective descendant %q was accepted", descendant)
		}
	}
}

func TestPrepareCutoverActiveCohortBindsReadySourcesWithoutMutation(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	beforeActive := cutoverActiveTestHashes(t, fixture.paths)
	beforeBuild := cutoverBoundTestHashes(t, fixture.build.files)

	bundle, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
	if err != nil {
		t.Fatalf("prepareCutoverActiveCohort() error = %v", err)
	}
	if len(bundle.files) != 5 || len(bundle.build.files) != 7 {
		t.Fatalf("prepared active bundle bindings = %d/%d, want 5/7", len(bundle.files), len(bundle.build.files))
	}
	receipt := fixture.build.buildReceipt
	if bundle.sources.dciHashes.DatabaseLogical != receipt.SourceDatabaseLogicalSHA256["source_dci"] ||
		bundle.sources.eventHashes.NonDCI != receipt.SourceNonDCILogicalSHA256["source_event_store"] ||
		bundle.sources.currentHashes.Schema != receipt.SourceSchemaSHA256["source_l1"] ||
		bundle.sources.archiveHashes.Classification != receipt.SourceDCIClassificationSHA256["source_archive"] ||
		bundle.sources.jsonHash != receipt.SourceFileSHA256["source_dci_jsonl"] {
		t.Fatalf("active source measurements do not bind the BuildReceipt: %#v", bundle.sources)
	}
	assertCutoverActiveSourcesShape(t, reflect.TypeOf(bundle.sources))
	if after := cutoverActiveTestHashes(t, fixture.paths); !mapsEqual(beforeActive, after) {
		t.Fatalf("active sources changed: before=%#v after=%#v", beforeActive, after)
	}
	if after := cutoverBoundTestHashes(t, fixture.build.files); !mapsEqual(beforeBuild, after) {
		t.Fatalf("prepared build cohort changed: before=%#v after=%#v", beforeBuild, after)
	}
	for _, path := range []string{fixture.build.paths.rollbackDir, fixture.build.paths.cutoverReceipt} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("active preparation created prospective path %q: %v", path, err)
		}
	}
}

func TestPrepareCutoverActiveCohortRejectsSemanticDriftForEverySource(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	tests := []struct {
		name   string
		mutate func(t *testing.T, fixture *cutoverActiveCohortTestFixture)
	}{
		{name: "dci", mutate: func(t *testing.T, fixture *cutoverActiveCohortTestFixture) {
			mutateLogicalDB(t, fixture.paths.dci, `UPDATE dci_search_trace SET actor='tampered'`)
		}},
		{name: "jsonl", mutate: func(t *testing.T, fixture *cutoverActiveCohortTestFixture) {
			file, err := os.OpenFile(fixture.paths.dciJSONL, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.Write([]byte("\n")); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "event store", mutate: func(t *testing.T, fixture *cutoverActiveCohortTestFixture) {
			mutateLogicalDB(t, fixture.paths.eventStore, `PRAGMA user_version=1`)
		}},
		{name: "current L1", mutate: func(t *testing.T, fixture *cutoverActiveCohortTestFixture) {
			mutateLogicalDB(t, fixture.paths.l1, `UPDATE l1_staging_item SET raw_text='tampered'`)
		}},
		{name: "archive L1", mutate: func(t *testing.T, fixture *cutoverActiveCohortTestFixture) {
			mutateLogicalDB(t, fixture.paths.archive, `CREATE TABLE drift(value TEXT)`)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.mutate(t, &fixture)
			if _, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options); err == nil {
				t.Fatal("semantic drift was accepted")
			} else if containsCutoverActivePrivateValue(err, fixture) {
				t.Fatalf("active source error leaked private value: %v", err)
			}
			fixture.restore(t)
		})
	}
}

func TestPrepareCutoverActiveCohortAcceptsPhysicalSQLiteVariation(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	before := mustFileSHA256(t, fixture.paths.eventStore)
	db := openTestDB(t, fixture.paths.eventStore)
	mustExec(t, db, `PRAGMA page_size=8192`)
	mustExec(t, db, `PRAGMA auto_vacuum=1`)
	mustExec(t, db, `VACUUM`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	after := mustFileSHA256(t, fixture.paths.eventStore)
	if after == before {
		t.Fatal("physical SQLite variation did not change the file hash")
	}
	if _, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options); err != nil {
		t.Fatalf("physical SQLite variation was rejected: %v", err)
	}
}

func TestCompareCutoverActiveSQLiteHashesRejectsEachBoundField(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	receipt := fixture.build.buildReceipt
	base := sourceHashes{
		DatabaseLogical: receipt.SourceDatabaseLogicalSHA256["source_event_store"],
		Schema:          receipt.SourceSchemaSHA256["source_event_store"],
		Classification:  receipt.SourceDCIClassificationSHA256["source_event_store"],
		NonDCI:          receipt.SourceNonDCILogicalSHA256["source_event_store"],
	}
	tests := []struct {
		name   string
		mutate func(*sourceHashes)
	}{
		{name: "full", mutate: func(value *sourceHashes) { value.DatabaseLogical = strings.Repeat("0", 64) }},
		{name: "schema", mutate: func(value *sourceHashes) { value.Schema = strings.Repeat("0", 64) }},
		{name: "classification", mutate: func(value *sourceHashes) { value.Classification = strings.Repeat("0", 64) }},
		{name: "non dci", mutate: func(value *sourceHashes) { value.NonDCI = strings.Repeat("0", 64) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := base
			tt.mutate(&value)
			if err := compareCutoverActiveSQLiteHashes(receipt, "source_event_store", value, true); err == nil {
				t.Fatal("mismatched active hash field was accepted")
			}
		})
	}
	dci := sourceHashes{
		DatabaseLogical: receipt.SourceDatabaseLogicalSHA256["source_dci"],
		Schema:          receipt.SourceSchemaSHA256["source_dci"],
		Classification:  receipt.SourceDCIClassificationSHA256["source_dci"],
		NonDCI:          strings.Repeat("a", 64),
	}
	if err := compareCutoverActiveSQLiteHashes(receipt, "source_dci", dci, false); err == nil {
		t.Fatal("unexpected DCI NonDCI hash was accepted")
	}
}

func TestCompareCutoverActiveJSONLHashesRejectsClassificationAndFile(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	receipt := fixture.build.buildReceipt
	base := sourceHashes{
		Classification: receipt.SourceDCIClassificationSHA256["source_dci_jsonl"],
		File:           receipt.SourceFileSHA256["source_dci_jsonl"],
	}
	for _, tt := range []struct {
		name   string
		mutate func(*sourceHashes)
	}{
		{name: "classification", mutate: func(value *sourceHashes) { value.Classification = strings.Repeat("0", 64) }},
		{name: "file", mutate: func(value *sourceHashes) { value.File = strings.Repeat("0", 64) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			value := base
			tt.mutate(&value)
			if err := compareCutoverActiveJSONLHashes(receipt, value); err == nil {
				t.Fatal("mismatched JSONL hash field was accepted")
			}
		})
	}
}

func TestCompareCutoverActiveSourceCountsRejectsRoleDrift(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	bundle, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*cutoverActiveSources)
	}{
		{name: "DCI", mutate: func(value *cutoverActiveSources) { value.dciCounts.DCISteps++ }},
		{name: "JSONL traces", mutate: func(value *cutoverActiveSources) { value.jsonRecords++ }},
		{name: "JSONL steps", mutate: func(value *cutoverActiveSources) { value.jsonSteps++ }},
		{name: "Event Store", mutate: func(value *cutoverActiveSources) { value.eventCounts.EventStore++ }},
		{name: "current L1", mutate: func(value *cutoverActiveSources) { value.currentCounts.CurrentStaging++ }},
		{name: "current L1 DCI", mutate: func(value *cutoverActiveSources) { value.currentCounts.CurrentDCIStaging++ }},
		{name: "current L1 registry", mutate: func(value *cutoverActiveSources) { value.currentCounts.CurrentRegistry++ }},
		{name: "archive L1", mutate: func(value *cutoverActiveSources) { value.archiveCounts.ArchiveStaging++ }},
		{name: "archive L1 DCI", mutate: func(value *cutoverActiveSources) { value.archiveCounts.ArchiveDCIStaging++ }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sources := bundle.sources
			tt.mutate(&sources)
			if err := compareCutoverActiveSourceCounts(fixture.build.buildReceipt.SourceCounts, sources.dciCounts, sources.jsonRecords, sources.jsonSteps, sources.eventCounts, sources.currentCounts, sources.archiveCounts); err == nil {
				t.Fatal("active count drift was accepted")
			}
		})
	}
}

func assertCutoverActiveSourcesShape(t *testing.T, typ reflect.Type) {
	t.Helper()
	expected := map[string]reflect.Type{
		"dciCounts": reflect.TypeOf(SourceCounts{}), "dciHashes": reflect.TypeOf(sourceHashes{}),
		"jsonRecords": reflect.TypeOf(0), "jsonSteps": reflect.TypeOf(0), "jsonHash": reflect.TypeOf(""),
		"eventCounts": reflect.TypeOf(SourceCounts{}), "eventHashes": reflect.TypeOf(sourceHashes{}),
		"currentCounts": reflect.TypeOf(SourceCounts{}), "currentHashes": reflect.TypeOf(sourceHashes{}),
		"archiveCounts": reflect.TypeOf(SourceCounts{}), "archiveHashes": reflect.TypeOf(sourceHashes{}),
	}
	if typ.NumField() != len(expected) {
		t.Fatalf("active source measurement fields = %d, want %d", typ.NumField(), len(expected))
	}
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		want, ok := expected[field.Name]
		if !ok || field.Type != want {
			t.Fatalf("unexpected active source measurement field %q (%v)", field.Name, field.Type)
		}
	}
}

func TestPrepareCutoverActiveCohortRejectsSidecarAndAliases(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, fixture *cutoverActiveCohortTestFixture)
	}{
		{name: "SQLite sidecar", mutate: func(t *testing.T, fixture *cutoverActiveCohortTestFixture) {
			if err := os.WriteFile(fixture.paths.eventStore+"-wal", []byte("sidecar"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hardlink active sources", mutate: func(t *testing.T, fixture *cutoverActiveCohortTestFixture) {
			if err := os.Remove(fixture.paths.archive); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(fixture.paths.l1, fixture.paths.archive); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "build output alias", mutate: func(t *testing.T, fixture *cutoverActiveCohortTestFixture) {
			fixture.options.SourceDCI = filepath.Join(fixture.build.paths.buildRoot, buildOutputDCIFilename)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newCutoverActiveCohortTestFixture(t)
			tt.mutate(t, &fixture)
			if _, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options); err == nil {
				t.Fatal("unsafe active source was accepted")
			}
		})
	}
}

func TestPrepareCutoverActiveCohortRejectsSymlinkedAndCanceledInputs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink path contracts are exercised on Unix")
	}
	fixture := newCutoverActiveCohortTestFixture(t)
	link := filepath.Join(t.TempDir(), "active-dci-link")
	if err := os.Symlink(fixture.paths.dci, link); err != nil {
		t.Fatal(err)
	}
	unsafe := fixture.options
	unsafe.SourceDCI = link
	if _, err := prepareCutoverActiveCohort(context.Background(), fixture.build, unsafe); err == nil {
		t.Fatal("symlinked active source was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prepareCutoverActiveCohort(ctx, fixture.build, fixture.options); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled active preparation error = %v", err)
	}
}

func TestPrepareCutoverActiveCohortRejectsPostBindMutation(t *testing.T) {
	fixture := newCutoverActiveCohortTestFixture(t)
	original := cutoverPrepareActiveAfterBind
	cutoverPrepareActiveAfterBind = func() error {
		data := mustReadFile(t, fixture.paths.dciJSONL)
		return os.WriteFile(fixture.paths.dciJSONL, append(data, '\n'), 0o600)
	}
	t.Cleanup(func() {
		cutoverPrepareActiveAfterBind = original
	})
	if _, err := prepareCutoverActiveCohort(context.Background(), fixture.build, fixture.options); err == nil {
		t.Fatal("post-bind active source mutation was accepted")
	} else if errorCode(err, "unknown") != "source_changed" {
		t.Fatalf("post-bind active source error code = %q, want source_changed", errorCode(err, "unknown"))
	}
}

func TestPrepareCutoverBuildCohortRejectsCanceledContextAndPostBindMutation(t *testing.T) {
	prepared := newBuildOutputsPreparedFixture(t)
	buildReceipt, err := Build(context.Background(), buildOptionsFromPrepared(prepared))
	if err != nil {
		t.Fatal(err)
	}
	options, runtimePaths := newCutoverBuildCohortFixture(t, prepared.paths.buildDir, buildReceipt)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prepareCutoverBuildCohort(ctx, options); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preparation error = %v", err)
	}
	if _, err := os.Lstat(options.RollbackDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled preparation created rollback path: %v", err)
	}

	original := cutoverPrepareAfterBind
	cutoverPrepareAfterBind = func() error {
		data := mustReadFile(t, runtimePaths.installed)
		return os.WriteFile(runtimePaths.installed, append(data, 'x'), 0o600)
	}
	t.Cleanup(func() { cutoverPrepareAfterBind = original })
	if _, err := prepareCutoverBuildCohort(context.Background(), options); err == nil {
		t.Fatal("post-bind mutation was accepted")
	} else if errorCode(err, "unknown") != "source_changed" {
		t.Fatalf("post-bind mutation error code = %q, want source_changed", errorCode(err, "unknown"))
	}
}

type cutoverActiveCohortTestFiles struct {
	dci        string
	dciJSONL   string
	eventStore string
	l1         string
	archive    string
}

type cutoverActiveCohortTestFixture struct {
	build     preparedCutoverArtifacts
	options   cutoverActiveOptions
	paths     cutoverActiveCohortTestFiles
	originals map[string]string
}

func newCutoverActiveCohortTestFixture(t *testing.T) cutoverActiveCohortTestFixture {
	t.Helper()
	prepared := newBuildOutputsPreparedFixture(t)
	buildReceipt, err := Build(context.Background(), buildOptionsFromPrepared(prepared))
	if err != nil {
		t.Fatal(err)
	}
	buildOptions, _ := newCutoverBuildCohortFixture(t, prepared.paths.buildDir, buildReceipt)
	build, err := prepareCutoverBuildCohort(context.Background(), buildOptions)
	if err != nil {
		t.Fatal(err)
	}
	activeRoot := t.TempDir()
	paths := cutoverActiveCohortTestFiles{
		dci: filepath.Join(activeRoot, "source-dci"), dciJSONL: filepath.Join(activeRoot, "source-dci-jsonl"),
		eventStore: filepath.Join(activeRoot, "source-event-store"), l1: filepath.Join(activeRoot, "source-l1"),
		archive: filepath.Join(activeRoot, "source-archive"),
	}
	// Use the resolved captured paths as the copy source.  The active files are
	// separate inodes so the alias checks exercise the real cutover boundary.
	originals := map[string]string{
		"dci": prepared.paths.sources.dci, "jsonl": prepared.paths.sources.dciJSONL,
		"event": prepared.paths.sources.eventStore, "l1": prepared.paths.sources.l1, "archive": prepared.paths.sources.archive,
	}
	for _, item := range []struct{ source, target string }{
		{originals["dci"], paths.dci}, {originals["jsonl"], paths.dciJSONL}, {originals["event"], paths.eventStore},
		{originals["l1"], paths.l1}, {originals["archive"], paths.archive},
	} {
		copyFile(t, item.source, item.target)
	}
	return cutoverActiveCohortTestFixture{
		build:   build,
		options: cutoverActiveOptions{SourceDCI: paths.dci, SourceDCIJSONL: paths.dciJSONL, SourceEventStore: paths.eventStore, SourceL1: paths.l1, SourceArchive: paths.archive},
		paths:   paths, originals: originals,
	}
}

func (fixture cutoverActiveCohortTestFixture) restore(t *testing.T) {
	t.Helper()
	for _, item := range []struct{ source, target string }{
		{fixture.originals["dci"], fixture.paths.dci}, {fixture.originals["jsonl"], fixture.paths.dciJSONL},
		{fixture.originals["event"], fixture.paths.eventStore}, {fixture.originals["l1"], fixture.paths.l1},
		{fixture.originals["archive"], fixture.paths.archive},
	} {
		copyFile(t, item.source, item.target)
	}
}

func cutoverActiveTestHashes(t *testing.T, paths cutoverActiveCohortTestFiles) map[string]string {
	t.Helper()
	hashes := make(map[string]string, 5)
	for role, path := range map[string]string{
		"dci": paths.dci, "jsonl": paths.dciJSONL, "event": paths.eventStore, "l1": paths.l1, "archive": paths.archive,
	} {
		hashes[role] = mustFileSHA256(t, path)
	}
	return hashes
}

func cutoverBoundTestHashes(t *testing.T, files []cutoverBoundFile) map[string]string {
	t.Helper()
	hashes := make(map[string]string, len(files))
	for _, file := range files {
		hashes[file.path] = mustFileSHA256(t, file.path)
	}
	return hashes
}

func containsCutoverActivePrivateValue(err error, fixture cutoverActiveCohortTestFixture) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	for _, value := range []string{
		fixture.paths.dci, fixture.paths.dciJSONL, fixture.paths.eventStore, fixture.paths.l1, fixture.paths.archive,
		fixture.build.paths.buildRoot, fixture.build.paths.buildReceipt, "legacy-search-1", "legacy-evidence-1",
	} {
		if strings.Contains(message, value) {
			return true
		}
	}
	return false
}

type cutoverRuntimeTestPaths struct {
	installed string
	staged    string
}

func newCutoverBuildCohortFixture(t *testing.T, buildRoot string, receipt BuildReceipt) (cutoverArtifactOptions, cutoverRuntimeTestPaths) {
	t.Helper()
	receiptPath := filepath.Join(buildRoot, BuildReceiptFilename)
	receiptSHA256, err := fileSHA256(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoot := t.TempDir()
	installed := filepath.Join(runtimeRoot, "installed-runtime")
	staged := filepath.Join(runtimeRoot, "staged-runtime")
	if err := os.WriteFile(installed, []byte("old-runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new-runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	installedSHA256 := mustFileSHA256(t, installed)
	stagedSHA256 := mustFileSHA256(t, staged)
	prospectiveRoot := t.TempDir()
	return cutoverArtifactOptions{
		BuildRoot: buildRoot, BuildReceipt: receiptPath, ExpectedBuildReceiptSHA256: receiptSHA256,
		InstalledRuntime: installed, StagedRuntime: staged,
		ExpectedInstalledRuntimeSHA256: installedSHA256, ExpectedStagedRuntimeSHA256: stagedSHA256,
		RollbackDir: filepath.Join(prospectiveRoot, "rollback"), CutoverReceipt: filepath.Join(prospectiveRoot, "cutover.json"),
	}, cutoverRuntimeTestPaths{installed: installed, staged: staged}
}

func cutoverBuildCohortHashes(t *testing.T, options cutoverArtifactOptions, runtimePaths cutoverRuntimeTestPaths) map[string]string {
	t.Helper()
	paths := []string{
		options.BuildReceipt,
		filepath.Join(options.BuildRoot, buildOutputDCIFilename), filepath.Join(options.BuildRoot, buildOutputEventStoreFilename),
		filepath.Join(options.BuildRoot, buildOutputL1Filename), filepath.Join(options.BuildRoot, buildOutputArchiveFilename),
		runtimePaths.installed, runtimePaths.staged,
	}
	hashes := make(map[string]string, len(paths))
	for _, path := range paths {
		hashes[path] = mustFileSHA256(t, path)
	}
	return hashes
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustFileSHA256(t *testing.T, path string) string {
	t.Helper()
	hash, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func writeCutoverTestReceipt(t *testing.T, path string, receipt BuildReceipt) {
	t.Helper()
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func containsCutoverPrivateValue(err error, options cutoverArtifactOptions) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	for _, value := range []string{options.BuildRoot, options.BuildReceipt, options.InstalledRuntime, options.StagedRuntime, options.RollbackDir, options.CutoverReceipt, "legacy-search-1", "legacy-evidence-1"} {
		if strings.Contains(message, value) {
			return true
		}
	}
	return false
}
