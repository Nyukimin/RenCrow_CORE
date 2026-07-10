package agent

import "testing"

func TestAgentPersona_BuildSystemPrompt(t *testing.T) {
	tests := []struct {
		name       string
		persona    AgentPersona
		taskPrompt string
		want       string
	}{
		{
			name: "personality あり",
			persona: AgentPersona{
				Name:        "Aka",
				Personality: "あなたは Aka。設計思考が得意。",
				Tone:        "analytical",
			},
			taskPrompt: "次のタスクを実行してください。",
			want:       "あなたは Aka。設計思考が得意。\n\n次のタスクを実行してください。",
		},
		{
			name: "personality なし（後方互換）",
			persona: AgentPersona{
				Name: "Coder",
				Tone: "neutral",
			},
			taskPrompt: "次のタスクを実行してください。",
			want:       "次のタスクを実行してください。",
		},
		{
			name: "複数行 personality",
			persona: AgentPersona{
				Name:        "Ao",
				Personality: "あなたは Ao。\n実装力が高く効率を重視する。\n簡潔に要点を伝える。",
				Tone:        "concise",
			},
			taskPrompt: "コードを書いてください。",
			want:       "あなたは Ao。\n実装力が高く効率を重視する。\n簡潔に要点を伝える。\n\nコードを書いてください。",
		},
		{
			name: "空の taskPrompt",
			persona: AgentPersona{
				Name:        "Gin",
				Personality: "あなたは Gin。",
			},
			taskPrompt: "",
			want:       "あなたは Gin。\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.persona.BuildSystemPrompt(tt.taskPrompt)
			if got != tt.want {
				t.Errorf("BuildSystemPrompt() = %q, want %q", got, tt.want)
			}
		})
	}
}
