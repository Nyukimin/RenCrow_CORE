package main

import (
	"context"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/migrationhook"
	"github.com/Nyukimin/RenCrow_CORE/internal/migrationpackage"
)

var migrationConfigLogMu sync.Mutex

func cmdMigrationHook() {
	os.Exit(runMigrationHook(os.Args[2:], os.Stdin, os.Stdout))
}

func runMigrationHook(args []string, stdin io.Reader, stdout io.Writer) int {
	return migrationhook.Run(args, stdin, stdout, validateMigrationConfig, migrationhook.StateOperations{
		Export:          exportMigrationState,
		ValidateRestore: validateMigrationStateRestore,
		ImportDryRun:    validateMigrationStateRestore,
	})
}

func exportMigrationState(outputDir string) (migrationhook.Artifact, error) {
	runner, err := installedOwnerExecutable("rencrow-storage-backup")
	if err != nil {
		return migrationhook.Artifact{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	command := exec.CommandContext(ctx, runner, "core-export", outputDir)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return migrationhook.Artifact{}, err
	}
	summary, err := migrationpackage.Inspect(outputDir)
	if err != nil {
		return migrationhook.Artifact{}, err
	}
	return migrationhook.Artifact{LogicalID: summary.LogicalID, SHA256: summary.SHA256, SizeBytes: summary.SizeBytes}, nil
}

func validateMigrationStateRestore(packageDir, candidateConfig, _ string) error {
	if _, err := migrationpackage.Inspect(packageDir); err != nil {
		return err
	}
	if err := validateMigrationConfig(candidateConfig); err != nil {
		return err
	}
	checker, err := installedOwnerExecutable("rencrow-storage-restore-check")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, checker, packageDir)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}

func installedOwnerExecutable(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".local", "bin", name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 || info.Mode().Perm()&0o100 == 0 {
		return "", os.ErrPermission
	}
	return path, nil
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
