package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	glossarypersistence "github.com/Nyukimin/RenCrow_CORE/internal/glossary/infrastructure/persistence"
	advisorpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/advisor"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/archivesqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	dcipersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/dci"
	durablestorepersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/durablestore"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	knowledgememorypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/knowledgememory"
	memorypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/memory"
	sandboxpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/sandbox"
	sessionstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
	skillgovernancepersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/skillgovernance"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/storagehost"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	toolregistrypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/toolregistry"
	verificationpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/verification"
	_ "modernc.org/sqlite"
)

const storageHostShutdownTimeout = 5 * time.Second

type storageHostEventStore interface {
	storagehost.EventGroupOwner
	Close() error
}

type storageHostL1Store interface {
	storagehost.L1GroupOwner
	storagehost.TurnGroupOwner
	Close() error
}

type storageHostTaskStore interface {
	storagehost.TaskGroupOwner
	Close() error
}

type storageHostArchiveStore interface {
	storagehost.ArchiveGroupRecoveryOwner
	Close() error
}

type storageHostGlossaryStore interface {
	storagehost.GlossaryGroupOwner
	Close() error
}

type storageHostMovieCatalogStore interface {
	storagehost.MovieCatalogGroupOwner
	Close() error
}

type storageHostHobbyGraphStore interface {
	storagehost.HobbyGraphGroupOwner
	Close() error
}

type storageHostDurableWorkflowStore interface {
	storagehost.DurableStoreWorkflowGroupOwner
	Close() error
}

type storageHostToolRegistryStore interface {
	storagehost.ToolRegistryGroupOwner
	Close() error
}

type storageHostAdvisorStore interface {
	storagehost.AdvisorGroupOwner
	Close() error
}

type storageHostSandboxStore interface {
	storagehost.SandboxGroupOwner
	Close() error
}

type storageHostDCIStore interface {
	storagehost.DCIGroupOwner
	Close() error
}

type storageHostSkillGovernanceStore interface {
	storagehost.SkillGovernanceGroupOwner
	Close() error
}

type storageHostKnowledgeMemoryStore interface {
	storagehost.KnowledgeMemoryGroupOwner
	Close() error
}

type storageHostServeDeps struct {
	ReadToken                    func(string) (string, error)
	NewHandler                   func(storagehost.HandlerConfig) (*storagehost.Handler, error)
	OpenEventStore               func(string) (storageHostEventStore, error)
	OpenArchiveStore             func(string) (storageHostArchiveStore, error)
	OpenSessionStore             func(string) (storagehost.SessionGroupOwner, error)
	OpenL1Store                  func(string) (storageHostL1Store, error)
	OpenTaskStore                func(string) (storageHostTaskStore, error)
	OpenOperationMemoryStore     func(string) (storagehost.OperationMemoryGroupOwner, error)
	OpenGlossaryStore            func(string) (storageHostGlossaryStore, error)
	OpenMovieCatalogStore        func(string) (storageHostMovieCatalogStore, error)
	OpenHobbyGraphStore          func(string) (storageHostHobbyGraphStore, error)
	OpenDurableWorkflowStore     func(string) (storageHostDurableWorkflowStore, error)
	OpenVerificationReportStore  func(string) (storagehost.VerificationReportGroupOwner, error)
	OpenToolRegistryStore        func(string) (storageHostToolRegistryStore, error)
	OpenAdvisorStore             func(string) (storageHostAdvisorStore, error)
	OpenSandboxStore             func(string) (storageHostSandboxStore, error)
	OpenDCIStore                 func(string) (storageHostDCIStore, error)
	OpenSkillGovernanceStore     func(string) (storageHostSkillGovernanceStore, error)
	OpenKnowledgeMemoryStore     func(string) (storageHostKnowledgeMemoryStore, error)
	RegisterEventGroup           func(*storagehost.Handler, storagehost.EventGroupOwner) error
	RegisterArchiveGroup         func(*storagehost.Handler, storagehost.ArchiveGroupOwner) error
	RegisterSessionGroup         func(*storagehost.Handler, storagehost.SessionGroupOwner) error
	RegisterL1Group              func(*storagehost.Handler, storagehost.L1GroupOwner, string) error
	RegisterTurnGroup            func(*storagehost.Handler, storagehost.TurnGroupOwner) error
	RegisterTaskGroup            func(*storagehost.Handler, storagehost.TaskGroupOwner) error
	RegisterOperationMemoryGroup func(*storagehost.Handler, storagehost.OperationMemoryGroupOwner) error
	RegisterUserMemoryGroup      func(*storagehost.Handler, storagehost.UserMemoryGroupOwner) error
	RegisterGlossaryGroup        func(*storagehost.Handler, storagehost.GlossaryGroupOwner) error
	RegisterMovieCatalogGroup    func(*storagehost.Handler, storagehost.MovieCatalogGroupOwner) error
	RegisterHobbyGraphGroup      func(*storagehost.Handler, storagehost.HobbyGraphGroupOwner) error
	RegisterDurableWorkflowGroup func(*storagehost.Handler, storagehost.DurableStoreWorkflowGroupOwner) error
	RegisterVerificationGroup    func(*storagehost.Handler, storagehost.VerificationReportGroupOwner) error
	RegisterToolRegistryGroup    func(*storagehost.Handler, storagehost.ToolRegistryGroupOwner) error
	RegisterAdvisorGroup         func(*storagehost.Handler, storagehost.AdvisorGroupOwner) error
	RegisterSandboxGroup         func(*storagehost.Handler, storagehost.SandboxGroupOwner) error
	RegisterDCIGroup             func(*storagehost.Handler, storagehost.DCIGroupOwner) error
	RegisterSkillGovernanceGroup func(*storagehost.Handler, storagehost.SkillGovernanceGroupOwner) error
	RegisterKnowledgeMemoryGroup func(*storagehost.Handler, storagehost.KnowledgeMemoryGroupOwner) error
	Listen                       func(string, string) (net.Listener, error)
	Serve                        func(context.Context, net.Listener, http.Handler) error
}

type storageHostCLIDeps struct {
	LoadConfig func(string) (*config.Config, error)
	ConfigPath func() string
	Context    context.Context
	Serve      storageHostServeDeps
}

func cmdStorageHost() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := runStorageHostCommand(os.Args[2:], storageHostCLIDeps{
		LoadConfig: config.LoadConfig,
		ConfigPath: getConfigPath,
		Context:    ctx,
		Serve:      defaultStorageHostServeDeps(),
	}, os.Stdout, os.Stderr)
	stop()
	if code != 0 {
		os.Exit(code)
	}
}

func defaultStorageHostServeDeps() storageHostServeDeps {
	return storageHostServeDeps{
		ReadToken:  config.ReadStorageHostBearerToken,
		NewHandler: storagehost.NewHandler,
		OpenEventStore: func(path string) (storageHostEventStore, error) {
			return eventstore.NewSQLiteStore(path)
		},
		OpenArchiveStore: func(path string) (storageHostArchiveStore, error) {
			return archivesqlite.NewArchiveSQLiteStore(path)
		},
		OpenSessionStore: func(path string) (storagehost.SessionGroupOwner, error) {
			return sessionstore.NewJSONSessionRepository(path), nil
		},
		OpenL1Store: func(path string) (storageHostL1Store, error) {
			return l1sqlite.NewL1SQLiteStore(path)
		},
		OpenTaskStore: func(path string) (storageHostTaskStore, error) {
			return taskpersistence.NewJSONLStore(path)
		},
		OpenOperationMemoryStore: func(path string) (storagehost.OperationMemoryGroupOwner, error) {
			return memorypersistence.OpenRecoverableFileStoreAt(path)
		},
		OpenGlossaryStore: func(path string) (storageHostGlossaryStore, error) {
			return glossarypersistence.NewSQLiteGlossaryRepository(path)
		},
		OpenMovieCatalogStore: func(path string) (storageHostMovieCatalogStore, error) {
			return openStorageHostMovieCatalogStore(path)
		},
		OpenHobbyGraphStore: func(path string) (storageHostHobbyGraphStore, error) {
			return openStorageHostHobbyGraphStore(path)
		},
		OpenDurableWorkflowStore: func(path string) (storageHostDurableWorkflowStore, error) {
			return durablestorepersistence.NewSQLiteStore(path)
		},
		OpenVerificationReportStore: func(path string) (storagehost.VerificationReportGroupOwner, error) {
			return verificationpersistence.NewJSONLReportStore(path)
		},
		OpenToolRegistryStore: func(path string) (storageHostToolRegistryStore, error) {
			return toolregistrypersistence.NewSQLiteToolRegistryStore(path)
		},
		OpenAdvisorStore: func(path string) (storageHostAdvisorStore, error) {
			return advisorpersistence.NewSQLiteStore(path)
		},
		OpenSandboxStore: func(path string) (storageHostSandboxStore, error) {
			return sandboxpersistence.NewSQLiteStore(path)
		},
		OpenDCIStore: func(path string) (storageHostDCIStore, error) {
			return dcipersistence.NewSQLiteStore(path)
		},
		OpenSkillGovernanceStore: func(path string) (storageHostSkillGovernanceStore, error) {
			return skillgovernancepersistence.NewSQLiteStore(path)
		},
		OpenKnowledgeMemoryStore: func(path string) (storageHostKnowledgeMemoryStore, error) {
			return knowledgememorypersistence.NewSQLiteStore(path)
		},
		RegisterEventGroup:           storagehost.RegisterEventGroup,
		RegisterArchiveGroup:         storagehost.RegisterArchiveGroup,
		RegisterSessionGroup:         storagehost.RegisterSessionGroup,
		RegisterL1Group:              storagehost.RegisterL1Group,
		RegisterTurnGroup:            storagehost.RegisterTurnGroup,
		RegisterTaskGroup:            storagehost.RegisterTaskGroup,
		RegisterOperationMemoryGroup: storagehost.RegisterOperationMemoryGroup,
		RegisterUserMemoryGroup:      storagehost.RegisterUserMemoryGroup,
		RegisterGlossaryGroup:        storagehost.RegisterGlossaryGroup,
		RegisterMovieCatalogGroup:    storagehost.RegisterMovieCatalogGroup,
		RegisterHobbyGraphGroup:      storagehost.RegisterHobbyGraphGroup,
		RegisterDurableWorkflowGroup: storagehost.RegisterDurableStoreWorkflowGroup,
		RegisterVerificationGroup:    storagehost.RegisterVerificationReportGroup,
		RegisterToolRegistryGroup:    storagehost.RegisterToolRegistryGroup,
		RegisterAdvisorGroup:         storagehost.RegisterAdvisorGroup,
		RegisterSandboxGroup:         storagehost.RegisterSandboxGroup,
		RegisterDCIGroup:             storagehost.RegisterDCIGroup,
		RegisterSkillGovernanceGroup: storagehost.RegisterSkillGovernanceGroup,
		RegisterKnowledgeMemoryGroup: storagehost.RegisterKnowledgeMemoryGroup,
		Listen:                       net.Listen,
		Serve:                        serveStorageHostHTTP,
	}
}

func runStorageHostCommand(args []string, deps storageHostCLIDeps, out, errOut io.Writer) int {
	if len(args) == 0 || hasFlag(args, "--help") || hasFlag(args, "-h") {
		writeStorageHostHelp(out)
		return 0
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "help":
		writeStorageHostHelp(out)
		return 0
	case "serve":
		if len(args) > 1 {
			fmt.Fprintf(errOut, "unexpected storage-host serve argument: %s\n", args[1])
			return 2
		}
		loadConfig := deps.LoadConfig
		if loadConfig == nil {
			loadConfig = config.LoadConfig
		}
		configPath := deps.ConfigPath
		if configPath == nil {
			configPath = getConfigPath
		}
		cfg, err := loadConfig(configPath())
		if err != nil {
			fmt.Fprintf(errOut, "failed to load config: %v\n", err)
			return 1
		}
		ctx := deps.Context
		if ctx == nil {
			ctx = context.Background()
		}
		if err := runStorageHostServe(ctx, cfg, deps.Serve); err != nil {
			fmt.Fprintf(errOut, "storage-host serve failed: %v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(errOut, "unknown storage-host command: %s\n", args[0])
		return 2
	}
}

func writeStorageHostHelp(out io.Writer) {
	fmt.Fprintln(out, "Usage: rencrow storage-host <command>")
	fmt.Fprintln(out, "Commands:")
	fmt.Fprintln(out, "  serve    Serve registered CORE-owned storage operations over authenticated RPC")
	fmt.Fprintln(out, "  help     Show this help message")
}

func runStorageHostServe(ctx context.Context, cfg *config.Config, deps storageHostServeDeps) (resultErr error) {
	if cfg == nil {
		return errors.New("storage-host config is required")
	}
	mode := strings.TrimSpace(cfg.Storage.Host.Mode)
	if mode != "" && mode != config.StorageHostModeLocal {
		return errors.New("storage-host serve requires storage.host.mode=local on the storage host")
	}
	listenAddress, err := parseStorageHostListenAddress(cfg.Storage.Host.Listen)
	if err != nil {
		return err
	}
	tokenPath := strings.TrimSpace(cfg.Storage.Host.TokenFile)
	if tokenPath == "" {
		return errors.New("storage.host.token_file is required for storage-host serve")
	}
	if !filepath.IsAbs(tokenPath) {
		return errors.New("storage.host.token_file must be an absolute path")
	}
	eventStorePath := strings.TrimSpace(cfg.Storage.Databases.EventStore)
	if eventStorePath == "" || !filepath.IsAbs(eventStorePath) {
		return errors.New("storage.databases.event_store must be an absolute local path for storage-host serve")
	}
	archiveStorePath := strings.TrimSpace(cfg.Storage.Databases.ConversationArchive)
	if archiveStorePath == "" || !filepath.IsAbs(archiveStorePath) {
		return errors.New("storage.databases.conversation_archive must be an absolute local path for storage-host serve")
	}
	sessionStoreDir := strings.TrimSpace(cfg.Session.StorageDir)
	if sessionStoreDir == "" || !filepath.IsAbs(sessionStoreDir) {
		return errors.New("session.storage_dir must be an absolute local path for storage-host serve")
	}
	conversationL1Path := strings.TrimSpace(cfg.Storage.Databases.ConversationL1)
	if conversationL1Path == "" || !filepath.IsAbs(conversationL1Path) {
		return errors.New("storage.databases.conversation_l1 must be an absolute local path for storage-host serve")
	}
	operationMemoryDir := strings.TrimSpace(cfg.Storage.Memory.OperationMemoryDir)
	if operationMemoryDir == "" || !filepath.IsAbs(operationMemoryDir) {
		return errors.New("storage.memory.operation_memory_dir must be an absolute local path for storage-host serve")
	}
	taskStorePath := defaultTaskStorePath(cfg.WorkspaceDir)
	if strings.TrimSpace(taskStorePath) == "" || !filepath.IsAbs(taskStorePath) {
		return errors.New("workspace_dir must resolve to an absolute local Task store path for storage-host serve")
	}
	toolRegistryPath := strings.TrimSpace(cfg.Storage.Databases.ToolRegistry)
	advisorPath := strings.TrimSpace(cfg.Storage.Databases.Advisor)
	sandboxPath := strings.TrimSpace(cfg.Storage.Databases.Sandbox)
	dciPath := strings.TrimSpace(cfg.Storage.Databases.DCI)
	skillGovernancePath := strings.TrimSpace(cfg.Storage.Databases.SkillGovernance)
	glossaryPath := strings.TrimSpace(cfg.Storage.Databases.Glossary)
	movieCatalogPath := strings.TrimSpace(cfg.Storage.Databases.MovieCatalog)
	hobbyGraphPath := strings.TrimSpace(cfg.Storage.Databases.HobbyGraph)
	durableWorkflowPath := strings.TrimSpace(cfg.Storage.Databases.DurableStoreWorkflow)
	knowledgeMemoryPath := strings.TrimSpace(cfg.Storage.Databases.KnowledgeMemory)
	verificationReportPath := strings.TrimSpace(cfg.Verification.ReportPath)
	for _, ownerPath := range []struct{ key, value string }{
		{"storage.databases.tool_registry", toolRegistryPath},
		{"storage.databases.advisor", advisorPath},
		{"storage.databases.sandbox", sandboxPath},
		{"storage.databases.dci", dciPath},
		{"storage.databases.skill_governance", skillGovernancePath},
		{"storage.databases.glossary", glossaryPath},
		{"storage.databases.movie_catalog", movieCatalogPath},
		{"storage.databases.hobby_graph", hobbyGraphPath},
		{"storage.databases.durable_store_workflow", durableWorkflowPath},
		{"storage.databases.knowledge_memory", knowledgeMemoryPath},
		{"verification.report_path", verificationReportPath},
	} {
		if ownerPath.value == "" || !filepath.IsAbs(ownerPath.value) {
			return fmt.Errorf("%s must be an absolute local path for storage-host serve", ownerPath.key)
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return nil
	}
	if deps.ReadToken == nil || deps.NewHandler == nil || deps.OpenEventStore == nil || deps.OpenArchiveStore == nil || deps.OpenSessionStore == nil ||
		deps.OpenL1Store == nil || deps.OpenTaskStore == nil || deps.OpenOperationMemoryStore == nil ||
		deps.OpenGlossaryStore == nil || deps.OpenMovieCatalogStore == nil || deps.OpenHobbyGraphStore == nil ||
		deps.OpenDurableWorkflowStore == nil || deps.OpenVerificationReportStore == nil || deps.OpenToolRegistryStore == nil ||
		deps.OpenAdvisorStore == nil || deps.OpenSandboxStore == nil || deps.OpenDCIStore == nil ||
		deps.OpenSkillGovernanceStore == nil || deps.OpenKnowledgeMemoryStore == nil ||
		deps.RegisterEventGroup == nil || deps.RegisterArchiveGroup == nil || deps.RegisterSessionGroup == nil || deps.RegisterL1Group == nil ||
		deps.RegisterTurnGroup == nil || deps.RegisterTaskGroup == nil || deps.RegisterOperationMemoryGroup == nil ||
		deps.RegisterUserMemoryGroup == nil || deps.RegisterGlossaryGroup == nil || deps.RegisterMovieCatalogGroup == nil ||
		deps.RegisterHobbyGraphGroup == nil || deps.RegisterDurableWorkflowGroup == nil || deps.RegisterVerificationGroup == nil ||
		deps.RegisterToolRegistryGroup == nil || deps.RegisterAdvisorGroup == nil || deps.RegisterSandboxGroup == nil ||
		deps.RegisterDCIGroup == nil || deps.RegisterSkillGovernanceGroup == nil || deps.RegisterKnowledgeMemoryGroup == nil || deps.Listen == nil {
		return errors.New("storage-host serve dependencies are incomplete")
	}
	token, err := deps.ReadToken(tokenPath)
	if err != nil {
		return fmt.Errorf("read storage-host token: %w", err)
	}
	if strings.TrimSpace(token) == "" {
		return errors.New("storage.host.token_file must contain a nonempty bearer token")
	}

	journalDir := filepath.Join(operationMemoryDir, "storage-host")
	handler, err := deps.NewHandler(storagehost.HandlerConfig{Token: token, JournalDir: journalDir})
	if err != nil {
		return fmt.Errorf("open storage-host journal: %w", err)
	}
	defer func() {
		if err := handler.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close storage-host journal: %w", err))
		}
	}()

	operationMemoryOwner, err := deps.OpenOperationMemoryStore(operationMemoryDir)
	if err != nil {
		return fmt.Errorf("open recoverable operation memory owner: %w", err)
	}
	if operationMemoryOwner == nil {
		return errors.New("open recoverable operation memory owner: constructor returned nil")
	}
	if closer, ok := operationMemoryOwner.(interface{ Close() error }); ok {
		defer func() {
			if err := closer.Close(); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close operation memory owner: %w", err))
			}
		}()
	}
	if err := os.MkdirAll(filepath.Dir(archiveStorePath), 0o755); err != nil {
		return fmt.Errorf("create canonical Conversation Archive owner directory: %w", err)
	}
	archiveOwner, err := deps.OpenArchiveStore(archiveStorePath)
	if archiveOwner != nil {
		defer func() {
			if err := archiveOwner.Close(); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close canonical Conversation Archive SQLite owner: %w", err))
			}
		}()
	}
	if err != nil {
		return fmt.Errorf("open canonical Conversation Archive SQLite owner: %w", err)
	}
	if archiveOwner == nil {
		return errors.New("open canonical Conversation Archive SQLite owner: constructor returned nil")
	}

	eventOwner, err := deps.OpenEventStore(eventStorePath)
	if eventOwner != nil {
		defer func() {
			if err := eventOwner.Close(); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close canonical Event SQLite owner: %w", err))
			}
		}()
	}
	if err != nil {
		return fmt.Errorf("open canonical Event SQLite owner: %w", err)
	}
	if eventOwner == nil {
		return errors.New("open canonical Event SQLite owner: constructor returned nil")
	}

	if err := os.MkdirAll(sessionStoreDir, 0o755); err != nil {
		return fmt.Errorf("create canonical Session owner directory: %w", err)
	}
	sessionOwner, err := deps.OpenSessionStore(sessionStoreDir)
	if err != nil {
		return fmt.Errorf("open canonical Session JSON owner: %w", err)
	}
	if sessionOwner == nil {
		return errors.New("open canonical Session JSON owner: constructor returned nil")
	}

	l1Owner, err := deps.OpenL1Store(conversationL1Path)
	if l1Owner != nil {
		defer func() {
			if err := l1Owner.Close(); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close canonical Conversation L1 SQLite owner: %w", err))
			}
		}()
	}
	if err != nil {
		return fmt.Errorf("open canonical Conversation L1 SQLite owner: %w", err)
	}
	if l1Owner == nil {
		return errors.New("open canonical Conversation L1 SQLite owner: constructor returned nil")
	}

	taskOwner, err := deps.OpenTaskStore(taskStorePath)
	if taskOwner != nil {
		defer func() {
			if err := taskOwner.Close(); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close canonical Task JSONL owner: %w", err))
			}
		}()
	}
	if err != nil {
		return fmt.Errorf("open canonical Task JSONL owner: %w", err)
	}
	if taskOwner == nil {
		return errors.New("open canonical Task JSONL owner: constructor returned nil")
	}
	glossaryOwner, err := deps.OpenGlossaryStore(glossaryPath)
	if glossaryOwner != nil {
		defer func() {
			if err := glossaryOwner.Close(); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close canonical Glossary SQLite owner: %w", err))
			}
		}()
	}
	if err != nil {
		return fmt.Errorf("open canonical Glossary SQLite owner: %w", err)
	}
	if glossaryOwner == nil {
		return errors.New("open canonical Glossary SQLite owner: constructor returned nil")
	}
	movieOwner, err := deps.OpenMovieCatalogStore(movieCatalogPath)
	if movieOwner != nil {
		defer func() {
			if err := movieOwner.Close(); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close canonical Movie Catalog SQLite owner: %w", err))
			}
		}()
	}
	if err != nil {
		return fmt.Errorf("open canonical Movie Catalog SQLite owner: %w", err)
	}
	if movieOwner == nil {
		return errors.New("open canonical Movie Catalog SQLite owner: constructor returned nil")
	}
	hobbyOwner, err := deps.OpenHobbyGraphStore(hobbyGraphPath)
	if hobbyOwner != nil {
		defer func() {
			if err := hobbyOwner.Close(); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close canonical Hobby Graph SQLite owner: %w", err))
			}
		}()
	}
	if err != nil {
		return fmt.Errorf("open canonical Hobby Graph SQLite owner: %w", err)
	}
	if hobbyOwner == nil {
		return errors.New("open canonical Hobby Graph SQLite owner: constructor returned nil")
	}
	durableWorkflowOwner, err := deps.OpenDurableWorkflowStore(durableWorkflowPath)
	if durableWorkflowOwner != nil {
		defer func() {
			if err := durableWorkflowOwner.Close(); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close canonical Durable Store Workflow SQLite owner: %w", err))
			}
		}()
	}
	if err != nil {
		return fmt.Errorf("open canonical Durable Store Workflow SQLite owner: %w", err)
	}
	if durableWorkflowOwner == nil {
		return errors.New("open canonical Durable Store Workflow SQLite owner: constructor returned nil")
	}
	verificationOwner, err := deps.OpenVerificationReportStore(verificationReportPath)
	if err != nil {
		return fmt.Errorf("open canonical Verification Report owner: %w", err)
	}
	if verificationOwner == nil {
		return errors.New("open canonical Verification Report owner: constructor returned nil")
	}
	toolRegistryOwner, err := deps.OpenToolRegistryStore(toolRegistryPath)
	if toolRegistryOwner != nil {
		defer func() {
			if err := toolRegistryOwner.Close(); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close canonical Tool Registry SQLite owner: %w", err))
			}
		}()
	}
	if err != nil {
		return fmt.Errorf("open canonical Tool Registry SQLite owner: %w", err)
	}
	if toolRegistryOwner == nil {
		return errors.New("open canonical Tool Registry SQLite owner: constructor returned nil")
	}
	advisorOwner, err := deps.OpenAdvisorStore(advisorPath)
	if advisorOwner != nil {
		defer func() {
			if err := advisorOwner.Close(); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close canonical Advisor SQLite owner: %w", err))
			}
		}()
	}
	if err != nil {
		return fmt.Errorf("open canonical Advisor SQLite owner: %w", err)
	}
	if advisorOwner == nil {
		return errors.New("open canonical Advisor SQLite owner: constructor returned nil")
	}
	sandboxOwner, err := deps.OpenSandboxStore(sandboxPath)
	if sandboxOwner != nil {
		defer func() {
			if err := sandboxOwner.Close(); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close canonical Sandbox SQLite owner: %w", err))
			}
		}()
	}
	if err != nil {
		return fmt.Errorf("open canonical Sandbox SQLite owner: %w", err)
	}
	if sandboxOwner == nil {
		return errors.New("open canonical Sandbox SQLite owner: constructor returned nil")
	}
	dciOwner, err := deps.OpenDCIStore(dciPath)
	if dciOwner != nil {
		defer func() {
			if err := dciOwner.Close(); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close canonical DCI SQLite owner: %w", err))
			}
		}()
	}
	if err != nil {
		return fmt.Errorf("open canonical DCI SQLite owner: %w", err)
	}
	if dciOwner == nil {
		return errors.New("open canonical DCI SQLite owner: constructor returned nil")
	}
	skillGovernanceOwner, err := deps.OpenSkillGovernanceStore(skillGovernancePath)
	if skillGovernanceOwner != nil {
		defer func() {
			if err := skillGovernanceOwner.Close(); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close canonical Skill Governance SQLite owner: %w", err))
			}
		}()
	}
	if err != nil {
		return fmt.Errorf("open canonical Skill Governance SQLite owner: %w", err)
	}
	if skillGovernanceOwner == nil {
		return errors.New("open canonical Skill Governance SQLite owner: constructor returned nil")
	}
	knowledgeMemoryOwner, err := deps.OpenKnowledgeMemoryStore(knowledgeMemoryPath)
	if knowledgeMemoryOwner != nil {
		defer func() {
			if err := knowledgeMemoryOwner.Close(); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close canonical Knowledge Memory SQLite owner: %w", err))
			}
		}()
	}
	if err != nil {
		return fmt.Errorf("open canonical Knowledge Memory SQLite owner: %w", err)
	}
	if knowledgeMemoryOwner == nil {
		return errors.New("open canonical Knowledge Memory SQLite owner: constructor returned nil")
	}

	if err := deps.RegisterEventGroup(handler, eventOwner); err != nil {
		return fmt.Errorf("register canonical Event storage operations: %w", err)
	}
	if err := deps.RegisterSessionGroup(handler, sessionOwner); err != nil {
		return fmt.Errorf("register canonical Session storage operations: %w", err)
	}
	if err := deps.RegisterL1Group(handler, l1Owner, configuredLocalAgentOpsPrincipal(cfg)); err != nil {
		return fmt.Errorf("register canonical Conversation L1 storage operations: %w", err)
	}
	if err := deps.RegisterTurnGroup(handler, l1Owner); err != nil {
		return fmt.Errorf("register canonical Conversation Turn storage operations: %w", err)
	}
	if err := deps.RegisterTaskGroup(handler, taskOwner); err != nil {
		return fmt.Errorf("register canonical Task storage operations: %w", err)
	}
	userMemoryOwner, ok := any(l1Owner).(storagehost.UserMemoryGroupOwner)
	if !ok {
		return errors.New("canonical Conversation L1 owner lacks User Memory storage methods")
	}
	if err := deps.RegisterUserMemoryGroup(handler, userMemoryOwner); err != nil {
		return fmt.Errorf("register canonical User Memory storage operations: %w", err)
	}
	if err := deps.RegisterGlossaryGroup(handler, glossaryOwner); err != nil {
		return fmt.Errorf("register canonical Glossary storage operations: %w", err)
	}
	if err := deps.RegisterMovieCatalogGroup(handler, movieOwner); err != nil {
		return fmt.Errorf("register canonical Movie Catalog storage operations: %w", err)
	}
	if err := deps.RegisterHobbyGraphGroup(handler, hobbyOwner); err != nil {
		return fmt.Errorf("register canonical Hobby Graph storage operations: %w", err)
	}
	if err := deps.RegisterDurableWorkflowGroup(handler, durableWorkflowOwner); err != nil {
		return fmt.Errorf("register canonical Durable Store Workflow operations: %w", err)
	}
	if err := deps.RegisterVerificationGroup(handler, verificationOwner); err != nil {
		return fmt.Errorf("register canonical Verification Report operations: %w", err)
	}
	if err := deps.RegisterToolRegistryGroup(handler, toolRegistryOwner); err != nil {
		return fmt.Errorf("register canonical Tool Registry storage operations: %w", err)
	}
	if err := deps.RegisterOperationMemoryGroup(handler, operationMemoryOwner); err != nil {
		return fmt.Errorf("register recoverable operation memory operations: %w", err)
	}
	if err := deps.RegisterArchiveGroup(handler, archiveOwner); err != nil {
		return fmt.Errorf("register canonical Conversation Archive storage operations: %w", err)
	}
	if err := deps.RegisterAdvisorGroup(handler, advisorOwner); err != nil {
		return fmt.Errorf("register canonical Advisor storage operations: %w", err)
	}
	if err := deps.RegisterSandboxGroup(handler, sandboxOwner); err != nil {
		return fmt.Errorf("register canonical Sandbox storage operations: %w", err)
	}
	if err := deps.RegisterDCIGroup(handler, dciOwner); err != nil {
		return fmt.Errorf("register canonical DCI storage operations: %w", err)
	}
	if err := deps.RegisterSkillGovernanceGroup(handler, skillGovernanceOwner); err != nil {
		return fmt.Errorf("register canonical Skill Governance storage operations: %w", err)
	}
	if err := deps.RegisterKnowledgeMemoryGroup(handler, knowledgeMemoryOwner); err != nil {
		return fmt.Errorf("register canonical Knowledge Memory storage operations: %w", err)
	}
	listener, err := deps.Listen("tcp", listenAddress)
	if err != nil {
		return fmt.Errorf("listen on configured storage-host address: %w", err)
	}
	defer func() {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			resultErr = errors.Join(resultErr, fmt.Errorf("close storage-host listener: %w", err))
		}
	}()

	serve := deps.Serve
	if serve == nil {
		serve = serveStorageHostHTTP
	}
	if err := serve(ctx, listener, handler); err != nil {
		return fmt.Errorf("serve storage-host RPC: %w", err)
	}
	return nil
}

func parseStorageHostListenAddress(listen string) (string, error) {
	const message = "storage.host.listen must be a fixed loopback IP address with a numeric port from 1 through 65535"
	value := strings.TrimSpace(listen)
	if value == "" || strings.Contains(value, "://") {
		return "", errors.New(message)
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil || host == "" || port == "" {
		return "", errors.New(message)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return "", errors.New(message)
	}
	for _, char := range port {
		if char < '0' || char > '9' {
			return "", errors.New(message)
		}
	}
	numericPort, err := strconv.Atoi(port)
	if err != nil || numericPort < 1 || numericPort > 65535 {
		return "", errors.New(message)
	}
	return net.JoinHostPort(host, port), nil
}

func serveStorageHostHTTP(ctx context.Context, listener net.Listener, handler http.Handler) error {
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()

	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), storageHostShutdownTimeout)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		if shutdownErr != nil {
			_ = server.Close()
		}
		serveErr := <-serveResult
		if !errors.Is(serveErr, http.ErrServerClosed) && serveErr != nil {
			return serveErr
		}
		if shutdownErr != nil {
			return shutdownErr
		}
		return nil
	}
}
