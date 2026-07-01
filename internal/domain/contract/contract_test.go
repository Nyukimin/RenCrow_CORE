package contract

import "testing"

func TestContractValidate(t *testing.T) {
	valid := Contract{
		Goal:         "TTSを導入して動作確認する",
		Acceptance:   []string{"音声生成に成功する"},
		Constraints:  []string{"破壊的操作は禁止"},
		Artifacts:    []string{"generated_audio.wav"},
		Verification: []string{"再生テストに成功する"},
		Rollback:     []string{"設定変更を戻す"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("expected valid contract, got err=%v", err)
	}

	invalid := valid
	invalid.Goal = ""
	if err := invalid.Validate(); err == nil {
		t.Fatal("expected validation error for empty goal")
	}
}

func TestContractValidateRequiresAllSections(t *testing.T) {
	valid := Contract{
		Goal:         "設定を移行する",
		Acceptance:   []string{"旧キーが消える"},
		Constraints:  []string{"バックアップを作る"},
		Artifacts:    []string{"config.yaml"},
		Verification: []string{"go test ./internal/adapter/config/..."},
		Rollback:     []string{"config.yaml.bak を戻す"},
	}

	tests := []struct {
		name   string
		mutate func(*Contract)
	}{
		{name: "acceptance", mutate: func(c *Contract) { c.Acceptance = nil }},
		{name: "constraints", mutate: func(c *Contract) { c.Constraints = nil }},
		{name: "artifacts", mutate: func(c *Contract) { c.Artifacts = nil }},
		{name: "verification", mutate: func(c *Contract) { c.Verification = nil }},
		{name: "rollback", mutate: func(c *Contract) { c.Rollback = nil }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contract := valid
			tt.mutate(&contract)
			if err := contract.Validate(); err == nil {
				t.Fatalf("expected validation error for missing %s", tt.name)
			}
		})
	}
}
