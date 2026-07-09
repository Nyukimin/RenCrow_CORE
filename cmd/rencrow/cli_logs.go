package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

func cmdLogs() {
	cfg, err := config.LoadConfig(getConfigPath())
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	logPath := os.Getenv("RENCROW_LOG_PATH")
	if strings.TrimSpace(logPath) == "" {
		logPath = "rencrow.log"
	}
	code := runLogsCommand(
		os.Args[2:],
		logPath,
		os.Stdout,
		os.Stderr,
		printLastLinesTo,
		followFileTo,
		func() time.Time { return time.Now().UTC() },
	)
	if code != 0 {
		os.Exit(code)
	}

	_ = cfg // keep config load validation for command consistency
}

func runLogsCommand(
	args []string,
	logPath string,
	out io.Writer,
	errOut io.Writer,
	tailFn func(path string, n int, out io.Writer) error,
	followFn func(path string, out io.Writer) error,
	now func() time.Time,
) int {
	follow := hasFlag(args, "--follow")
	jsonOut := hasFlag(args, "--json")

	if jsonOut {
		status := "snapshot"
		if follow {
			status = "streaming"
		}
		writeJSONCLI(out, map[string]any{
			"ok":        true,
			"timestamp": now().Format(time.RFC3339),
			"component": "logs",
			"status":    status,
			"details": map[string]any{
				"path":   logPath,
				"follow": follow,
			},
		}, false)
	}

	if err := tailFn(logPath, 100, out); err != nil {
		fmt.Fprintf(errOut, "failed to read logs: %v\n", err)
		return 1
	}
	if !follow {
		return 0
	}
	if err := followFn(logPath, out); err != nil {
		fmt.Fprintf(errOut, "failed to follow logs: %v\n", err)
		return 1
	}
	return 0
}

func printLastLines(path string, n int) error {
	return printLastLinesTo(path, n, os.Stdout)
}

func printLastLinesTo(path string, n int, out io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	lines := make([]string, 0, n)
	s := bufio.NewScanner(f)
	for s.Scan() {
		lines = append(lines, s.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	if err := s.Err(); err != nil {
		return err
	}
	for _, line := range lines {
		fmt.Fprintln(out, line)
	}
	return nil
}

func followFile(path string) error {
	return followFileTo(path, os.Stdout)
}

func followFileTo(path string, out io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	_, _ = f.Seek(0, io.SeekEnd)
	reader := bufio.NewReader(f)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		for {
			line, err := reader.ReadString('\n')
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			fmt.Fprint(out, line)
		}
	}
	return nil
}

// cmdHelp はヘルプメッセージを表示
