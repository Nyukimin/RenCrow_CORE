.PHONY: all build install uninstall clean help test install-watchdog enable-watchdog disable-watchdog watchdog-status watchdog-run-once test-watchdog-mock watchdog-kick install-data-scheduler enable-data-scheduler disable-data-scheduler data-scheduler-status rencrow-data-init rencrow-data-market rencrow-data-market-online rencrow-data-macro rencrow-data-macro-online rencrow-data-features rencrow-data-events rencrow-data-snapshot rencrow-data-validate rencrow-data-backtest rencrow-data-risk rencrow-data-decision rencrow-data-llm-report rencrow-data-audit-report rencrow-data-paper-trade rencrow-data-manual-stop rencrow-data-daily-refresh rencrow-data-weekly-research rencrow-data-test rencrow-data-e2e rencrow-data-backfill rencrow-data-check

# Build variables
BINARY_NAME=rencrow
BUILD_DIR=build
CMD_DIR=cmd/$(BINARY_NAME)
MAIN_GO=$(CMD_DIR)/main.go

# Version
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GIT_COMMIT=$(shell git rev-parse --short=8 HEAD 2>/dev/null || echo "dev")
BUILD_TIME=$(shell date +%FT%T%z)
GO_VERSION=$(shell $(GO) version | awk '{print $$3}')
LDFLAGS=-ldflags "-X main.version=$(VERSION) -X main.gitCommit=$(GIT_COMMIT) -X main.buildTime=$(BUILD_TIME) -X main.goVersion=$(GO_VERSION)"

# Go variables
GO?=go
GOFLAGS?=-v
PYTHON?=python3
PYTHONPATH?=rencrow-data/src
DATA_DB?=rencrow-data/data/rencrow.db
DATA_CONFIG_ROOT?=rencrow-data/config
DATA_ROOT?=rencrow-data
SNAPSHOT_DATE?=$(shell date -u +%F)
DATA_START_DATE?=
DATA_END_DATE?=
DATA_LOOKBACK_DAYS?=
DATA_STRATEGY?=weekly_etf_rotation_v1
DATA_RISK_CONFIG?=rencrow-data/config/risk_limits.yml
DATA_BACKTEST_OUTPUT_DIR?=rencrow-data/data/backtests
DATA_APPROVAL_DIR?=rencrow-data/approvals
DATA_REPORT_DIR?=rencrow-data/reports
DATA_APPROVAL_FILE?=$(DATA_APPROVAL_DIR)/latest.yml
DATA_PAPER_CAPITAL?=1000000
DATA_STOP_OPERATOR?=manual
DATA_STOP_REASON?=manual stop requested
DATA_STOP_NOTE?=

# Installation
INSTALL_PREFIX?=$(HOME)/.local
INSTALL_BIN_DIR=$(INSTALL_PREFIX)/bin
INSTALL_MAN_DIR=$(INSTALL_PREFIX)/share/man/man1

# Workspace and Skills
RENCROW_HOME?=$(HOME)/.rencrow
WORKSPACE_DIR?=$(RENCROW_HOME)/workspace
WORKSPACE_SKILLS_DIR=$(WORKSPACE_DIR)/skills
BUILTIN_SKILLS_DIR=$(CURDIR)/skills
SYSTEMD_USER_DIR=$(HOME)/.config/systemd/user
RENCROW_SHARE_DIR=$(INSTALL_PREFIX)/share/rencrow
WATCHDOG_SCRIPT_SRC=$(CURDIR)/scripts/ops_watchdog.sh
WATCHDOG_SCRIPT_DST=$(RENCROW_SHARE_DIR)/scripts/ops_watchdog.sh
WATCHDOG_KICK_SCRIPT_SRC=$(CURDIR)/scripts/ops_watchdog_kick.sh
WATCHDOG_KICK_SCRIPT_DST=$(RENCROW_SHARE_DIR)/scripts/ops_watchdog_kick.sh
WATCHDOG_SERVICE_SRC=$(CURDIR)/systemd/user/rencrow-watchdog.service
WATCHDOG_TIMER_SRC=$(CURDIR)/systemd/user/rencrow-watchdog.timer
DATA_SCHEDULER_SCRIPT_SRC=$(CURDIR)/scripts/rencrow_data_scheduler.sh
DATA_SCHEDULER_SCRIPT_DST=$(RENCROW_SHARE_DIR)/scripts/rencrow_data_scheduler.sh
DATA_DAILY_SERVICE_SRC=$(CURDIR)/systemd/user/rencrow-data-daily.service
DATA_DAILY_TIMER_SRC=$(CURDIR)/systemd/user/rencrow-data-daily.timer
DATA_WEEKLY_SERVICE_SRC=$(CURDIR)/systemd/user/rencrow-data-weekly.service
DATA_WEEKLY_TIMER_SRC=$(CURDIR)/systemd/user/rencrow-data-weekly.timer

# OS detection
UNAME_S:=$(shell uname -s)
UNAME_M:=$(shell uname -m)

# Platform-specific settings
ifeq ($(UNAME_S),Linux)
	PLATFORM=linux
	ifeq ($(UNAME_M),x86_64)
		ARCH=amd64
	else ifeq ($(UNAME_M),aarch64)
		ARCH=arm64
	else ifeq ($(UNAME_M),riscv64)
		ARCH=riscv64
	else
		ARCH=$(UNAME_M)
	endif
else ifeq ($(UNAME_S),Darwin)
	PLATFORM=darwin
	ifeq ($(UNAME_M),x86_64)
		ARCH=amd64
	else ifeq ($(UNAME_M),arm64)
		ARCH=arm64
	else
		ARCH=$(UNAME_M)
	endif
else
	PLATFORM=$(UNAME_S)
	ARCH=$(UNAME_M)
endif

BINARY_PATH=$(BUILD_DIR)/$(BINARY_NAME)-$(PLATFORM)-$(ARCH)

# Default target
all: build

## generate: Run generate
generate:
	@echo "Run generate..."
	@rm -r ./$(CMD_DIR)/workspace 2>/dev/null || true
	@$(GO) generate ./...
	@echo "Run generate complete"

## build: Build the rencrow binary for current platform
build: generate
	@echo "Building $(BINARY_NAME) for $(PLATFORM)/$(ARCH)..."
	@mkdir -p $(BUILD_DIR)
	@$(GO) build $(GOFLAGS) $(LDFLAGS) -o $(BINARY_PATH) ./$(CMD_DIR)
	@echo "Build complete: $(BINARY_PATH)"
	@ln -sf $(BINARY_NAME)-$(PLATFORM)-$(ARCH) $(BUILD_DIR)/$(BINARY_NAME)

## build-all: Build rencrow for all platforms
build-all: generate
	@echo "Building for multiple platforms..."
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 ./$(CMD_DIR)
	GOOS=linux GOARCH=arm64 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 ./$(CMD_DIR)
	GOOS=linux GOARCH=riscv64 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-riscv64 ./$(CMD_DIR)
	GOOS=darwin GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 ./$(CMD_DIR)
	GOOS=darwin GOARCH=arm64 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 ./$(CMD_DIR)
	GOOS=windows GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe ./$(CMD_DIR)
	GOOS=windows GOARCH=arm64 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-windows-arm64.exe ./$(CMD_DIR)
	@echo "All builds complete"

## install: Install rencrow to system and copy builtin skills
install: build
	@echo "Installing $(BINARY_NAME)..."
	@mkdir -p $(INSTALL_BIN_DIR)
	@cp $(BUILD_DIR)/$(BINARY_NAME) $(INSTALL_BIN_DIR)/$(BINARY_NAME)
	@chmod +x $(INSTALL_BIN_DIR)/$(BINARY_NAME)
	@echo "Installed binary to $(INSTALL_BIN_DIR)/$(BINARY_NAME)"
	@echo "Installation complete!"
	@echo "Tip: run 'make install-watchdog enable-watchdog' to enable ops watchdog."

## install-watchdog: Install watchdog script and systemd --user units
install-watchdog:
	@echo "Installing watchdog script and systemd units..."
	@mkdir -p $(RENCROW_SHARE_DIR)/scripts
	@mkdir -p $(SYSTEMD_USER_DIR)
	@cp $(WATCHDOG_SCRIPT_SRC) $(WATCHDOG_SCRIPT_DST)
	@chmod +x $(WATCHDOG_SCRIPT_DST)
	@cp $(WATCHDOG_KICK_SCRIPT_SRC) $(WATCHDOG_KICK_SCRIPT_DST)
	@chmod +x $(WATCHDOG_KICK_SCRIPT_DST)
	@sed 's#%h/.local/share/rencrow/scripts/ops_watchdog.sh#$(WATCHDOG_SCRIPT_DST)#g' $(WATCHDOG_SERVICE_SRC) > $(SYSTEMD_USER_DIR)/rencrow-watchdog.service
	@cp $(WATCHDOG_TIMER_SRC) $(SYSTEMD_USER_DIR)/rencrow-watchdog.timer
	@systemctl --user daemon-reload
	@echo "Installed: $(WATCHDOG_SCRIPT_DST)"
	@echo "Installed: $(SYSTEMD_USER_DIR)/rencrow-watchdog.service"
	@echo "Installed: $(SYSTEMD_USER_DIR)/rencrow-watchdog.timer"

## install-data-scheduler: Install daily and weekly data scheduler units
install-data-scheduler:
	@echo "Installing data scheduler script and systemd units..."
	@mkdir -p $(RENCROW_SHARE_DIR)/scripts
	@mkdir -p $(SYSTEMD_USER_DIR)
	@cp $(DATA_SCHEDULER_SCRIPT_SRC) $(DATA_SCHEDULER_SCRIPT_DST)
	@chmod +x $(DATA_SCHEDULER_SCRIPT_DST)
	@sed 's#@RENCROW_REPO_DIR@#$(CURDIR)#g' $(DATA_DAILY_SERVICE_SRC) > $(SYSTEMD_USER_DIR)/rencrow-data-daily.service
	@cp $(DATA_DAILY_TIMER_SRC) $(SYSTEMD_USER_DIR)/rencrow-data-daily.timer
	@sed 's#@RENCROW_REPO_DIR@#$(CURDIR)#g' $(DATA_WEEKLY_SERVICE_SRC) > $(SYSTEMD_USER_DIR)/rencrow-data-weekly.service
	@cp $(DATA_WEEKLY_TIMER_SRC) $(SYSTEMD_USER_DIR)/rencrow-data-weekly.timer
	@systemctl --user daemon-reload
	@echo "Installed: $(DATA_SCHEDULER_SCRIPT_DST)"
	@echo "Installed: $(SYSTEMD_USER_DIR)/rencrow-data-daily.service"
	@echo "Installed: $(SYSTEMD_USER_DIR)/rencrow-data-daily.timer"
	@echo "Installed: $(SYSTEMD_USER_DIR)/rencrow-data-weekly.service"
	@echo "Installed: $(SYSTEMD_USER_DIR)/rencrow-data-weekly.timer"

## enable-data-scheduler: Enable daily and weekly data timers
enable-data-scheduler:
	@systemctl --user daemon-reload
	@systemctl --user enable --now rencrow-data-daily.timer
	@systemctl --user enable --now rencrow-data-weekly.timer
	@echo "data scheduler timers enabled."

## disable-data-scheduler: Disable daily and weekly data timers
disable-data-scheduler:
	@systemctl --user disable --now rencrow-data-daily.timer || true
	@systemctl --user disable --now rencrow-data-weekly.timer || true
	@echo "data scheduler timers disabled."

## data-scheduler-status: Show data scheduler timer/service status
data-scheduler-status:
	@systemctl --user status rencrow-data-daily.timer --no-pager || true
	@systemctl --user status rencrow-data-weekly.timer --no-pager || true
	@systemctl --user status rencrow-data-daily.service --no-pager || true
	@systemctl --user status rencrow-data-weekly.service --no-pager || true

## enable-watchdog: Enable and start watchdog timer
enable-watchdog:
	@systemctl --user daemon-reload
	@systemctl --user enable --now rencrow-watchdog.timer
	@echo "watchdog timer enabled."

## disable-watchdog: Disable and stop watchdog timer
disable-watchdog:
	@systemctl --user disable --now rencrow-watchdog.timer || true
	@echo "watchdog timer disabled."

## watchdog-status: Show watchdog timer/service status
watchdog-status:
	@systemctl --user status rencrow-watchdog.timer --no-pager || true
	@systemctl --user status rencrow-watchdog.service --no-pager || true

## watchdog-run-once: Run watchdog script one time
watchdog-run-once:
	@bash "$(WATCHDOG_SCRIPT_DST)" once

## test-watchdog-mock: Run mock-based watchdog regression tests
test-watchdog-mock:
	@bash scripts/tests/watchdog_mock_test.sh

## rencrow-data-init: Initialize the stock/ETF learning foundation SQLite schema
rencrow-data-init:
	@PYTHONPATH=$(PYTHONPATH) $(PYTHON) rencrow-data/src/01_init_db.py --db $(DATA_DB) --config-root $(DATA_CONFIG_ROOT)

## rencrow-data-market: Ingest market fixtures / providers
rencrow-data-market:
	@PYTHONPATH=$(PYTHONPATH) $(PYTHON) rencrow-data/src/02_fetch_market.py --db $(DATA_DB) --config-root $(DATA_CONFIG_ROOT) --data-root $(DATA_ROOT)

## rencrow-data-market-online: Ingest market data from online providers for the widest available history
rencrow-data-market-online:
	@PYTHONPATH=$(PYTHONPATH) $(PYTHON) rencrow-data/src/02_fetch_market.py --db $(DATA_DB) --config-root $(DATA_CONFIG_ROOT) --data-root $(DATA_ROOT) --mode incremental $(if $(DATA_START_DATE),--start-date $(DATA_START_DATE),) $(if $(DATA_END_DATE),--end-date $(DATA_END_DATE),) $(if $(DATA_LOOKBACK_DAYS),--lookback-days $(DATA_LOOKBACK_DAYS),)

## rencrow-data-macro: Ingest macro and calendar fixtures / providers
rencrow-data-macro:
	@PYTHONPATH=$(PYTHONPATH) $(PYTHON) rencrow-data/src/03_fetch_macro.py --db $(DATA_DB) --config-root $(DATA_CONFIG_ROOT) --data-root $(DATA_ROOT)

## rencrow-data-macro-online: Ingest macro and calendar data from online providers for the widest available history
rencrow-data-macro-online:
	@PYTHONPATH=$(PYTHONPATH) $(PYTHON) rencrow-data/src/03_fetch_macro.py --db $(DATA_DB) --config-root $(DATA_CONFIG_ROOT) --data-root $(DATA_ROOT) --mode incremental $(if $(DATA_START_DATE),--start-date $(DATA_START_DATE),) $(if $(DATA_END_DATE),--end-date $(DATA_END_DATE),) $(if $(DATA_LOOKBACK_DAYS),--lookback-days $(DATA_LOOKBACK_DAYS),)

## rencrow-data-features: Build weekly features from raw inputs
rencrow-data-features:
	@PYTHONPATH=$(PYTHONPATH) $(PYTHON) rencrow-data/src/04_build_features.py --db $(DATA_DB)

## rencrow-data-events: Detect macro / market / data safety events
rencrow-data-events:
	@PYTHONPATH=$(PYTHONPATH) $(PYTHON) rencrow-data/src/05_detect_events.py --db $(DATA_DB)

## rencrow-data-snapshot: Freeze the weekly snapshot archive
rencrow-data-snapshot:
	@PYTHONPATH=$(PYTHONPATH) $(PYTHON) rencrow-data/src/06_make_snapshot.py --db $(DATA_DB) --output-dir rencrow-data/data/snapshots --snapshot-date $(SNAPSHOT_DATE)

## rencrow-data-validate: Validate current data quality and fetch status
rencrow-data-validate:
	@PYTHONPATH=$(PYTHONPATH) $(PYTHON) rencrow-data/src/08_validate_data.py --db $(DATA_DB) --as-of $(SNAPSHOT_DATE); \
	status=$$?; \
	if [ $$status -eq 0 ] || [ $$status -eq 2 ] || [ $$status -eq 3 ]; then exit 0; fi; \
	exit $$status

## rencrow-data-backtest: Run weekly ETF rotation backtest for the latest snapshot
rencrow-data-backtest:
	@PYTHONPATH=$(PYTHONPATH) $(PYTHON) rencrow-data/src/09_backtest_weekly_rotation.py --db $(DATA_DB) --snapshot latest --strategy $(DATA_STRATEGY) --tax-mode approx_jp_taxable --walk-forward --output-dir $(DATA_BACKTEST_OUTPUT_DIR)

## rencrow-data-risk: Run risk checks for the latest snapshot and strategy
rencrow-data-risk:
	@PYTHONPATH=$(PYTHONPATH) $(PYTHON) rencrow-data/src/10_risk_check.py --db $(DATA_DB) --snapshot latest --strategy $(DATA_STRATEGY) --risk-config $(DATA_RISK_CONFIG); \
	status=$$?; \
	if [ $$status -eq 0 ] || [ $$status -eq 3 ]; then exit 0; fi; \
	exit $$status

## rencrow-data-decision: Generate a human-approved weekly decision candidate
rencrow-data-decision:
	@PYTHONPATH=$(PYTHONPATH) $(PYTHON) rencrow-data/src/11_generate_decision.py --db $(DATA_DB) --snapshot latest --strategy $(DATA_STRATEGY) --output-dir $(DATA_APPROVAL_DIR)

## rencrow-data-llm-report: Generate the local deterministic weekly report
rencrow-data-llm-report:
	@PYTHONPATH=$(PYTHONPATH) $(PYTHON) rencrow-data/src/13_llm_report.py --db $(DATA_DB) --snapshot latest --decision latest --output-dir $(DATA_REPORT_DIR)

## rencrow-data-audit-report: Generate the weekly audit report and paper gate status
rencrow-data-audit-report:
	@PYTHONPATH=$(PYTHONPATH) $(PYTHON) rencrow-data/src/14_audit_report.py --db $(DATA_DB) --snapshot latest --decision latest --paper-latest --output-dir $(DATA_REPORT_DIR)

## rencrow-data-paper-trade: Record a paper trade only after explicit human approval
rencrow-data-paper-trade:
	@PYTHONPATH=$(PYTHONPATH) $(PYTHON) rencrow-data/src/12_paper_trade.py --db $(DATA_DB) --decision latest --approval-file $(DATA_APPROVAL_FILE) --capital $(DATA_PAPER_CAPITAL)

## rencrow-data-manual-stop: Record a manual kill switch event before risk/decision
rencrow-data-manual-stop:
	@PYTHONPATH=$(PYTHONPATH) $(PYTHON) rencrow-data/src/15_manual_stop.py --db $(DATA_DB) --operator "$(DATA_STOP_OPERATOR)" --reason "$(DATA_STOP_REASON)" --note "$(DATA_STOP_NOTE)"

## rencrow-data-daily-refresh: Run the standard daily market, macro, and validation flow
rencrow-data-daily-refresh:
	@$(MAKE) rencrow-data-init
	@$(MAKE) rencrow-data-market-online
	@$(MAKE) rencrow-data-macro-online
	@$(MAKE) rencrow-data-validate SNAPSHOT_DATE=$(SNAPSHOT_DATE)

## rencrow-data-weekly-research: Run snapshot, validation, backtest, risk, decision, report, and audit
rencrow-data-weekly-research:
	@$(MAKE) rencrow-data-features
	@$(MAKE) rencrow-data-events
	@$(MAKE) rencrow-data-snapshot
	@$(MAKE) rencrow-data-validate
	@$(MAKE) rencrow-data-backtest
	@$(MAKE) rencrow-data-risk
	@$(MAKE) rencrow-data-decision
	@$(MAKE) rencrow-data-llm-report
	@$(MAKE) rencrow-data-audit-report

## rencrow-data-test: Run the Python foundation unit tests
rencrow-data-test:
	@PYTHONPATH=$(PYTHONPATH) $(PYTHON) -m unittest discover -s rencrow-data/tests -p 'test_*.py' -v

## rencrow-data-e2e: Run the full offline ingest -> feature -> event -> snapshot flow
rencrow-data-e2e: rencrow-data-test
	@$(MAKE) rencrow-data-init
	@$(MAKE) rencrow-data-market
	@$(MAKE) rencrow-data-macro
	@$(MAKE) rencrow-data-weekly-research

## rencrow-data-check: Run tests and the full offline E2E flow
rencrow-data-check: rencrow-data-e2e

## rencrow-data-backfill: Backfill online market and macro history, then refresh feature/event/snapshot outputs
rencrow-data-backfill:
	@$(MAKE) rencrow-data-init
	@$(MAKE) rencrow-data-market-online
	@$(MAKE) rencrow-data-macro-online
	@$(MAKE) rencrow-data-weekly-research

## watchdog-kick: Obsolete; use make watchdog-run-once for Viewer Serve recovery
watchdog-kick:
	@bash "$(WATCHDOG_KICK_SCRIPT_DST)" "$(ACTION)" "$(SOURCE)" "$(TOKEN)"

## uninstall: Remove rencrow from system
uninstall:
	@echo "Uninstalling $(BINARY_NAME)..."
	@rm -f $(INSTALL_BIN_DIR)/$(BINARY_NAME)
	@echo "Removed binary from $(INSTALL_BIN_DIR)/$(BINARY_NAME)"
	@echo "Note: Only the executable file has been deleted."
	@echo "If you need to delete all configurations (config.json, workspace, etc.), run 'make uninstall-all'"

## uninstall-all: Remove rencrow and all data
uninstall-all:
	@echo "Removing workspace and skills..."
	@rm -rf $(RENCROW_HOME)
	@echo "Removed workspace: $(RENCROW_HOME)"
	@echo "Complete uninstallation done!"

## clean: Remove build artifacts
clean:
	@echo "Cleaning build artifacts..."
	@rm -rf $(BUILD_DIR)
	@echo "Clean complete"

## vet: Run go vet for static analysis
vet:
	@$(GO) vet ./...

## fmt: Format Go code
test:
	@$(GO) test ./...

## fmt: Format Go code
fmt:
	@$(GO) fmt ./...

## deps: Download dependencies
deps:
	@$(GO) mod download
	@$(GO) mod verify

## update-deps: Update dependencies
update-deps:
	@$(GO) get -u ./...
	@$(GO) mod tidy

## check: Run vet, fmt, and verify dependencies
check: deps fmt vet test

## run: Build and run rencrow
run: build
	@$(BUILD_DIR)/$(BINARY_NAME) $(ARGS)

## help: Show this help message
help:
	@echo "rencrow Makefile"
	@echo ""
	@echo "Usage:"
	@echo "  make [target]"
	@echo ""
	@echo "Targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'
	@echo ""
	@echo "Examples:"
	@echo "  make build              # Build for current platform"
	@echo "  make install            # Install to ~/.local/bin"
	@echo "  make uninstall          # Remove from /usr/local/bin"
	@echo "  make install-skills     # Install skills to workspace"
	@echo ""
	@echo "Environment Variables:"
	@echo "  INSTALL_PREFIX          # Installation prefix (default: ~/.local)"
	@echo "  WORKSPACE_DIR           # Workspace directory (default: ~/.rencrow/workspace)"
	@echo "  VERSION                 # Version string (default: git describe)"
	@echo ""
	@echo "Current Configuration:"
	@echo "  Platform: $(PLATFORM)/$(ARCH)"
	@echo "  Binary: $(BINARY_PATH)"
	@echo "  Install Prefix: $(INSTALL_PREFIX)"
	@echo "  Workspace: $(WORKSPACE_DIR)"
