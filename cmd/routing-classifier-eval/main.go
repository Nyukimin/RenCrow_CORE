package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing/classifierlab"
)

func main() {
	if err := execute(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "routing-classifier-eval:", err)
		os.Exit(1)
	}
}

type commandError struct {
	Code string
	Err  error
}

func (e commandError) Error() string {
	if e.Err == nil {
		return e.Code
	}
	return e.Code + ": " + e.Err.Error()
}

func execute(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("command required: inspect, prepare, smoke, replay, e2e, report")
	}
	command := arguments[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", "", "experiment config path")
	coreConfigPath := flags.String("core-config", "", "explicit CORE runtime config path for inspect-only legacy baseline resolution")
	outPath := flags.String("out", "", "new private output directory")
	repoPath := flags.String("repo", "", "CORE repository path; defaults to the current repository")
	datasetPath := flags.String("dataset", "", "JSONL evaluation dataset")
	fixturesPath := flags.String("fixtures", "", "JSONL smoke fixtures")
	split := flags.String("split", "", "dataset split")
	variants := flags.String("variants", "", "comma-separated variants")
	runPath := flags.String("run", "", "existing run directory containing predictions.jsonl")
	e2ePath := flags.String("e2e", "", "existing isolated E2E run directory")
	isolated := flags.Bool("isolated", false, "assert that the E2E environment is isolated")
	policyPath := flags.String("policy", "", "externally reviewed RoutePolicy JSON for prepare")
	legacyPromptPath := flags.String("legacy-prompt", "", "explicit prompt file whose bytes are recorded as provenance")
	sdkVersion := flags.String("sdk-version", "", "exact official Laya package version for prepare")
	basePython := flags.String("base-python", "", "Python executable used only to create the private venv; defaults to python3")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	repoRoot := ""
	if command != "report" {
		var err error
		repoRoot, err = resolveRepoRoot(*repoPath)
		if err != nil {
			return err
		}
	}
	switch command {
	case "inspect":
		if err := requireFlags("inspect", map[string]string{"config": *configPath, "out": *outPath}); err != nil {
			return err
		}
		return runInspect(*configPath, *outPath, repoRoot, *coreConfigPath)
	case "prepare":
		if err := requireFlags("prepare", map[string]string{"config": *configPath, "out": *outPath}); err != nil {
			return err
		}
		return runPrepare(*configPath, *outPath, repoRoot, *policyPath, *legacyPromptPath, *sdkVersion, *basePython)
	case "smoke":
		if err := requireFlags("smoke", map[string]string{"config": *configPath, "fixtures": *fixturesPath, "out": *outPath}); err != nil {
			return err
		}
		return runSmoke(*configPath, *fixturesPath, *outPath, repoRoot)
	case "replay":
		if err := requireFlags("replay", map[string]string{"config": *configPath, "dataset": *datasetPath, "split": *split, "variants": *variants, "out": *outPath}); err != nil {
			return err
		}
		return runReplay(*configPath, *datasetPath, *split, *variants, *outPath, repoRoot)
	case "e2e":
		if err := requireFlags("e2e", map[string]string{"config": *configPath, "dataset": *datasetPath, "out": *outPath}); err != nil {
			return err
		}
		return runE2E(*configPath, *datasetPath, *outPath, *isolated, repoRoot)
	case "report":
		if err := requireFlags("report", map[string]string{"run": *runPath, "e2e": *e2ePath, "out": *outPath}); err != nil {
			return err
		}
		return runReport(*runPath, *e2ePath, *outPath)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func requireFlags(command string, required map[string]string) error {
	missing := make([]string, 0)
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, "--"+name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%s requires %s", command, strings.Join(missing, ", "))
}

func resolveRepoRoot(value string) (string, error) {
	if value != "" {
		absolute, err := filepath.Abs(value)
		if err != nil {
			return "", fmt.Errorf("resolve --repo: %w", err)
		}
		if _, err := os.Stat(filepath.Join(absolute, "go.mod")); err != nil {
			return "", errors.New("--repo must point to RenCrow_CORE root")
		}
		return absolute, nil
	}
	current, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get current directory: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(current, "internal", "infrastructure", "routing", "classifierlab")); err == nil {
				return current, nil
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return "", errors.New("could not locate RenCrow_CORE root; pass --repo")
}

func loadExperimentConfig(name string) (classifierlab.Config, error) {
	config, err := classifierlab.LoadConfig(name)
	if err != nil {
		return classifierlab.Config{}, commandError{Code: "CONFIG_INVALID", Err: err}
	}
	if strings.TrimSpace(config.ExperimentID) == "" || strings.HasPrefix(strings.TrimSpace(config.ExperimentID), "REPLACE_") || len(config.ExperimentID) > 128 {
		return classifierlab.Config{}, commandError{Code: "CONFIG_INVALID", Err: errors.New("experiment_id must be a concrete value between 1 and 128 bytes")}
	}
	return config, nil
}
