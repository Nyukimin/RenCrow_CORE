package main

import (
	"io"
	"log"
	"os"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/migrationhook"
)

var migrationConfigLogMu sync.Mutex

func cmdMigrationHook() {
	os.Exit(runMigrationHook(os.Args[2:], os.Stdin, os.Stdout))
}

func runMigrationHook(args []string, stdin io.Reader, stdout io.Writer) int {
	return migrationhook.Run(args, stdin, stdout, validateMigrationConfig)
}

func validateMigrationConfig(candidatePath string) error {
	// LoadConfig is the canonical validator, but its normal startup diagnostics
	// may contain host-bound paths. The migration hook must emit only its bounded
	// receipt, so validation runs with the process logger temporarily discarded.
	migrationConfigLogMu.Lock()
	defer migrationConfigLogMu.Unlock()
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	log.SetOutput(io.Discard)
	defer func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	}()
	_, err := config.LoadConfig(candidatePath)
	return err
}
