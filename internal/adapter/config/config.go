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

	// 先に YAML をパースし、scalar 値として環境変数を展開する。
	// Windows パスのバックスラッシュ等を YAML 構文として再解釈させない。
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("failed to parse config YAML: %w", err)
	}
	expandConfigEnvironment(&root)
	var cfg Config
	if err := root.Decode(&cfg); err != nil {
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

func expandConfigEnvironment(node *yaml.Node) {
	if node.Kind == yaml.ScalarNode && strings.Contains(node.Value, "${") {
		node.Value = os.Expand(node.Value, func(key string) string {
			if strings.HasPrefix(key, "module:") {
				return "${" + key + "}"
			}
			return os.Getenv(key)
		})
		// Decode 時に展開後の値を対象フィールドの型へ変換させる。
		node.Tag = ""
	}
	for _, child := range node.Content {
		expandConfigEnvironment(child)
	}
}
