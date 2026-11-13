package task

import (
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func validPrivateTextCheckpoint() PrivateTextCheckpoint {
	return PrivateTextCheckpoint{
		TaskID:      modulecore.NewTaskID(),
		RunID:       modulecore.NewRunID(),
		QueueItemID: modulecore.NewQueueItemID(),
		ActorID:     "mio",
		InputSHA256: strings.Repeat("a", 64),
		Output:      "a private draft",
	}
}

func TestPrivateTextCheckpointDigestIsStableAndCoversCanonicalFields(t *testing.T) {
	checkpoint := validPrivateTextCheckpoint()
	digest := checkpoint.Digest()
	if len(digest) != 64 {
		t.Fatalf("Digest() length = %d, want SHA-256 hex", len(digest))
	}
	if got := checkpoint.Digest(); got != digest {
		t.Fatalf("Digest() changed for the same checkpoint: %q then %q", digest, got)
	}

	mutations := []func(*PrivateTextCheckpoint){
		func(value *PrivateTextCheckpoint) { value.TaskID = modulecore.NewTaskID() },
		func(value *PrivateTextCheckpoint) { value.RunID = modulecore.NewRunID() },
		func(value *PrivateTextCheckpoint) { value.QueueItemID = modulecore.NewQueueItemID() },
		func(value *PrivateTextCheckpoint) { value.ActorID = "shiro" },
		func(value *PrivateTextCheckpoint) { value.InputSHA256 = strings.Repeat("b", 64) },
		func(value *PrivateTextCheckpoint) { value.Output = "changed private draft" },
	}
	for index, mutate := range mutations {
		changed := checkpoint
		mutate(&changed)
		if got := changed.Digest(); got == digest {
			t.Errorf("mutation %d did not change checkpoint digest", index)
		}
	}
}

func TestPrivateTextCheckpointValidateAllowsPreparedAndBoundsSavedOutput(t *testing.T) {
	checkpoint := validPrivateTextCheckpoint()
	if err := checkpoint.Validate(); err != nil {
		t.Fatalf("Validate(saved output): %v", err)
	}

	checkpoint.Output = ""
	if err := checkpoint.Validate(); err != nil {
		t.Fatalf("Validate(prepared checkpoint): %v", err)
	}

	checkpoint.Output = " \t\n"
	if err := checkpoint.Validate(); err == nil {
		t.Fatal("whitespace-only output was accepted")
	}
	checkpoint.Output = strings.Repeat("x", conversation.MaxReportResultBytes+1)
	if err := checkpoint.Validate(); err == nil {
		t.Fatal("output above the report result byte limit was accepted")
	}
	checkpoint.Output = string([]byte{0xff})
	if err := checkpoint.Validate(); err == nil {
		t.Fatal("invalid UTF-8 output was accepted")
	}
}

func TestPrivateTextCheckpointValidateRejectsInvalidOwnershipAndDigest(t *testing.T) {
	checkpoint := validPrivateTextCheckpoint()
	mutations := []struct {
		name   string
		change func(*PrivateTextCheckpoint)
	}{
		{name: "task_id", change: func(value *PrivateTextCheckpoint) { value.TaskID = "" }},
		{name: "run_id", change: func(value *PrivateTextCheckpoint) { value.RunID = "" }},
		{name: "queue_item_id", change: func(value *PrivateTextCheckpoint) { value.QueueItemID = "" }},
		{name: "actor_id", change: func(value *PrivateTextCheckpoint) { value.ActorID = " " }},
		{name: "input_sha256", change: func(value *PrivateTextCheckpoint) { value.InputSHA256 = "bad" }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			value := checkpoint
			test.change(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("invalid checkpoint was accepted")
			}
		})
	}
}
