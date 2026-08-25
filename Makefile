.PHONY: all build install uninstall clean help test install-watchdog enable-watchdog disable-watchdog watchdog-status watchdog-run-once test-watchdog-mock install-log-retention enable-log-retention disable-log-retention log-retention-status log-retention-run-once test-log-retention install-resilience enable-resilience disable-resilience resilience-status resilience-run-once install-storage-configure storage-configure-inspect storage-configure-verify test-storage-configure install-storage-backup enable-storage-backup disable-storage-backup storage-backup-status storage-backup-check storage-backup-run-once storage-restore-check test-storage-backup

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
LDFLAGS=-ldflags "-X main.Version=$(VERSION) -X main.Commit=$(GIT_COMMIT) -X main.BuildDate=$(BUILD_TIME)"

# Go variables
GO?=go
GOFLAGS?=-v
# Keep repository-local runtime/cache artifacts under Tmp out of Go package
# discovery. These are deliberately inside the repo on Windows and may contain
# downloaded modules with their own go.mod files.
GO_PACKAGES=./application/... ./cmd/... ./domain/... ./infrastructure/... ./internal/... ./modules/... ./pkg/... ./test/... ./tools/...

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
WATCHDOG_SERVICE_SRC=$(CURDIR)/systemd/user/rencrow-watchdog.service
WATCHDOG_TIMER_SRC=$(CURDIR)/systemd/user/rencrow-watchdog.timer
LOG_ROTATE_SCRIPT_SRC=$(CURDIR)/scripts/rencrow_log_rotate.sh
LOG_ROTATE_SCRIPT_DST=$(RENCROW_SHARE_DIR)/scripts/rencrow_log_rotate.sh
LOG_ROTATE_SERVICE_SRC=$(CURDIR)/systemd/user/rencrow-log-rotate.service
LOG_ROTATE_TIMER_SRC=$(CURDIR)/systemd/user/rencrow-log-rotate.timer
PANIC_STACK_DROPIN_SRC=$(CURDIR)/systemd/user/rencrow.service.d/10-panic-stack.conf
PANIC_STACK_DROPIN_DIR=$(SYSTEMD_USER_DIR)/rencrow.service.d
RESILIENCE_SERVICE_SRC=$(CURDIR)/systemd/user/rencrow-resilience.service
RESILIENCE_TIMER_SRC=$(CURDIR)/systemd/user/rencrow-resilience.timer
RESILIENCE_DROPIN_SRC=$(CURDIR)/systemd/user/rencrow.service.d/20-resilience.conf
STORAGE_BACKUP_SCRIPT_SRC=$(CURDIR)/scripts/rencrow-storage-backup
STORAGE_BACKUP_SCRIPT_DST=$(INSTALL_BIN_DIR)/rencrow-storage-backup
STORAGE_CONFIGURE_SCRIPT_SRC=$(CURDIR)/scripts/rencrow-storage-configure
STORAGE_CONFIGURE_SCRIPT_DST=$(INSTALL_BIN_DIR)/rencrow-storage-configure
STORAGE_RESTORE_CHECK_SCRIPT_SRC=$(CURDIR)/scripts/rencrow-storage-restore-check
STORAGE_RESTORE_CHECK_SCRIPT_DST=$(INSTALL_BIN_DIR)/rencrow-storage-restore-check
STORAGE_BACKUP_SERVICE_SRC=$(CURDIR)/systemd/user/rencrow-storage-backup.service
STORAGE_BACKUP_TIMER_SRC=$(CURDIR)/systemd/user/rencrow-storage-backup.timer

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
	@$(GO) generate $(GO_PACKAGES)
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
	@mkdir -p $(RENCROW_SHARE_DIR)/prompts
	@mkdir -p $(RENCROW_SHARE_DIR)/config
	@cp -R prompts/. $(RENCROW_SHARE_DIR)/prompts/
	@cp config/durable-stores.json $(RENCROW_SHARE_DIR)/config/durable-stores.json
	@cp $(BUILD_DIR)/$(BINARY_NAME) $(INSTALL_BIN_DIR)/.$(BINARY_NAME).new
	@chmod +x $(INSTALL_BIN_DIR)/.$(BINARY_NAME).new
	@mv -f $(INSTALL_BIN_DIR)/.$(BINARY_NAME).new $(INSTALL_BIN_DIR)/$(BINARY_NAME)
	@echo "Installed binary to $(INSTALL_BIN_DIR)/$(BINARY_NAME)"
	@echo "Installed prompt assets to $(RENCROW_SHARE_DIR)/prompts"
	@echo "Installed durable store manifest to $(RENCROW_SHARE_DIR)/config/durable-stores.json"
	@echo "Installation complete!"
	@echo "Tip: run 'make install-log-retention enable-log-retention' to retain CORE logs for seven days."
	@echo "Tip: run 'make install-resilience enable-resilience' to enable restart and self-repair."
	@echo "Tip: run 'make install-watchdog enable-watchdog' to enable ops watchdog."

## install-watchdog: Install watchdog script and systemd --user units
install-watchdog:
	@echo "Installing watchdog script and systemd units..."
	@mkdir -p $(RENCROW_SHARE_DIR)/scripts
	@mkdir -p $(SYSTEMD_USER_DIR)
	@cp $(WATCHDOG_SCRIPT_SRC) $(WATCHDOG_SCRIPT_DST)
	@chmod +x $(WATCHDOG_SCRIPT_DST)
	@sed 's#%h/.local/share/rencrow/scripts/ops_watchdog.sh#$(WATCHDOG_SCRIPT_DST)#g' $(WATCHDOG_SERVICE_SRC) > $(SYSTEMD_USER_DIR)/rencrow-watchdog.service
	@cp $(WATCHDOG_TIMER_SRC) $(SYSTEMD_USER_DIR)/rencrow-watchdog.timer
	@systemctl --user daemon-reload
	@echo "Installed: $(WATCHDOG_SCRIPT_DST)"
	@echo "Installed: $(SYSTEMD_USER_DIR)/rencrow-watchdog.service"
	@echo "Installed: $(SYSTEMD_USER_DIR)/rencrow-watchdog.timer"

## install-log-retention: Install seven-day journal archives and full panic stack settings
install-log-retention:
	@echo "Installing log retention script and systemd units..."
	@mkdir -p $(RENCROW_SHARE_DIR)/scripts
	@mkdir -p $(SYSTEMD_USER_DIR)
	@mkdir -p $(PANIC_STACK_DROPIN_DIR)
	@cp $(LOG_ROTATE_SCRIPT_SRC) $(LOG_ROTATE_SCRIPT_DST)
	@chmod +x $(LOG_ROTATE_SCRIPT_DST)
	@cp $(LOG_ROTATE_SERVICE_SRC) $(SYSTEMD_USER_DIR)/rencrow-log-rotate.service
	@cp $(LOG_ROTATE_TIMER_SRC) $(SYSTEMD_USER_DIR)/rencrow-log-rotate.timer
	@cp $(PANIC_STACK_DROPIN_SRC) $(PANIC_STACK_DROPIN_DIR)/10-panic-stack.conf
	@systemctl --user daemon-reload
	@echo "Installed: $(LOG_ROTATE_SCRIPT_DST)"
	@echo "Installed: $(SYSTEMD_USER_DIR)/rencrow-log-rotate.service"
	@echo "Installed: $(SYSTEMD_USER_DIR)/rencrow-log-rotate.timer"
	@echo "Installed: $(PANIC_STACK_DROPIN_DIR)/10-panic-stack.conf"

## enable-log-retention: Enable hourly seven-day log archives
enable-log-retention:
	@systemctl --user daemon-reload
	@systemctl --user enable --now rencrow-log-rotate.timer
	@echo "RenCrow log retention timer enabled."

## disable-log-retention: Disable hourly log archives
disable-log-retention:
	@systemctl --user disable --now rencrow-log-rotate.timer || true
	@echo "RenCrow log retention timer disabled."

## log-retention-status: Show log retention timer/service status
log-retention-status:
	@systemctl --user status rencrow-log-rotate.timer --no-pager || true
	@systemctl --user status rencrow-log-rotate.service --no-pager || true

## log-retention-run-once: Archive RenCrow CORE journal immediately
log-retention-run-once:
	@systemctl --user start rencrow-log-rotate.service

## test-log-retention: Run log retention regression tests
test-log-retention:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-local.ps1 -Step log-retention
else
	@bash scripts/tests/log_retention_test.sh
endif

## install-resilience: Install CORE restart, incident ledger, and self-repair units
install-resilience: install
	@echo "Installing resilience systemd units..."
	@mkdir -p $(SYSTEMD_USER_DIR)
	@mkdir -p $(PANIC_STACK_DROPIN_DIR)
	@sed 's#@RENCROW_REPO_DIR@#$(CURDIR)#g' $(RESILIENCE_SERVICE_SRC) > $(SYSTEMD_USER_DIR)/rencrow-resilience.service
	@cp $(RESILIENCE_TIMER_SRC) $(SYSTEMD_USER_DIR)/rencrow-resilience.timer
	@cp $(RESILIENCE_DROPIN_SRC) $(PANIC_STACK_DROPIN_DIR)/20-resilience.conf
	@systemctl --user daemon-reload
	@echo "Installed: $(SYSTEMD_USER_DIR)/rencrow-resilience.service"
	@echo "Installed: $(SYSTEMD_USER_DIR)/rencrow-resilience.timer"
	@echo "Installed: $(PANIC_STACK_DROPIN_DIR)/20-resilience.conf"

## enable-resilience: Enable continuous CORE liveness/recovery checks
enable-resilience:
	@systemctl --user daemon-reload
	@systemctl --user enable --now rencrow-resilience.timer
	@echo "RenCrow resilience timer enabled."

## disable-resilience: Disable automatic liveness/recovery checks
disable-resilience:
	@systemctl --user disable --now rencrow-resilience.timer || true
	@echo "RenCrow resilience timer disabled."

## resilience-status: Show supervisor and incident ledger status
resilience-status:
	@systemctl --user status rencrow-resilience.timer --no-pager || true
	@systemctl --user status rencrow-resilience.service --no-pager || true
	@$(INSTALL_BIN_DIR)/rencrow resilience status

## resilience-run-once: Run one liveness/recovery reconciliation
resilience-run-once:
	@systemctl --user start rencrow-resilience.service

## install-storage-backup: Install config-driven CORE/Knowledge backup runner and timer
install-storage-backup: install
	@echo "Installing storage backup runner and systemd units..."
	@mkdir -p $(INSTALL_BIN_DIR) $(SYSTEMD_USER_DIR)
	@install -m 0755 $(STORAGE_BACKUP_SCRIPT_SRC) $(STORAGE_BACKUP_SCRIPT_DST)
	@install -m 0755 $(STORAGE_CONFIGURE_SCRIPT_SRC) $(STORAGE_CONFIGURE_SCRIPT_DST)
	@install -m 0755 $(STORAGE_RESTORE_CHECK_SCRIPT_SRC) $(STORAGE_RESTORE_CHECK_SCRIPT_DST)
	@install -m 0644 $(STORAGE_BACKUP_SERVICE_SRC) $(SYSTEMD_USER_DIR)/rencrow-storage-backup.service
	@install -m 0644 $(STORAGE_BACKUP_TIMER_SRC) $(SYSTEMD_USER_DIR)/rencrow-storage-backup.timer
	@systemctl --user daemon-reload
	@echo "Installed storage backup runtime."

install-storage-configure:
	@mkdir -p $(INSTALL_BIN_DIR)
	@install -m 0755 $(STORAGE_CONFIGURE_SCRIPT_SRC) $(STORAGE_CONFIGURE_SCRIPT_DST)
	@echo "Installed storage configure CLI: $(STORAGE_CONFIGURE_SCRIPT_DST)"

storage-configure-inspect:
	@$(STORAGE_CONFIGURE_SCRIPT_DST) inspect --json

storage-configure-verify:
	@$(STORAGE_CONFIGURE_SCRIPT_DST) verify --json

test-storage-configure:
ifeq ($(OS),Windows_NT)
	@echo "storage configure is an Ubuntu host operation; source contract runs in Linux CI"
else
	@bash scripts/tests/storage_configure_contract_test.sh
endif

enable-storage-backup:
	@systemctl --user enable --now rencrow-storage-backup.timer

disable-storage-backup:
	@systemctl --user disable --now rencrow-storage-backup.timer

storage-backup-status:
	@systemctl --user status rencrow-storage-backup.timer --no-pager

storage-backup-check:
	@$(STORAGE_BACKUP_SCRIPT_DST) check

storage-backup-run-once:
	@systemctl --user start rencrow-storage-backup.service

storage-restore-check:
	@test -n "$(SNAPSHOT_DIR)" || (echo "SNAPSHOT_DIR is required" >&2; exit 2)
	@$(STORAGE_RESTORE_CHECK_SCRIPT_DST) "$(SNAPSHOT_DIR)"

test-storage-backup:
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-local.ps1 -Step storage-backup
else
	@bash scripts/tests/storage_backup_contract_test.sh
	@bash scripts/tests/storage_configure_contract_test.sh
endif

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
ifeq ($(OS),Windows_NT)
	@powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-local.ps1 -Step watchdog
else
	@bash scripts/tests/watchdog_mock_test.sh
endif

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
	@$(GO) vet $(GO_PACKAGES)

## fmt: Format Go code
test:
	@$(GO) test $(GO_PACKAGES)

## fmt: Format Go code
fmt:
	@$(GO) fmt $(GO_PACKAGES)

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
