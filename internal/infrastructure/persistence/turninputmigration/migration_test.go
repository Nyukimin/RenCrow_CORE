package turninputmigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	sessionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	_ "modernc.org/sqlite"
)

type migrationFixture struct {
	t            *testing.T
	root         string
	source       string
	eventDB      string
	conversation string
	output       string
	sessionID    string
	jobIDs       []string
	dryReceipt   string
}

func TestRunThreeRowMigrationReusesExactReceiptAndLoadsOwnerRepository(t *testing.T) {
	fixture := newMigrationFixture(t)
	sourceBefore := snapshotDirectory(t, fixture.source)
	eventBefore := readFile(t, fixture.eventDB)
	conversationBefore := readFile(t, fixture.conversation)

	dryReceipt := filepath.Join(fixture.root, "dry-receipt.json")
	dry, err := Run(context.Background(), Options{
		Mode: ModeDryRun, SourceDir: fixture.source, EventDBPath: fixture.eventDB,
		ConversationDBPath: fixture.conversation, ReceiptPath: dryReceipt,
	})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if dry.Status != "ready" || dry.Mode != ModeDryRun {
		t.Fatalf("dry receipt status/mode = %q/%q", dry.Status, dry.Mode)
	}
	if dry.SourceFiles != 2 || dry.NonSessionFiles != 1 || dry.LegacyHistoryRows != 3 || dry.OutputHistoryRows != 3 || dry.ReceiptLinkedRows != 1 || dry.DeterministicRows != 2 {
		t.Fatalf("dry counts = %#v", dry)
	}
	if dry.EventEvidenceSHA256 == "" || dry.ConversationEvidenceSHA256 == "" || dry.MappingSHA256 == "" || dry.OutputSHA256 == "" {
		t.Fatal("dry receipt hashes are incomplete")
	}
	receiptBytes := readFile(t, dryReceipt)
	for _, forbidden := range append([]string{fixture.source, fixture.eventDB, fixture.conversation}, fixture.jobIDs...) {
		if strings.Contains(string(receiptBytes), forbidden) {
			t.Fatalf("receipt contains forbidden path or legacy identifier %q", forbidden)
		}
	}
	if permission(t, dryReceipt) != 0600 {
		t.Fatalf("dry receipt permission = %o, want 600", permission(t, dryReceipt))
	}

	outputReceipt := filepath.Join(fixture.root, "apply-receipt.json")
	applied, err := Run(context.Background(), Options{
		Mode: ModeApply, SourceDir: fixture.source, EventDBPath: fixture.eventDB,
		ConversationDBPath: fixture.conversation, OutputDir: fixture.output,
		ReceiptPath: outputReceipt, DryRunReceipt: dryReceipt,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied.Status != "applied" || applied.Mode != ModeApply || applied.OutputSHA256 != dry.OutputSHA256 {
		t.Fatalf("applied receipt = %#v", applied)
	}
	if got := snapshotDirectory(t, fixture.source); !reflect.DeepEqual(got, sourceBefore) {
		t.Fatal("source directory changed")
	}
	if got := readFile(t, fixture.eventDB); !reflect.DeepEqual(got, eventBefore) {
		t.Fatal("event database changed")
	}
	if got := readFile(t, fixture.conversation); !reflect.DeepEqual(got, conversationBefore) {
		t.Fatal("conversation database changed")
	}

	repository := sessionpersistence.NewJSONSessionRepository(fixture.output)
	loaded, err := repository.Load(context.Background(), fixture.sessionID)
	if err != nil {
		t.Fatalf("load migrated Session: %v", err)
	}
	history := loaded.GetHistory()
	if len(history) != 3 || loaded.ID() != fixture.sessionID || loaded.ChannelAddress().ChannelType() != "line" || loaded.ChannelAddress().ExternalConversationID() != "U123" {
		t.Fatalf("loaded Session identity/history = %q/%d/%#v", loaded.ID(), len(history), loaded.ChannelAddress())
	}
	for index, input := range history {
		if input.MessageText() != []string{"hello", "second", "third"}[index] || input.ChannelAddress() != loaded.ChannelAddress() || input.SessionID() != loaded.ID() || input.Route() != "chat" {
			t.Fatalf("history[%d] metadata = %#v", index, input)
		}
		if err := input.Validate(); err != nil {
			t.Fatalf("history[%d] invalid: %v", index, err)
		}
		if input.UserMessageID() == input.AgentMessageID() {
			t.Fatalf("history[%d] user/agent IDs are equal", index)
		}
	}
	if history[0].RootTaskID() != modulecore.TaskID(fixture.receiptLinkedIdentity().rootTaskID) {
		t.Fatal("receipt-linked RootTaskID was not reused")
	}
	assertNoLegacyFields(t, filepath.Join(fixture.output, fixture.sessionID+".json"))
	if got := readFile(t, filepath.Join(fixture.output, "notes.bin")); string(got) != "non-session bytes\x00\xff" {
		t.Fatalf("non-session bytes changed: %q", got)
	}
	if permission(t, filepath.Join(fixture.output, "notes.bin")) != 0640 {
		t.Fatalf("non-session permission = %o, want 640", permission(t, filepath.Join(fixture.output, "notes.bin")))
	}
}

func TestDeterministicMappingIsReproducibleAndMessageIDsAreDistinct(t *testing.T) {
	first, err := deterministicMapping("job-repro")
	if err != nil {
		t.Fatal(err)
	}
	second, err := deterministicMapping("job-repro")
	if err != nil {
		t.Fatal(err)
	}
	if first.identity != second.identity || first.identity.userMessageID == first.identity.agentMessageID {
		t.Fatalf("deterministic mapping = %#v / %#v", first.identity, second.identity)
	}
	user, err := modulecore.NewMigrationID(modulecore.CanonicalMessageID, "session_history", "user_message", "job-repro")
	if err != nil || string(first.identity.userMessageID) != user {
		t.Fatalf("user message namespace mismatch: %q/%q", first.identity.userMessageID, user)
	}
	agent, err := modulecore.NewMigrationID(modulecore.CanonicalMessageID, "session_history", "agent_message", "job-repro")
	if err != nil || string(first.identity.agentMessageID) != agent {
		t.Fatalf("agent message namespace mismatch: %q/%q", first.identity.agentMessageID, agent)
	}
}

func TestCanonicalSourceIsAByteStableNoOp(t *testing.T) {
	fixture := newMigrationFixture(t)
	firstDry := filepath.Join(fixture.root, "first-dry.json")
	if _, err := Run(context.Background(), Options{
		Mode: ModeDryRun, SourceDir: fixture.source, EventDBPath: fixture.eventDB,
		ConversationDBPath: fixture.conversation, ReceiptPath: firstDry,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), Options{
		Mode: ModeApply, SourceDir: fixture.source, EventDBPath: fixture.eventDB,
		ConversationDBPath: fixture.conversation, OutputDir: fixture.output,
		ReceiptPath: filepath.Join(fixture.root, "first-apply.json"), DryRunReceipt: firstDry,
	}); err != nil {
		t.Fatal(err)
	}
	canonicalBefore := snapshotDirectory(t, fixture.output)

	secondOutput := filepath.Join(fixture.root, "second-output")
	if err := os.Mkdir(secondOutput, 0700); err != nil {
		t.Fatal(err)
	}
	secondDry := filepath.Join(fixture.root, "second-dry.json")
	dry, err := Run(context.Background(), Options{
		Mode: ModeDryRun, SourceDir: fixture.output, EventDBPath: fixture.eventDB,
		ConversationDBPath: fixture.conversation, ReceiptPath: secondDry,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dry.CanonicalSessionFiles != 1 || dry.LegacyHistoryRows != 0 || dry.DeterministicRows != 0 || dry.ReceiptLinkedRows != 0 {
		t.Fatalf("canonical no-op counts = %#v", dry)
	}
	if _, err := Run(context.Background(), Options{
		Mode: ModeApply, SourceDir: fixture.output, EventDBPath: fixture.eventDB,
		ConversationDBPath: fixture.conversation, OutputDir: secondOutput,
		ReceiptPath: filepath.Join(fixture.root, "second-apply.json"), DryRunReceipt: secondDry,
	}); err != nil {
		t.Fatal(err)
	}
	if got := snapshotDirectory(t, secondOutput); !reflect.DeepEqual(got, canonicalBefore) {
		t.Fatal("canonical source was not copied byte-for-byte")
	}
}

func TestMigrationRejectsSourceAndEvidenceDriftBeforeOutputMutation(t *testing.T) {
	t.Run("source drift", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		dryPath := filepath.Join(fixture.root, "dry.json")
		if _, err := Run(context.Background(), Options{Mode: ModeDryRun, SourceDir: fixture.source, EventDBPath: fixture.eventDB, ConversationDBPath: fixture.conversation, ReceiptPath: dryPath}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixture.source, "notes.bin"), []byte("drift"), 0640); err != nil {
			t.Fatal(err)
		}
		_, err := Run(context.Background(), Options{Mode: ModeApply, SourceDir: fixture.source, EventDBPath: fixture.eventDB, ConversationDBPath: fixture.conversation, OutputDir: fixture.output, ReceiptPath: filepath.Join(fixture.root, "apply.json"), DryRunReceipt: dryPath})
		if err == nil {
			t.Fatal("source drift was accepted")
		}
		if entries, readErr := os.ReadDir(fixture.output); readErr != nil || len(entries) != 0 {
			t.Fatal("source drift mutated output")
		}
	})

	t.Run("relevant event drift", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		dryPath := filepath.Join(fixture.root, "dry.json")
		if _, err := Run(context.Background(), Options{Mode: ModeDryRun, SourceDir: fixture.source, EventDBPath: fixture.eventDB, ConversationDBPath: fixture.conversation, ReceiptPath: dryPath}); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", fixture.eventDB)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(`UPDATE event_envelope SET envelope_json = ? WHERE event_id = ?`, `{"event_type":"message.received","trace_id":"trc_drift","payload":{"job_id":"job-linked","message_id":"msg_drift","message_text":"changed","channel":"line","chat_id":"U123"}}`, "event-user")
		_ = db.Close()
		if err != nil {
			t.Fatal(err)
		}
		_, err = Run(context.Background(), Options{Mode: ModeApply, SourceDir: fixture.source, EventDBPath: fixture.eventDB, ConversationDBPath: fixture.conversation, OutputDir: fixture.output, ReceiptPath: filepath.Join(fixture.root, "apply.json"), DryRunReceipt: dryPath})
		if err == nil {
			t.Fatal("relevant event drift was accepted")
		}
	})

	t.Run("unrelated event does not drift evidence", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		dryPath := filepath.Join(fixture.root, "dry.json")
		dry, err := Run(context.Background(), Options{Mode: ModeDryRun, SourceDir: fixture.source, EventDBPath: fixture.eventDB, ConversationDBPath: fixture.conversation, ReceiptPath: dryPath})
		if err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", fixture.eventDB)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(`INSERT INTO event_envelope(event_id, trace_id, envelope_json) VALUES (?, ?, ?)`, "unrelated-new", "trc_unrelated", `{"event_type":"message.received","trace_id":"trc_unrelated","payload":{"job_id":"not-a-legacy-job","message_id":"msg_unrelated"}}`)
		_ = db.Close()
		if err != nil {
			t.Fatal(err)
		}
		applied, err := Run(context.Background(), Options{Mode: ModeApply, SourceDir: fixture.source, EventDBPath: fixture.eventDB, ConversationDBPath: fixture.conversation, OutputDir: fixture.output, ReceiptPath: filepath.Join(fixture.root, "apply.json"), DryRunReceipt: dryPath})
		if err != nil {
			t.Fatalf("unrelated event changed plan: %v", err)
		}
		if applied.EventEvidenceSHA256 != dry.EventEvidenceSHA256 {
			t.Fatal("unrelated event changed relevant evidence hash")
		}
	})
}

func TestMigrationRejectsAmbiguityMismatchCollisionAndSchema(t *testing.T) {
	t.Run("multiple traces", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		insertEvent(t, fixture.eventDB, "event-extra-trace", "trc_extra", `{"event_type":"message.received","trace_id":"trc_extra","payload":{"job_id":"job-linked","message_id":"msg_extra","message_text":"hello","channel":"line","chat_id":"U123"}}`)
		assertBlocked(t, fixture, "ambiguous")
	})
	t.Run("receipt metadata mismatch", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		linked := fixture.receiptLinkedIdentity()
		envelope := `{"event_type":"message.received","trace_id":"` + string(linked.traceID) + `","payload":{"job_id":"job-linked","message_id":"` + string(linked.userMessageID) + `","message_text":"wrong","channel":"line","chat_id":"U123"}}`
		insertEvent(t, fixture.eventDB, "event-mismatch", string(linked.traceID), envelope)
		assertBlocked(t, fixture, "mismatch")
	})
	t.Run("canonical collision", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		mapping, err := deterministicMapping("job-no-event")
		if err != nil {
			t.Fatal(err)
		}
		replaceLinkedIdentity(t, fixture, mapping.identity)
		assertBlocked(t, fixture, "collision")
	})
	t.Run("unknown root schema", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		path := filepath.Join(fixture.source, fixture.sessionID+".json")
		var fields map[string]any
		if err := json.Unmarshal(readFile(t, path), &fields); err != nil {
			t.Fatal(err)
		}
		fields["unknown"] = true
		data, _ := json.Marshal(fields)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		assertBlocked(t, fixture, "schema")
	})
}

func TestMigrationRejectsUnsafeSourceEntriesAndStrictReceipt(t *testing.T) {
	fixture := newMigrationFixture(t)
	if err := os.Mkdir(filepath.Join(fixture.source, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	assertBlocked(t, fixture, "nested")

	fixture = newMigrationFixture(t)
	dryPath := filepath.Join(fixture.root, "dry.json")
	if _, err := Run(context.Background(), Options{Mode: ModeDryRun, SourceDir: fixture.source, EventDBPath: fixture.eventDB, ConversationDBPath: fixture.conversation, ReceiptPath: dryPath}); err != nil {
		t.Fatal(err)
	}
	data := readFile(t, dryPath)
	data = append(data, []byte(" trailing")...)
	if err := os.WriteFile(dryPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), Options{Mode: ModeApply, SourceDir: fixture.source, EventDBPath: fixture.eventDB, ConversationDBPath: fixture.conversation, OutputDir: fixture.output, ReceiptPath: filepath.Join(fixture.root, "apply.json"), DryRunReceipt: dryPath})
	if err == nil {
		t.Fatal("trailing receipt data was accepted")
	}
}

func TestMigrationRejectsMixedSchemasAliasesAndUnsafePaths(t *testing.T) {
	t.Run("mixed session directory", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		identity, err := migrationIdentity("canonical-extra")
		if err != nil {
			t.Fatal(err)
		}
		canonicalID := modulecore.NewSessionID()
		canonical := outputSessionDTO{
			ID: string(canonicalID), LogicalDate: "2026-09-05",
			ChannelAddress: outputAddressDTO{ChannelType: "line", ExternalConversationID: "U999"},
			History: []outputTurnDTO{{
				RootTaskID: string(identity.rootTaskID), TurnID: string(identity.turnID), TraceID: string(identity.traceID),
				UserMessageID: string(identity.userMessageID), AgentMessageID: string(identity.agentMessageID),
				MessageText: "canonical", ChannelAddress: outputAddressDTO{ChannelType: "line", ExternalConversationID: "U999"},
				Attachments: []attachment.Attachment{}, Route: "chat",
			}},
			Memory: json.RawMessage(`{}`), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		data, err := json.Marshal(canonical)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixture.source, string(canonicalID)+".json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		assertBlocked(t, fixture, "mixed-directory")
	})
	t.Run("mixed parent address", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		path := filepath.Join(fixture.source, fixture.sessionID+".json")
		var fields map[string]any
		if err := json.Unmarshal(readFile(t, path), &fields); err != nil {
			t.Fatal(err)
		}
		fields["channel_address"] = map[string]string{"channel": "line", "address": "U123", "channel_type": "line"}
		data, _ := json.Marshal(fields)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		assertBlocked(t, fixture, "mixed-address")
	})
	t.Run("history parent mismatch", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		path := filepath.Join(fixture.source, fixture.sessionID+".json")
		var fields map[string]any
		if err := json.Unmarshal(readFile(t, path), &fields); err != nil {
			t.Fatal(err)
		}
		history := fields["history"].([]any)
		history[0].(map[string]any)["chat_id"] = "U999"
		fields["history"] = history
		data, _ := json.Marshal(fields)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		assertBlocked(t, fixture, "history-address")
	})
	t.Run("filename and ID mismatch", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		if err := os.Rename(filepath.Join(fixture.source, fixture.sessionID+".json"), filepath.Join(fixture.source, "ses_bad.json")); err != nil {
			t.Fatal(err)
		}
		assertBlocked(t, fixture, "filename")
	})
	t.Run("source symlink", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		if err := os.Symlink(filepath.Join(fixture.source, "notes.bin"), filepath.Join(fixture.source, "link.bin")); err != nil {
			t.Fatalf("create symlink: %v", err)
		}
		assertBlocked(t, fixture, "symlink")
	})
	t.Run("receipt alias and nonfresh target", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		if _, err := Run(context.Background(), Options{Mode: ModeDryRun, SourceDir: fixture.source, EventDBPath: fixture.eventDB, ConversationDBPath: fixture.conversation, ReceiptPath: filepath.Join(fixture.root, "dry.json")}); err != nil {
			t.Fatal(err)
		}
		_, err := Run(context.Background(), Options{Mode: ModeApply, SourceDir: fixture.source, EventDBPath: fixture.eventDB, ConversationDBPath: fixture.conversation, OutputDir: fixture.source, ReceiptPath: filepath.Join(fixture.root, "apply.json"), DryRunReceipt: filepath.Join(fixture.root, "dry.json")})
		if err == nil {
			t.Fatal("source/output alias was accepted")
		}
		_, err = Run(context.Background(), Options{Mode: ModeDryRun, SourceDir: fixture.source, EventDBPath: fixture.eventDB, ConversationDBPath: fixture.conversation, ReceiptPath: filepath.Join(fixture.root, "dry.json")})
		if err == nil {
			t.Fatal("nonfresh receipt target was accepted")
		}
	})
	t.Run("receipt permission", func(t *testing.T) {
		fixture := newMigrationFixture(t)
		dryPath := filepath.Join(fixture.root, "dry.json")
		if _, err := Run(context.Background(), Options{Mode: ModeDryRun, SourceDir: fixture.source, EventDBPath: fixture.eventDB, ConversationDBPath: fixture.conversation, ReceiptPath: dryPath}); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dryPath, 0644); err != nil {
			t.Fatal(err)
		}
		_, err := Run(context.Background(), Options{Mode: ModeApply, SourceDir: fixture.source, EventDBPath: fixture.eventDB, ConversationDBPath: fixture.conversation, OutputDir: fixture.output, ReceiptPath: filepath.Join(fixture.root, "apply.json"), DryRunReceipt: dryPath})
		if err == nil {
			t.Fatal("non-private dry-run receipt was accepted")
		}
	})
}

func newMigrationFixture(t *testing.T) *migrationFixture {
	t.Helper()
	root := t.TempDir()
	fixture := &migrationFixture{t: t, root: root, source: filepath.Join(root, "source"), eventDB: filepath.Join(root, "events.db"), conversation: filepath.Join(root, "conversation.db"), output: filepath.Join(root, "output"), sessionID: string(modulecore.NewSessionID()), jobIDs: []string{"job-linked", "job-no-receipt", "job-no-event"}}
	for _, dir := range []string{fixture.source, fixture.output} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	legacy := map[string]any{
		"id": fixture.sessionID, "logical_date": "2026-09-05",
		"channel_address": map[string]string{"channel": "line", "address": "U123"},
		"history": []map[string]string{
			{"job_id": "job-linked", "user_message": "hello", "channel": "line", "chat_id": "U123", "route": "chat"},
			{"job_id": "job-no-receipt", "user_message": "second", "channel": "line", "chat_id": "U123", "route": "chat"},
			{"job_id": "job-no-event", "user_message": "third", "channel": "line", "chat_id": "U123", "route": "chat"},
		},
		"memory": map[string]any{"keep": "value"}, "created_at": "2026-09-05T00:00:00Z", "updated_at": "2026-09-05T00:00:00Z",
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.source, fixture.sessionID+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.source, "notes.bin"), []byte("non-session bytes\x00\xff"), 0640); err != nil {
		t.Fatal(err)
	}
	setupDatabases(t, fixture)
	return fixture
}

func setupDatabases(t *testing.T, fixture *migrationFixture) {
	t.Helper()
	eventDB, err := sql.Open("sqlite", fixture.eventDB)
	if err != nil {
		t.Fatal(err)
	}
	_, err = eventDB.Exec(`CREATE TABLE event_envelope(event_id TEXT, trace_id TEXT, envelope_json TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	linked := fixture.receiptLinkedIdentity()
	insert := func(id, trace, envelope string) {
		if _, err := eventDB.Exec(`INSERT INTO event_envelope(event_id, trace_id, envelope_json) VALUES (?, ?, ?)`, id, trace, envelope); err != nil {
			t.Fatal(err)
		}
	}
	insert("event-user", string(linked.traceID), `{"event_type":"message.received","trace_id":"`+string(linked.traceID)+`","payload":{"job_id":"job-linked","message_id":"`+string(linked.userMessageID)+`","session_id":"`+fixture.sessionID+`","message_text":"hello","channel":"line","chat_id":"U123"}}`)
	insert("event-agent", string(linked.traceID), `{"event_type":"agent.response","trace_id":"`+string(linked.traceID)+`","payload":{"job_id":"job-linked","message_id":"`+string(linked.agentMessageID)+`","session_id":"`+fixture.sessionID+`","channel":"line","chat_id":"U123","route":"chat"}}`)
	insert("event-extra", string(linked.traceID), `{"event_type":"agent.response","trace_id":"`+string(linked.traceID)+`","payload":{"job_id":"job-linked","message_id":"msg_extra","channel":"line","chat_id":"U123","route":"chat"}}`)
	insert("event-deterministic", "trc_no_receipt", `{"event_type":"message.received","trace_id":"trc_no_receipt","payload":{"job_id":"job-no-receipt","message_id":"msg_no_receipt","message_text":"second","channel":"line","chat_id":"U123"}}`)
	if err := eventDB.Close(); err != nil {
		t.Fatal(err)
	}

	conversationDB, err := sql.Open("sqlite", fixture.conversation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conversationDB.Exec(`CREATE TABLE conversation_turn_receipt(turn_id TEXT, trace_id TEXT, root_task_id TEXT, session_id TEXT, user_message_id TEXT, agent_message_id TEXT, result_json TEXT)`); err != nil {
		t.Fatal(err)
	}
	result := map[string]string{"root_task_id": string(linked.rootTaskID), "turn_id": string(linked.turnID), "trace_id": string(linked.traceID), "user_message_id": string(linked.userMessageID), "agent_message_id": string(linked.agentMessageID), "session_id": fixture.sessionID}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conversationDB.Exec(`INSERT INTO conversation_turn_receipt(turn_id, trace_id, root_task_id, session_id, user_message_id, agent_message_id, result_json) VALUES (?, ?, ?, ?, ?, ?, ?)`, linked.turnID, linked.traceID, linked.rootTaskID, fixture.sessionID, linked.userMessageID, linked.agentMessageID, string(resultJSON)); err != nil {
		t.Fatal(err)
	}
	if err := conversationDB.Close(); err != nil {
		t.Fatal(err)
	}
}

func (f *migrationFixture) receiptLinkedIdentity() canonicalIdentity {
	root, _ := modulecore.NewMigrationID(modulecore.CanonicalTaskID, "receipt", "job", "linked")
	turn, _ := modulecore.NewMigrationID(modulecore.CanonicalTurnID, "receipt", "job", "linked")
	trace, _ := modulecore.NewMigrationID(modulecore.CanonicalTraceID, "receipt", "job", "linked")
	user, _ := modulecore.NewMigrationID(modulecore.CanonicalMessageID, "receipt", "user", "linked")
	agent, _ := modulecore.NewMigrationID(modulecore.CanonicalMessageID, "receipt", "agent", "linked")
	return canonicalIdentity{rootTaskID: modulecore.TaskID(root), turnID: modulecore.TurnID(turn), traceID: modulecore.TraceID(trace), userMessageID: modulecore.MessageID(user), agentMessageID: modulecore.MessageID(agent)}
}

func replaceLinkedIdentity(t *testing.T, fixture *migrationFixture, identity canonicalIdentity) {
	t.Helper()
	eventDB, err := sql.Open("sqlite", fixture.eventDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eventDB.Exec(`DELETE FROM event_envelope WHERE event_id IN ('event-user', 'event-agent', 'event-extra')`); err != nil {
		_ = eventDB.Close()
		t.Fatal(err)
	}
	if err := eventDB.Close(); err != nil {
		t.Fatal(err)
	}
	insertEvent(t, fixture.eventDB, "event-user", string(identity.traceID), `{"event_type":"message.received","trace_id":"`+string(identity.traceID)+`","payload":{"job_id":"job-linked","message_id":"`+string(identity.userMessageID)+`","session_id":"`+fixture.sessionID+`","message_text":"hello","channel":"line","chat_id":"U123"}}`)
	insertEvent(t, fixture.eventDB, "event-agent", string(identity.traceID), `{"event_type":"agent.response","trace_id":"`+string(identity.traceID)+`","payload":{"job_id":"job-linked","message_id":"`+string(identity.agentMessageID)+`","session_id":"`+fixture.sessionID+`","channel":"line","chat_id":"U123","route":"chat"}}`)

	result := map[string]string{
		"root_task_id": string(identity.rootTaskID), "turn_id": string(identity.turnID), "trace_id": string(identity.traceID),
		"user_message_id": string(identity.userMessageID), "agent_message_id": string(identity.agentMessageID), "session_id": fixture.sessionID,
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	conversationDB, err := sql.Open("sqlite", fixture.conversation)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conversationDB.Exec(
		`UPDATE conversation_turn_receipt SET turn_id = ?, trace_id = ?, root_task_id = ?, user_message_id = ?, agent_message_id = ?, result_json = ?`,
		identity.turnID, identity.traceID, identity.rootTaskID, identity.userMessageID, identity.agentMessageID, string(resultJSON),
	)
	if closeErr := conversationDB.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
}

func insertEvent(t *testing.T, path, eventID, traceID, envelope string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO event_envelope(event_id, trace_id, envelope_json) VALUES (?, ?, ?)`, eventID, traceID, envelope); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertBlocked(t *testing.T, fixture *migrationFixture, name string) {
	t.Helper()
	_, err := Run(context.Background(), Options{Mode: ModeDryRun, SourceDir: fixture.source, EventDBPath: fixture.eventDB, ConversationDBPath: fixture.conversation, ReceiptPath: filepath.Join(fixture.root, name+"-receipt.json")})
	if err == nil {
		t.Fatalf("%s was accepted", name)
	}
}

func snapshotDirectory(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			result[entry.Name()+"/"] = nil
			continue
		}
		result[entry.Name()] = readFile(t, filepath.Join(dir, entry.Name()))
	}
	return result
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func permission(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func assertNoLegacyFields(t *testing.T, path string) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(readFile(t, path), &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"channel", "chat_id"} {
		if _, ok := fields[key]; ok {
			t.Fatalf("legacy root field %s remains", key)
		}
	}
	var history []map[string]json.RawMessage
	if err := json.Unmarshal(fields["history"], &history); err != nil {
		t.Fatal(err)
	}
	for _, row := range history {
		for _, key := range []string{"job_id", "user_message", "channel", "chat_id"} {
			if _, ok := row[key]; ok {
				t.Fatalf("legacy history field %s remains", key)
			}
		}
	}
}
