package moduleregistry

import "testing"

func TestDefaultRegistryResolveExplicitModules(t *testing.T) {
	reg := DefaultRegistry()
	tests := []struct {
		name string
		msg  string
		want string
	}{
		{name: "stt", msg: "RenCrow_STT の音声入力を修正して", want: "stt"},
		{name: "tts", msg: "TTS の読み上げを直して", want: "tts"},
		{name: "llm", msg: "RenCrow_LLM の MLX provider を確認して", want: "llm"},
		{name: "cli", msg: "RenCrow_CLI の rencrow chat を修正して", want: "cli"},
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
			if got.Module.Root == "/home/nyukimi/RenCrow" {
				t.Fatalf("parent root must not be selected as edit root")
			}
		})
	}
}

func TestDefaultRegistryParentRootIsAmbiguous(t *testing.T) {
	got := DefaultRegistry().Resolve("/home/nyukimi/RenCrow を使って直して")
	if got.Found() {
		t.Fatalf("parent root should not resolve to editable module: %+v", got)
	}
	if !got.Ambiguous {
		t.Fatalf("parent root should be reported ambiguous: %+v", got)
	}
}
