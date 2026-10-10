package vision

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager/taskmanagertest"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/transportmanager"
	domainvision "github.com/Nyukimin/RenCrow_CORE/internal/domain/vision"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	transportpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// A refused Vision admission must not leave a Run-less queued Task behind.
func TestActionAnalyzerLeavesNothingWhenExecutionCapacityIsUnavailable(t *testing.T) {
	saturated := taskmanagertest.NewSaturated(t)
	client, err := NewClient("http://vision.invalid", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	actionStore, err := actionpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	actions := actionmanager.New(actionStore)
	transportStore, err := transportpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "transport"))
	if err != nil {
		t.Fatal(err)
	}
	analyzer, err := NewActionAnalyzer(client, actions, saturated.Manager, transportmanager.New(transportStore, actions), "mio")
	if err != nil {
		t.Fatal(err)
	}

	_, err = analyzer.Analyze(context.Background(), domainvision.AnalyzeRequest{
		RequestID: modulecore.NewRequestID(), SessionID: "session", Kind: "image",
		Filename: "image.png", ContentType: "image/png", Data: []byte("image"),
	})
	if !errors.Is(err, taskmanager.ErrParallelLimit) {
		t.Fatalf("Analyze error=%v, want ErrParallelLimit", err)
	}
	saturated.AssertNothingPersisted(t)
}
