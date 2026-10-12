package main

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/storagehost"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
)

// task_store.index.enabled reaches the store that owns the Task log: the
// runtime's own store in the local profile and the storage host's store in the
// remote profile. It is off unless configured.

type indexStatsReporter interface {
	IndexStats() (taskpersistence.IndexStats, bool)
}

func TestRuntimeTaskOwnerOpensTheLocalStoreWithTheConfiguredIndex(t *testing.T) {
	for _, tc := range []struct {
		name  string
		index bool
	}{
		{"default off", false},
		{"enabled", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := &Dependencies{taskStoreIndex: tc.index}
			if err := initializeRuntimeTaskOwner(deps, t.TempDir()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = deps.taskManager.Close() })
			reporter, ok := deps.taskStore.(indexStatsReporter)
			if !ok {
				t.Fatalf("local Task store %T cannot report its index", deps.taskStore)
			}
			if _, indexed := reporter.IndexStats(); indexed != tc.index {
				t.Fatalf("local store indexed = %v, want %v", indexed, tc.index)
			}
			if deps.taskManager.IDIndexed() != tc.index {
				t.Fatalf("Task owner declares an ID index = %v, want %v (it must match the store it sits on)", deps.taskManager.IDIndexed(), tc.index)
			}
		})
	}
}

// A remote Task store lives in the storage host, whose configuration the
// runtime cannot see, so the runtime never declares an ID index for it.
func TestRuntimeTaskOwnerNeverDeclaresAnIDIndexForARemoteStore(t *testing.T) {
	client, err := storagehost.NewClient(storagehost.ClientConfig{Endpoint: "http://127.0.0.1:18820", Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	remoteStore, err := storagehost.NewTaskStoreClient(client)
	if err != nil {
		t.Fatal(err)
	}
	deps := &Dependencies{taskStoreIndex: true}
	if err := initializeRuntimeTaskOwner(deps, t.TempDir(), remoteStore); err != nil {
		t.Fatal(err)
	}
	if deps.taskStore != remoteStore || deps.taskManager.IDIndexed() {
		t.Fatalf("remote Task owner: store=%T idIndexed=%v, want the injected remote store and no declared index", deps.taskStore, deps.taskManager.IDIndexed())
	}
}

// task_store.index.persist keeps the index in a sidecar file (index/seg-legacy.tsi)
// that the writer replaces when it closes, so the next start restores it. It
// needs the index and is off unless configured.
func TestRuntimeTaskOwnerPersistsTheIndexSidecarOnlyWhenConfigured(t *testing.T) {
	for _, tc := range []struct {
		name    string
		persist bool
	}{
		{"index only", false},
		{"index with sidecar", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			deps := &Dependencies{taskStoreIndex: true, taskStorePersist: tc.persist}
			if err := initializeRuntimeTaskOwner(deps, workspace); err != nil {
				t.Fatal(err)
			}
			if err := deps.taskManager.Close(); err != nil {
				t.Fatal(err)
			}
			sidecar := filepath.Join(defaultTaskStorePath(workspace), "index", "seg-legacy.tsi")
			_, err := os.Stat(sidecar)
			if tc.persist && err != nil {
				t.Fatalf("persist=true left no sidecar after the writer closed: %v", err)
			}
			if !tc.persist && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("persist=false wrote a sidecar (stat err=%v)", err)
			}
		})
	}
}

// The sidecar is a copy of the index, so there is nothing to persist without
// it. Configuration rejects the combination; if a caller builds it anyway the
// runtime fails closed instead of silently running without what was asked for.
func TestRuntimeTaskOwnerRefusesPersistWithoutTheIndex(t *testing.T) {
	deps := &Dependencies{taskStoreIndex: false, taskStorePersist: true}
	if err := initializeRuntimeTaskOwner(deps, t.TempDir()); err == nil {
		t.Fatal("a Task store with persist but no index was opened")
	}
	if deps.taskStore != nil || deps.taskManager != nil {
		t.Fatal("a refused Task owner initialization left a store or manager behind")
	}
}

func TestStorageHostServeOpensTheTaskStoreWithTheConfiguredIndex(t *testing.T) {
	for _, tc := range []struct {
		name    string
		index   bool
		persist bool
	}{
		{"default off", false, false},
		{"index", true, false},
		{"index with sidecar", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _ := newStorageHostTestConfig(t, nextFixedLoopbackAddress(t))
			cfg.TaskStore.Index.Enabled = tc.index
			cfg.TaskStore.Index.Persist = tc.persist
			deps := realStorageHostServeDeps()
			var openedPath string
			var openedOptions taskpersistence.OpenOptions
			stop := errors.New("stop after the Task store is requested")
			deps.OpenTaskStore = func(path string, options taskpersistence.OpenOptions) (storageHostTaskStore, error) {
				openedPath, openedOptions = path, options
				return nil, stop
			}
			deps.Listen = func(string, string) (net.Listener, error) {
				t.Error("the storage host started listening before its Task store opened")
				return nil, errors.New("unexpected listen")
			}
			if err := runStorageHostServe(context.Background(), cfg, deps); !errors.Is(err, stop) {
				t.Fatalf("runStorageHostServe error = %v, want the Task store open failure", err)
			}
			if openedOptions.Index != tc.index || openedOptions.Persist != tc.persist {
				t.Fatalf("Task store opened with Index=%v Persist=%v, want %v and %v", openedOptions.Index, openedOptions.Persist, tc.index, tc.persist)
			}
			if openedPath != defaultTaskStorePath(cfg.WorkspaceDir) || !filepath.IsAbs(openedPath) {
				t.Fatalf("Task store path = %q, want %q", openedPath, defaultTaskStorePath(cfg.WorkspaceDir))
			}
		})
	}
}

// task_store.rotation.enabled reaches the store that owns the Task log: with it
// the first open moves the store to layout v2 (LAYOUT, a first segment, segment 0
// frozen); without it nothing changes.
func TestRuntimeTaskOwnerMovesTheLocalStoreToSegmentsOnlyWhenConfigured(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rotation bool
	}{
		{"default off", false},
		{"enabled", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			root := defaultTaskStorePath(workspace)
			t.Cleanup(func() {
				_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
					if err == nil && !entry.IsDir() {
						_ = os.Chmod(path, 0o644)
					}
					return nil
				})
			})
			deps := &Dependencies{taskStoreIndex: true, taskStorePersist: true, taskStoreRotation: tc.rotation, taskStoreSizeValve: 2 << 20}
			if err := initializeRuntimeTaskOwner(deps, workspace); err != nil {
				t.Fatal(err)
			}
			if err := deps.taskManager.Close(); err != nil {
				t.Fatal(err)
			}
			_, layoutErr := os.Stat(filepath.Join(root, "LAYOUT"))
			if tc.rotation && layoutErr != nil {
				t.Fatalf("rotation=true left no LAYOUT: %v", layoutErr)
			}
			if !tc.rotation && !errors.Is(layoutErr, os.ErrNotExist) {
				t.Fatalf("rotation=false wrote LAYOUT (stat err=%v)", layoutErr)
			}
		})
	}
}

// Rotation needs the persisted index. Configuration rejects the combination; if a
// caller builds it anyway the runtime fails closed.
func TestRuntimeTaskOwnerRefusesRotationWithoutThePersistedIndex(t *testing.T) {
	for _, deps := range []*Dependencies{
		{taskStoreIndex: false, taskStorePersist: false, taskStoreRotation: true},
		{taskStoreIndex: true, taskStorePersist: false, taskStoreRotation: true},
	} {
		if err := initializeRuntimeTaskOwner(deps, t.TempDir()); err == nil {
			t.Fatalf("a Task store with rotation but no persisted index was opened (%+v)", deps)
		}
		if deps.taskStore != nil || deps.taskManager != nil {
			t.Fatal("a refused Task owner initialization left a store or manager behind")
		}
	}
}

func TestStorageHostServeOpensTheTaskStoreWithTheConfiguredRotation(t *testing.T) {
	cfg, _ := newStorageHostTestConfig(t, nextFixedLoopbackAddress(t))
	cfg.TaskStore.Index.Enabled, cfg.TaskStore.Index.Persist = true, true
	cfg.TaskStore.Rotation.Enabled, cfg.TaskStore.Rotation.SizeValveBytes = true, 4<<20
	deps := realStorageHostServeDeps()
	var openedOptions taskpersistence.OpenOptions
	stop := errors.New("stop after the Task store is requested")
	deps.OpenTaskStore = func(_ string, options taskpersistence.OpenOptions) (storageHostTaskStore, error) {
		openedOptions = options
		return nil, stop
	}
	deps.Listen = func(string, string) (net.Listener, error) {
		t.Error("the storage host started listening before its Task store opened")
		return nil, errors.New("unexpected listen")
	}
	if err := runStorageHostServe(context.Background(), cfg, deps); !errors.Is(err, stop) {
		t.Fatalf("runStorageHostServe error = %v, want the Task store open failure", err)
	}
	if !openedOptions.Rotation || openedOptions.RotationSizeValve != 4<<20 {
		t.Fatalf("Task store opened with Rotation=%v RotationSizeValve=%d, want true and %d", openedOptions.Rotation, openedOptions.RotationSizeValve, 4<<20)
	}
}
