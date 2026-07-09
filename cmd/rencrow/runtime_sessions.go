package main

import (
	"log"
	"os"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	memorypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/memory"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
)

type sessionRuntime struct {
	SessionRepo   orchestrator.SessionRepository
	CentralMemory *domainsession.CentralMemory
	MemoryStore   *memorypersistence.FileStore
}

func buildSessionRuntime(cfg *config.Config) sessionRuntime {
	sessionRepo := session.NewJSONSessionRepository(cfg.Session.StorageDir)
	centralMemory := domainsession.NewCentralMemory()
	if err := os.MkdirAll(cfg.Session.StorageDir, 0755); err != nil {
		log.Fatalf("Failed to create session directory: %v", err)
	}
	operationMemoryDir := cfg.OperationMemoryDir
	if operationMemoryDir == "" {
		operationMemoryDir = config.DefaultOperationMemoryDir()
	}
	memStore := memorypersistence.NewFileStoreAt(operationMemoryDir)
	log.Printf("MemoryStore initialized (operation_memory_dir: %s)", operationMemoryDir)
	return sessionRuntime{
		SessionRepo:   sessionRepo,
		CentralMemory: centralMemory,
		MemoryStore:   memStore,
	}
}
