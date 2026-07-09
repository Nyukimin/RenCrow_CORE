package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

type mockUserMemoryManager struct {
	createInput     domainmemory.CreateUserMemoryInput
	createInputs    []domainmemory.CreateUserMemoryInput
	listItems       []domainmemory.UserMemory
	listErr         error
	createErr       error
	forgetErr       error
	forgetID        string
	forgetReason    string
	supersedeOldID  string
	supersedeNewID  string
	supersedeReason string
	supersedeErr    error
}

func (m *mockUserMemoryManager) CreateUserMemory(_ context.Context, input domainmemory.CreateUserMemoryInput) (*domainmemory.UserMemory, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	m.createInput = input
	m.createInputs = append(m.createInputs, input)
	return &domainmemory.UserMemory{
		ID:               "mem-1",
		Namespace:        "user:" + input.UserID,
		UserID:           input.UserID,
		Type:             input.Type,
		Statement:        strings.TrimSpace(input.Statement),
		EvidenceEventIDs: input.EvidenceEventIDs,
		Confidence:       input.Confidence,
		Sensitivity:      input.Sensitivity,
		State:            input.State,
		Active:           true,
	}, nil
}

func (m *mockUserMemoryManager) ListUserMemories(_ context.Context, _ string, _ string, _ bool, _ int) ([]domainmemory.UserMemory, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return append([]domainmemory.UserMemory(nil), m.listItems...), nil
}

func (m *mockUserMemoryManager) UpdateUserMemoryState(context.Context, string, string, string) (*domainmemory.UserMemory, error) {
	return nil, nil
}

func (m *mockUserMemoryManager) ForgetUserMemory(_ context.Context, id string, reason string) (*domainmemory.UserMemory, error) {
	if m.forgetErr != nil {
		return nil, m.forgetErr
	}
	m.forgetID = id
	m.forgetReason = reason
	return &domainmemory.UserMemory{ID: id, Namespace: "user:ren", UserID: "ren", Statement: "短く答える", Active: false}, nil
}

func (m *mockUserMemoryManager) SupersedeUserMemory(_ context.Context, oldID string, newID string, reason string) (*domainmemory.UserMemory, error) {
	if m.supersedeErr != nil {
		return nil, m.supersedeErr
	}
	m.supersedeOldID = oldID
	m.supersedeNewID = newID
	m.supersedeReason = reason
	return &domainmemory.UserMemory{ID: oldID, Namespace: "user:ren", UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "短く答える", Active: false, SupersededBy: newID}, nil
}

func TestParseChatCommand(t *testing.T) {
	tests := []struct {
		input   string
		wantCmd string
		wantArg string
	}{
		{"/status", "status", ""},
		{"/stop", "stop", ""},
		{"/compact", "compact", ""},
		{"/context", "context", ""},
		{"/new", "new", ""},
		{"/status extra", "status", "extra"},
		{"/code something", "", ""},   // ルーティングコマンドはチャットコマンドではない
		{"/code3 something", "", ""},  // 同上
		{"hello", "", ""},             // コマンドではない
		{"", "", ""},                  // 空文字列
		{"/unknown", "", ""},          // 未知のコマンド
		{"  /status  ", "status", ""}, // 空白あり
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			cmd, arg := parseChatCommand(tt.input)
			if cmd != tt.wantCmd {
				t.Errorf("parseChatCommand(%q) cmd = %q, want %q", tt.input, cmd, tt.wantCmd)
			}
			if arg != tt.wantArg {
				t.Errorf("parseChatCommand(%q) arg = %q, want %q", tt.input, arg, tt.wantArg)
			}
		})
	}
}

func TestHandleChatCommand_NoEngine(t *testing.T) {
	// conversationEngine が nil の場合
	m := &MioAgent{}

	tests := []string{"/status", "/compact", "/context", "/new"}
	for _, cmd := range tests {
		t.Run(cmd, func(t *testing.T) {
			result, err := m.HandleChatCommand(nil, "session1", cmd)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !result.Handled {
				t.Error("expected Handled=true")
			}
			if result.Response != "会話エンジンが無効です。" {
				t.Errorf("unexpected response: %s", result.Response)
			}
		})
	}
}

func TestHandleChatCommand_Stop(t *testing.T) {
	m := &MioAgent{}
	result, err := m.HandleChatCommand(nil, "session1", "/stop")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Handled {
		t.Error("expected Handled=true")
	}
	if result.Response == "" {
		t.Error("expected non-empty response for /stop")
	}
}

func TestHandleChatCommand_NotCommand(t *testing.T) {
	m := &MioAgent{}
	result, err := m.HandleChatCommand(nil, "session1", "こんにちは")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Handled {
		t.Error("expected Handled=false for normal message")
	}
}

func TestHandleChatCommand_RoutingCommand(t *testing.T) {
	m := &MioAgent{}
	result, err := m.HandleChatCommand(nil, "session1", "/code fix bug")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Handled {
		t.Error("expected Handled=false for routing command /code")
	}
}

func TestHandleChatCommand_UserMemoryRemember(t *testing.T) {
	mem := &mockUserMemoryManager{}
	m := (&MioAgent{}).WithUserMemoryManager(mem)

	result, err := m.HandleChatCommand(context.Background(), "session1", "覚えて: 短く論理的な説明を好む")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Handled || !strings.Contains(result.Response, "覚える候補") {
		t.Fatalf("unexpected result: %+v", result)
	}
	if mem.createInput.UserID != "ren" ||
		mem.createInput.State != domainmemory.MemoryStateCandidate ||
		mem.createInput.Type != domainmemory.UserMemoryTypePreference ||
		mem.createInput.Statement != "短く論理的な説明を好む" ||
		len(mem.createInput.EvidenceEventIDs) != 1 {
		t.Fatalf("unexpected create input: %+v", mem.createInput)
	}
}

func TestHandleChatCommand_UserMemoryRememberUnknownSessionAndCreateError(t *testing.T) {
	mem := &mockUserMemoryManager{}
	m := (&MioAgent{}).WithUserMemoryManager(mem)

	result, err := m.HandleChatCommand(context.Background(), "   ", "AIの話は短くを覚えて")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Handled || mem.createInput.EvidenceEventIDs[0] != "chat_memory_command:unknown_session" {
		t.Fatalf("unexpected result=%+v input=%+v", result, mem.createInput)
	}

	mem = &mockUserMemoryManager{createErr: errors.New("store down")}
	m = (&MioAgent{}).WithUserMemoryManager(mem)
	_, err = m.HandleChatCommand(context.Background(), "session1", "覚えて AIの話は短く")
	if err == nil || !strings.Contains(err.Error(), "user memory create failed") {
		t.Fatalf("err=%v, want create wrapper", err)
	}
}

func TestHandleChatCommand_UserMemoryPrioritize(t *testing.T) {
	mem := &mockUserMemoryManager{}
	m := (&MioAgent{}).WithUserMemoryManager(mem)

	result, err := m.HandleChatCommand(context.Background(), "session1", "これを優先して 日本語で答える")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Handled || !strings.Contains(result.Response, "固定") {
		t.Fatalf("unexpected result: %+v", result)
	}
	if mem.createInput.State != domainmemory.MemoryStatePinned ||
		mem.createInput.Type != domainmemory.UserMemoryTypeConstraint ||
		mem.createInput.Source != "user_explicit_priority" {
		t.Fatalf("unexpected priority input: %+v", mem.createInput)
	}
}

func TestHandleChatCommand_UserMemorySaveSummaryShowAndDoNotUse(t *testing.T) {
	mem := &mockUserMemoryManager{
		listItems: []domainmemory.UserMemory{{
			ID:          "mem-1",
			Namespace:   "user:ren",
			UserID:      "ren",
			Type:        domainmemory.UserMemoryTypePreference,
			Statement:   "短く答える",
			State:       domainmemory.MemoryStateConfirmed,
			Sensitivity: "normal",
			Active:      true,
		}},
	}
	m := (&MioAgent{}).WithUserMemoryManager(mem)

	summary, err := m.HandleChatCommand(context.Background(), "session1", "要約して保存: 今日の修復方針を決めた")
	if err != nil {
		t.Fatalf("summary command error: %v", err)
	}
	if !summary.Handled || !strings.Contains(summary.Response, "保存候補") {
		t.Fatalf("unexpected summary result: %+v", summary)
	}
	if mem.createInput.Type != domainmemory.UserMemoryTypeEpisode || mem.createInput.Source != "user_summary_save_command" {
		t.Fatalf("unexpected summary create input: %+v", mem.createInput)
	}

	show, err := m.HandleChatCommand(context.Background(), "session1", "この記憶を見せて")
	if err != nil {
		t.Fatalf("show command error: %v", err)
	}
	if !show.Handled || !strings.Contains(show.Response, "mem-1") || !strings.Contains(show.Response, "短く答える") {
		t.Fatalf("unexpected show result: %+v", show)
	}

	doNotUse, err := m.HandleChatCommand(context.Background(), "session1", "今後使わないで 短く答える")
	if err != nil {
		t.Fatalf("do_not_use command error: %v", err)
	}
	if !doNotUse.Handled || mem.forgetID != "mem-1" || mem.forgetReason != "do_not_use" {
		t.Fatalf("unexpected do_not_use result=%+v mem=%+v", doNotUse, mem)
	}
}

func TestHandleChatCommand_UserMemoryEmptyBody(t *testing.T) {
	mem := &mockUserMemoryManager{}
	m := (&MioAgent{}).WithUserMemoryManager(mem)

	result, err := m.HandleChatCommand(context.Background(), "session1", "覚えて")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Handled || !strings.Contains(result.Response, "具体的") || len(mem.createInputs) != 0 {
		t.Fatalf("unexpected result=%+v created=%d", result, len(mem.createInputs))
	}
}

func TestHandleChatCommand_UserMemoryForget(t *testing.T) {
	mem := &mockUserMemoryManager{
		listItems: []domainmemory.UserMemory{{
			ID:        "mem-1",
			Namespace: "user:ren",
			UserID:    "ren",
			Statement: "短く答える",
			State:     domainmemory.MemoryStateConfirmed,
			Active:    true,
		}},
	}
	m := (&MioAgent{}).WithUserMemoryManager(mem)

	result, err := m.HandleChatCommand(context.Background(), "session1", "忘れて 短く答える")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Handled || !strings.Contains(result.Response, "無効化") {
		t.Fatalf("unexpected result: %+v", result)
	}
	if mem.forgetID != "mem-1" || mem.forgetReason != "forget" {
		t.Fatalf("unexpected forget args: %+v", mem)
	}
}

func TestHandleChatCommand_UserMemoryForgetByIDNotFoundAndErrors(t *testing.T) {
	mem := &mockUserMemoryManager{
		listItems: []domainmemory.UserMemory{{
			ID:        "mem-1",
			Namespace: "user:ren",
			UserID:    "ren",
			Statement: "短く答える",
			State:     domainmemory.MemoryStateConfirmed,
			Active:    true,
		}},
	}
	m := (&MioAgent{}).WithUserMemoryManager(mem)
	result, err := m.HandleChatCommand(context.Background(), "session1", "これは違う mem-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Handled || mem.forgetID != "mem-1" || mem.forgetReason != "correct" {
		t.Fatalf("unexpected result=%+v mem=%+v", result, mem)
	}

	mem = &mockUserMemoryManager{}
	m = (&MioAgent{}).WithUserMemoryManager(mem)
	result, err = m.HandleChatCommand(context.Background(), "session1", "忘れて 存在しない")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Response, "見つけられません") {
		t.Fatalf("unexpected result: %+v", result)
	}

	mem = &mockUserMemoryManager{listErr: errors.New("list failed")}
	m = (&MioAgent{}).WithUserMemoryManager(mem)
	_, err = m.HandleChatCommand(context.Background(), "session1", "忘れて 存在しない")
	if err == nil || !strings.Contains(err.Error(), "user memory list failed") {
		t.Fatalf("err=%v, want list wrapper", err)
	}

	mem = &mockUserMemoryManager{
		forgetErr: errors.New("forget failed"),
		listItems: []domainmemory.UserMemory{{
			ID:        "mem-1",
			Namespace: "user:ren",
			UserID:    "ren",
			Statement: "短く答える",
			State:     domainmemory.MemoryStateConfirmed,
			Active:    true,
		}},
	}
	m = (&MioAgent{}).WithUserMemoryManager(mem)
	_, err = m.HandleChatCommand(context.Background(), "session1", "忘れて mem-1")
	if err == nil || !strings.Contains(err.Error(), "user memory forget failed") {
		t.Fatalf("err=%v, want forget wrapper", err)
	}
}

func TestHandleChatCommand_UserMemoryForgetAmbiguousShowsCandidates(t *testing.T) {
	mem := &mockUserMemoryManager{
		listItems: []domainmemory.UserMemory{
			{ID: "mem-1", Namespace: "user:ren", UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "短く答える", State: domainmemory.MemoryStateConfirmed, Active: true},
			{ID: "mem-2", Namespace: "user:ren", UserID: "ren", Type: domainmemory.UserMemoryTypeConstraint, Statement: "短く要点だけ答える", State: domainmemory.MemoryStatePinned, Active: true},
		},
	}
	m := (&MioAgent{}).WithUserMemoryManager(mem)

	result, err := m.HandleChatCommand(context.Background(), "session1", "忘れて 短く")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Handled || !strings.Contains(result.Response, "候補が複数") || !strings.Contains(result.Response, "mem-1") || !strings.Contains(result.Response, "mem-2") {
		t.Fatalf("unexpected ambiguous result: %+v", result)
	}
	if mem.forgetID != "" {
		t.Fatalf("ambiguous forget should not modify memory: %+v", mem)
	}
}

func TestHandleChatCommand_UserMemorySupersedeCreatesCandidateAndSupersedesOld(t *testing.T) {
	mem := &mockUserMemoryManager{
		listItems: []domainmemory.UserMemory{{
			ID:          "mem-old",
			Namespace:   "user:ren",
			UserID:      "ren",
			Type:        domainmemory.UserMemoryTypePreference,
			Statement:   "短く答える",
			State:       domainmemory.MemoryStateConfirmed,
			Sensitivity: "normal",
			Scope:       "all_personas",
			Active:      true,
		}},
	}
	m := (&MioAgent{}).WithUserMemoryManager(mem)

	result, err := m.HandleChatCommand(context.Background(), "session1", "記憶を置き換えて: 短く答える => 詳しく答える")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Handled || !strings.Contains(result.Response, "置き換え候補") {
		t.Fatalf("unexpected result: %+v", result)
	}
	if mem.createInput.Statement != "詳しく答える" ||
		mem.createInput.State != domainmemory.MemoryStateCandidate ||
		mem.createInput.Source != "user_memory_supersede_command" {
		t.Fatalf("unexpected supersede create input: %+v", mem.createInput)
	}
	if mem.supersedeOldID != "mem-old" || mem.supersedeNewID != "mem-1" || mem.supersedeReason != "supersede" {
		t.Fatalf("unexpected supersede args: %+v", mem)
	}
}

func TestParseUserMemoryCommandVariants(t *testing.T) {
	cases := []struct {
		input      string
		wantAction string
		wantBody   string
	}{
		{"優先して: 日本語で答える。", "prioritize", "日本語で答える"},
		{"要約して保存: 今日の修復方針", "save_summary", "今日の修復方針"},
		{"この記憶を見せて", "show", ""},
		{"今後使わないで: 古い制約", "do_not_use", "古い制約"},
		{"記憶を置き換えて: 古い => 新しい", "supersede", "古い => 新しい"},
		{"古い記憶を新しい記憶に置き換えて", "supersede", "古い記憶 => 新しい記憶"},
		{"覚えて、短く答える", "remember", "、短く答える"},
		{"この設定は忘れて", "forget", "この設定"},
		{"これは違う: 前の記憶", "correct", "前の記憶"},
		{"/status", "", ""},
		{"普通の発話", "", ""},
		{"", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			gotAction, gotBody := parseUserMemoryCommand(tc.input)
			if gotAction != tc.wantAction || gotBody != tc.wantBody {
				t.Fatalf("got (%q,%q), want (%q,%q)", gotAction, gotBody, tc.wantAction, tc.wantBody)
			}
		})
	}
}

func TestUserMemoryPromptFiltersAndFormatsInjectableMemories(t *testing.T) {
	mem := &mockUserMemoryManager{listItems: []domainmemory.UserMemory{
		{Statement: "確定記憶", State: domainmemory.MemoryStateConfirmed, Active: true, Sensitivity: "normal"},
		{Statement: "優先記憶", State: domainmemory.MemoryStatePinned, Active: true, Sensitivity: "normal"},
		{Statement: "候補記憶", State: domainmemory.MemoryStateCandidate, Active: true, Sensitivity: "normal"},
		{Statement: "機微記憶", State: domainmemory.MemoryStateConfirmed, Active: true, Sensitivity: "sensitive"},
		{Statement: "無効記憶", State: domainmemory.MemoryStateConfirmed, Active: false, Sensitivity: "normal"},
		{Statement: "   ", State: domainmemory.MemoryStateConfirmed, Active: true, Sensitivity: "normal"},
	}}
	m := (&MioAgent{}).WithUserMemoryManager(mem)

	prompt, err := m.userMemoryPrompt(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"思い出したこと", "- 確定記憶", "- [優先] 優先記憶", "confirmed/pinned"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt=%q, missing %q", prompt, want)
		}
	}
	for _, notWant := range []string{"候補記憶", "機微記憶", "無効記憶"} {
		if strings.Contains(prompt, notWant) {
			t.Fatalf("prompt=%q, unexpectedly contains %q", prompt, notWant)
		}
	}

	mem.listItems = nil
	prompt, err = m.userMemoryPrompt(context.Background())
	if err != nil || prompt != "" {
		t.Fatalf("prompt=%q err=%v, want empty", prompt, err)
	}

	mem.listErr = errors.New("list failed")
	_, err = m.userMemoryPrompt(context.Background())
	if err == nil {
		t.Fatal("expected list error")
	}
}

func TestChatCommandsWithConversationEngine(t *testing.T) {
	now := time.Now().Add(-2 * time.Minute)
	engine := &mockConversationEngine{
		persona: conversation.PersonaState{Name: "Mio"},
		statusFunc: func(context.Context, string) (*conversation.ConversationStatus, error) {
			return &conversation.ConversationStatus{
				SessionID:    "session1",
				ThreadID:     42,
				ThreadDomain: "default",
				TurnCount:    7,
				ThreadStart:  now,
				ThreadStatus: conversation.ThreadActive,
			}, nil
		},
		beginTurnFunc: func(context.Context, string, string) (*conversation.RecallPack, error) {
			return &conversation.RecallPack{
				Persona:      conversation.PersonaState{Name: "Mio"},
				ShortContext: []conversation.Message{{Speaker: conversation.SpeakerUser, Msg: strings.Repeat("短期", 40)}},
				MidSummaries: []conversation.ThreadSummary{{Summary: strings.Repeat("中期", 50)}},
				LongFacts:    []string{strings.Repeat("長期", 50)},
			}, nil
		},
	}
	mem := &mockUserMemoryManager{listItems: []domainmemory.UserMemory{
		{State: domainmemory.MemoryStateConfirmed, Active: true},
		{State: domainmemory.MemoryStatePinned, Active: true},
		{State: domainmemory.MemoryStateCandidate, Active: true},
	}}
	m := (&MioAgent{conversationEngine: engine}).WithUserMemoryManager(mem)

	status, err := m.HandleChatCommand(context.Background(), "session1", "/status")
	if err != nil {
		t.Fatalf("status error: %v", err)
	}
	if !strings.Contains(status.Response, "スレッドID: 42") || !strings.Contains(status.Response, "ターン数: 7") {
		t.Fatalf("unexpected status response: %s", status.Response)
	}

	compact, err := m.HandleChatCommand(context.Background(), "session1", "/compact")
	if err != nil || !strings.Contains(compact.Response, "フラッシュ") {
		t.Fatalf("compact=%+v err=%v", compact, err)
	}

	contextResult, err := m.HandleChatCommand(context.Background(), "session1", "/context")
	if err != nil {
		t.Fatalf("context error: %v", err)
	}
	for _, want := range []string{"【ペルソナ】Mio", "【短期記憶】1件", "【中期記憶】1件", "【長期記憶】1件", "confirmed=1 pinned=1 candidate=1"} {
		if !strings.Contains(contextResult.Response, want) {
			t.Fatalf("context response=%q, missing %q", contextResult.Response, want)
		}
	}

	newResult, err := m.HandleChatCommand(context.Background(), "session1", "/new")
	if err != nil || !strings.Contains(newResult.Response, "リセット") {
		t.Fatalf("new=%+v err=%v", newResult, err)
	}
}

func TestChatCommandsConversationEngineErrors(t *testing.T) {
	engine := &mockConversationEngine{
		statusFunc: func(context.Context, string) (*conversation.ConversationStatus, error) {
			return nil, errors.New("status failed")
		},
		flushFunc: func(context.Context, string) error {
			return errors.New("flush failed")
		},
		resetFunc: func(context.Context, string) error {
			return errors.New("reset failed")
		},
		beginTurnFunc: func(context.Context, string, string) (*conversation.RecallPack, error) {
			return nil, errors.New("begin failed")
		},
	}
	m := &MioAgent{conversationEngine: engine}

	if _, err := m.HandleChatCommand(context.Background(), "session1", "/status"); err == nil || !strings.Contains(err.Error(), "GetStatus failed") {
		t.Fatalf("status err=%v", err)
	}
	compact, err := m.HandleChatCommand(context.Background(), "session1", "/compact")
	if err != nil || !strings.Contains(compact.Response, "失敗") {
		t.Fatalf("compact=%+v err=%v", compact, err)
	}
	contextResult, err := m.HandleChatCommand(context.Background(), "session1", "/context")
	if err != nil || !strings.Contains(contextResult.Response, "RecallPack取得に失敗") {
		t.Fatalf("context=%+v err=%v", contextResult, err)
	}
	newResult, err := m.HandleChatCommand(context.Background(), "session1", "/new")
	if err != nil || !strings.Contains(newResult.Response, "失敗") {
		t.Fatalf("new=%+v err=%v", newResult, err)
	}
}

func TestCountUserMemoryStates(t *testing.T) {
	confirmed, pinned, candidate := countUserMemoryStates([]domainmemory.UserMemory{
		{State: domainmemory.MemoryStateConfirmed, Active: true},
		{State: domainmemory.MemoryStatePinned, Active: true},
		{State: domainmemory.MemoryStateCandidate, Active: true},
		{State: domainmemory.MemoryStateCandidate, Active: false},
	})
	if confirmed != 1 || pinned != 1 || candidate != 1 {
		t.Fatalf("unexpected counts confirmed=%d pinned=%d candidate=%d", confirmed, pinned, candidate)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"hello", 10, "hello"},
		{"hello", 3, "hel..."},
		{"こんにちは世界", 4, "こんにち..."},
		{"", 5, ""},
	}
	for _, tt := range tests {
		got := truncate(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}
