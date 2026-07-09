package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadConfig は設定ファイルを読み込む
func LoadConfig(path string) (*Config, error) {
	// ファイル読み込み
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// ${ENV_VAR} を環境変数で展開してから YAML パースする。
	// ${module:...} は runtime_topology resolver 用の参照なのでここでは保持する。
	expanded := os.Expand(string(data), func(key string) string {
		if strings.HasPrefix(key, "module:") {
			return "${" + key + "}"
		}
		return os.Getenv(key)
	})

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config YAML: %w", err)
	}

	if err := cfg.resolveRuntimeTopologyReferences(); err != nil {
		return nil, fmt.Errorf("failed to resolve runtime topology references: %w", err)
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
