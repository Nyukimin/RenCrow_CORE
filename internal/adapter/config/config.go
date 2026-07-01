package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadConfig は設定ファイルを読み込む
func LoadConfig(path string) (*Config, error) {
	// ファイル読み込み
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// ${ENV_VAR} を環境変数で展開してから YAML パース
	expanded := os.ExpandEnv(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config YAML: %w", err)
	}

	// デフォルト値設定
	cfg.setDefaults()

	// バリデーション
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	// プロンプトファイル読み込み（prompts/ → workspace/ の順でオーバーライド）
	cfg.Prompts = LoadPrompts(cfg.PromptsDir, cfg.WorkspaceDir)

	return &cfg, nil
}
