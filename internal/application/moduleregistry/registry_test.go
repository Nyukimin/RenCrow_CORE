package moduleregistry

import (
	"path/filepath"
	"testing"
)

const testWorkspaceRoot = "/workspace/RenCrow"

func useTestWorkspace(t *testing.T) {
	t.Helper()
	t.Setenv("RENCROW_WORKSPACE_ROOT", testWorkspaceRoot)
}

func TestDefaultRegistryResolveExplicitModules(t *testing.T) {
	useTestWorkspace(t)
	reg := DefaultRegistry()
	tests := []struct {
		name string
		msg  string
		want string
	}{
		{name: "stt", msg: "RenCrow_STT の音声入力を修正して", want: "stt"},
		{name: "tts", msg: "TTS の読み上げを直して", want: "tts"},
		{name: "llm", msg: "RenCrow_LLM Gatewayを確認して", want: "llm"},
		{name: "cli", msg: "RenCrow_CMD の rencrowctl chat を修正して", want: "cli"},
		{name: "portal", msg: "RenCrow_PORTAL のChat画面を修正して", want: "portal"},
		{name: "games", msg: "RenCrow_GAMES のObserverを修正して", want: "games"},
		{name: "image", msg: "RenCrow_Image の生成APIを修正して", want: "image"},
		{name: "workspace", msg: "RenCrow_Workspace のsnapshotを更新して", want: "workspace"},
		{name: "chat", msg: "rencrow.service の Worker を直して", want: "chat"},
		{name: "tools", msg: "RenCrow_Tools に検証ツールを追加して", want: "tools"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reg.Resolve(tt.msg)
			if !got.Found() {
				t.Fatalf("expected module %s, got unresolved", tt.want)
			}
			if got.Module.ID != tt.want {
				t.Fatalf("module=%s, want %s; resolution=%+v", got.Module.ID, tt.want, got)
			}
			if got.Module.Root == filepath.Clean(testWorkspaceRoot) {
				t.Fatalf("parent root must not be selected as edit root")
			}
		})
	}
}

func TestDefaultRegistryCommandMetadata(t *testing.T) {
	useTestWorkspace(t)
	reg := DefaultRegistry()
	modules := make(map[string]struct {
		install string
		root    string
		kind    string
		aliases []string
	})
	for _, module := range reg.Modules() {
		modules[module.ID] = struct {
			install string
			root    string
			kind    string
			aliases []string
		}{
			install: module.InstallCommand,
			root:    module.Root,
			kind:    module.Kind,
			aliases: module.Aliases,
		}
	}

	if got := modules["cli"].install; got != "" {
		t.Fatalf("cli install command must be OS-specific operator policy, got %q", got)
	}
	for _, alias := range modules["cli"].aliases {
		switch alias {
		case "rencrow chat", "rencrow-cli", "rencrow_cli":
			t.Fatalf("cli keeps retired alias %q", alias)
		}
	}
	if got := modules["portal"].root; got != filepath.Join(testWorkspaceRoot, "RenCrow_PORTAL") {
		t.Fatalf("portal root = %q", got)
	}
	if got := modules["games"].root; got != filepath.Join(testWorkspaceRoot, "RenCrow_GAMES") {
		t.Fatalf("games root = %q", got)
	}
	if got := modules["workspace"].kind; got != "snapshot" {
		t.Fatalf("workspace kind = %q, want snapshot", got)
	}
	for _, alias := range modules["workspace"].aliases {
		if alias == "config" {
			t.Fatal("workspace keeps CORE-owned runtime config alias")
		}
	}
}

func TestDefaultRegistryParentRootIsAmbiguous(t *testing.T) {
	useTestWorkspace(t)
	got := DefaultRegistry().Resolve(testWorkspaceRoot + " を使って直して")
	if got.Found() {
		t.Fatalf("parent root should not resolve to editable module: %+v", got)
	}
	if !got.Ambiguous {
		t.Fatalf("parent root should be reported ambiguous: %+v", got)
	}
}
