package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenCrowServiceUnitIsPortableProductionDefinition(t *testing.T) {
	unit := readRepoText(t, "systemd", "user", "rencrow.service")
	mustContainAll(t, unit, []string{
		"Description=RenCrow CORE",
		"StartLimitIntervalSec=0",
		"WorkingDirectory=%h/.local/share/rencrow",
		"ExecStart=%h/.local/bin/rencrow run",
		"EnvironmentFile=%h/.rencrow/.env",
		"EnvironmentFile=-%h/.rencrow/llm_ops.env",
		"Environment=RENCROW_CONFIG=%h/.rencrow/config/core.yaml",
		"Environment=GOTRACEBACK=all",
		"Restart=always",
		"RestartSec=5",
		"StandardOutput=journal",
		"StandardError=journal",
		"LogRateLimitIntervalSec=0",
		"LogRateLimitBurst=0",
	})
	for _, forbidden := range []string{
		"/home/nyukimi",
		"append:",
		"ExecStart=%h/.local/bin/rencrow\n",
	} {
		if strings.Contains(unit, forbidden) {
			t.Fatalf("rencrow.service contains non-production wiring %q:\n%s", forbidden, unit)
		}
	}
}

func TestInstallScriptUsesServiceUnitSourceOfTruth(t *testing.T) {
	script := readRepoText(t, "install.sh")
	mustContainAll(t, script, []string{
		"install -m 0644 \"systemd/user/rencrow.service\" \"${SYSTEMD_USER_DIR}/rencrow.service\"",
		"systemd/user/rencrow.service.d/30-games-observer.conf",
	})
	if strings.Contains(script, "cat > \"$SYSTEMD_USER_DIR/rencrow.service\"") {
		t.Fatalf("install.sh must not inline-generate rencrow.service")
	}
}

func TestOpsDocsNameServiceUnitSourceOfTruth(t *testing.T) {
	doc := readRepoText(t, "docs", "09_運用ログ・panic保存仕様.md")
	mustContainAll(t, doc, []string{
		"`systemd/user/rencrow.service`",
		"`WorkingDirectory=%h/.local/share/rencrow`",
		"`ExecStart=%h/.local/bin/rencrow run`",
		"`RENCROW_CONFIG=%h/.rencrow/config/core.yaml`",
		"`StartLimitIntervalSec=0`",
		"`LogRateLimitIntervalSec=0`",
	})
}

func TestInstallEntrypointsCopyRuntimePromptAssets(t *testing.T) {
	makefile := readRepoText(t, "Makefile")
	installScript := readRepoText(t, "install.sh")
	mustContainAll(t, makefile, []string{
		"mkdir -p $(RENCROW_SHARE_DIR)/prompts",
		"cp -R prompts/. $(RENCROW_SHARE_DIR)/prompts/",
	})
	mustContainAll(t, installScript, []string{
		"mkdir -p \"${RENCROW_SHARE_DIR}/prompts\"",
		"cp -R prompts/. \"${RENCROW_SHARE_DIR}/prompts/\"",
	})
}

func TestConfigDocsNameCanonicalRuntimeLayout(t *testing.T) {
	doc := readRepoText(t, "docs", "05_設定リファレンス.md")
	mustContainAll(t, doc, []string{
		"`~/.rencrow/config/core.yaml`",
		"`~/.rencrow/config/llm.json`",
		"`~/.rencrow/config/stt.json`",
		"`~/.rencrow/config/tts.json`",
		"`~/.rencrow/config/vision.yaml`",
		"`~/.rencrow/config/image.json`",
		"`~/.rencrow/config/portal.json`",
		"`~/.rencrow/config/trade/learning-plan.json`",
		"`~/.config/systemd/user/`",
	})
}

func readRepoText(t *testing.T, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{"..", ".."}, parts...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func mustContainAll(t *testing.T, text string, wants []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}
