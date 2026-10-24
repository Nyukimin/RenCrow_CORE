package main

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func runtimeCapabilityPromptDiscoveryStub(calls *atomic.Int32) runtimeCapabilityPromptDiscovery {
	return func(context.Context) ([]domaintool.ToolMetadata, []runtimeMCPObservation, string) {
		calls.Add(1)
		return []domaintool.ToolMetadata{{ToolID: "data_capability.describe", Description: "data catalog"}}, nil, ""
	}
}

func TestRuntimeCapabilityPromptReusesDiscoverySnapshot(t *testing.T) {
	var discoveryCalls atomic.Int32
	metadata := []domaintool.ToolMetadata{{ToolID: "data_capability.describe", Description: "data catalog"}}
	snapshot := newRuntimeCapabilityPromptSnapshotCache(func(context.Context) ([]domaintool.ToolMetadata, []runtimeMCPObservation, string) {
		discoveryCalls.Add(1)
		return metadata, nil, ""
	})
	snapshot.refresh(context.Background())
	metadata[0].ToolID = "mutated.tool"
	if got := snapshot.render(nil, nil, nil); !strings.Contains(got, "data_capability.describe") {
		t.Fatalf("cached snapshot was mutated with discovery-owned metadata:\n%s", got)
	}
	provider := runtimeAgentContextProvider(nil, func(context.Context) string {
		return snapshot.render(nil, nil, nil)
	})

	for range 3 {
		if got := provider(context.Background(), "mio"); !strings.Contains(got, "tools: available=1 unavailable=0") {
			t.Fatalf("Mio prompt is missing the cached capability summary:\n%s", got)
		}
	}
	if got := discoveryCalls.Load(); got != 1 {
		t.Fatalf("discovery calls across repeated prompts = %d, want 1 cached discovery", got)
	}
}

func TestRuntimeCapabilityPromptSnapshotStartsDiscoveryAsynchronously(t *testing.T) {
	var discoveryCalls atomic.Int32
	snapshot := newRuntimeCapabilityPromptSnapshotCache(runtimeCapabilityPromptDiscoveryStub(&discoveryCalls))
	ctx, cancel := context.WithCancel(context.Background())
	done := snapshot.start(ctx)

	deadline := time.After(time.Second)
	for {
		if discoveryCalls.Load() == 1 && strings.Contains(snapshot.render(nil, nil, nil), "data_capability.describe") {
			break
		}
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatal("startup discovery did not publish a snapshot")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("snapshot refresher did not stop after cancellation")
	}
}

func TestRuntimeCapabilityPromptDiscoveryFailurePublishesUnavailableSnapshot(t *testing.T) {
	var unavailable atomic.Bool
	snapshot := newRuntimeCapabilityPromptSnapshotCache(func(context.Context) ([]domaintool.ToolMetadata, []runtimeMCPObservation, string) {
		if unavailable.Load() {
			return nil, nil, "Worker Tool一覧を取得できません"
		}
		return []domaintool.ToolMetadata{{ToolID: "data_capability.describe"}}, nil, ""
	})
	snapshot.refresh(context.Background())
	if got := snapshot.render(nil, nil, nil); !strings.Contains(got, "利用可能: data_capability.describe") {
		t.Fatalf("initial good snapshot missing capability:\n%s", got)
	}

	unavailable.Store(true)
	snapshot.refresh(context.Background())
	got := snapshot.render(nil, nil, nil)
	if !strings.Contains(got, "利用不可: worker_toolrunner") || strings.Contains(got, "利用可能: data_capability.describe") {
		t.Fatalf("discovery failure must replace stale capabilities with unavailable status:\n%s", got)
	}
}

func TestRuntimeCapabilityPromptReturnsWhileDiscoveryIsBlocked(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var discoveryCalls atomic.Int32
	snapshot := newRuntimeCapabilityPromptSnapshotCache(func(context.Context) ([]domaintool.ToolMetadata, []runtimeMCPObservation, string) {
		discoveryCalls.Add(1)
		close(started)
		<-release
		return []domaintool.ToolMetadata{{ToolID: "late.tool"}}, nil, ""
	})

	refreshDone := make(chan struct{})
	go func() {
		snapshot.refresh(context.Background())
		close(refreshDone)
	}()
	<-started

	provider := runtimeAgentContextProvider(nil, func(context.Context) string {
		return snapshot.render(nil, nil, nil)
	})
	promptDone := make(chan string, 1)
	go func() { promptDone <- provider(context.Background(), "mio") }()
	select {
	case got := <-promptDone:
		if !strings.Contains(got, "tools: available=0 unavailable=1") || len(got) > 512 {
			t.Fatalf("blocked discovery should return a bounded unavailable Mio snapshot:\n%s", got)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("prompt snapshot blocked on capability discovery")
	}
	if got := discoveryCalls.Load(); got != 1 {
		t.Fatalf("discovery calls while rendering prompt = %d, want 1 background discovery", got)
	}

	close(release)
	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("blocked discovery did not finish after release")
	}
	if got := snapshot.render(nil, nil, nil); !strings.Contains(got, "利用可能: late.tool") {
		t.Fatalf("completed discovery was not published:\n%s", got)
	}
}

func TestRuntimeCapabilityPromptSnapshotConcurrentRefreshAndReads(t *testing.T) {
	const refreshes = 8
	var discoveryCalls atomic.Int32
	var activeDiscoveries atomic.Int32
	var maxActiveDiscoveries atomic.Int32
	snapshot := newRuntimeCapabilityPromptSnapshotCache(func(context.Context) ([]domaintool.ToolMetadata, []runtimeMCPObservation, string) {
		active := activeDiscoveries.Add(1)
		for current := maxActiveDiscoveries.Load(); active > current; current = maxActiveDiscoveries.Load() {
			if maxActiveDiscoveries.CompareAndSwap(current, active) {
				break
			}
		}
		discoveryCalls.Add(1)
		time.Sleep(time.Millisecond)
		activeDiscoveries.Add(-1)
		return []domaintool.ToolMetadata{{ToolID: "refresh.tool"}}, nil, ""
	})

	var readers sync.WaitGroup
	readers.Add(4)
	stopReaders := make(chan struct{})
	for range 4 {
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stopReaders:
					return
				default:
					_ = snapshot.render(nil, nil, nil)
				}
			}
		}()
	}

	var refreshers sync.WaitGroup
	refreshers.Add(refreshes)
	for range refreshes {
		go func() {
			defer refreshers.Done()
			snapshot.refresh(context.Background())
		}()
	}
	refreshers.Wait()
	close(stopReaders)
	readers.Wait()

	if got := discoveryCalls.Load(); got != refreshes {
		t.Fatalf("concurrent refresh discovery calls = %d, want %d", got, refreshes)
	}
	if got := maxActiveDiscoveries.Load(); got != 1 {
		t.Fatalf("overlapping discovery calls = %d, want one serialized refresh", got)
	}
	if got := snapshot.render(nil, nil, nil); !strings.Contains(got, "refresh.") {
		t.Fatalf("latest concurrent refresh was not published:\n%s", got)
	}
}
