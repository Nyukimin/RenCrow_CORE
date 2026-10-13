package action

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// VerifyReport is what VerifyActionStore found. It carries counts and sizes, never
// a record or an ID.
type VerifyReport struct {
	// OK is true only when the logs are valid under the full read, the snapshot
	// built from them is valid, and the two hold the same records.
	OK bool `json:"ok"`
	// Layout is "v1" (the logs in the store root) or "v2" (segments), and Segments
	// how many segments the check read.
	Layout       string   `json:"layout"`
	Segments     int      `json:"segments"`
	Actions      int      `json:"actions"`
	Attempts     int      `json:"attempts"`
	ActionBytes  int64    `json:"action_bytes"`
	AttemptBytes int64    `json:"attempt_bytes"`
	Mismatches   int      `json:"mismatches"`
	Problems     []string `json:"problems,omitempty"`
	// RunsChecked and RunsMissing count the distinct Runs the Actions name that
	// the Run check was asked about, and those it reported as owned by no Task.
	// Both are zero when no Run check was given.
	RunsChecked int `json:"runs_checked"`
	RunsMissing int `json:"runs_missing"`
	// BuildMs is the time to build the snapshot, FullReadMs the time of the full
	// read and its whole-store check: the cost a transaction pays without it.
	BuildMs    int64 `json:"build_ms"`
	FullReadMs int64 `json:"full_read_ms"`
	DurationMs int64 `json:"duration_ms"`
}

func (r *VerifyReport) problem(format string, args ...any) {
	r.Mismatches++
	r.Problems = append(r.Problems, fmt.Sprintf(format, args...))
}

// VerifyOptions selects optional checks of VerifyActionStoreWithOptions.
type VerifyOptions struct {
	// CheckRuns, when set, is given every distinct Run the Actions name, once, and
	// returns how many of them no Task owns. The Action store cannot answer that
	// itself (Runs belong to the Task store), so the caller supplies it.
	CheckRuns func(ctx context.Context, runs []modulecore.RunID) (missing int, err error)
}

// VerifyActionStore is VerifyActionStoreWithOptions without the optional checks.
func VerifyActionStore(ctx context.Context, root string) (VerifyReport, error) {
	return VerifyActionStoreWithOptions(ctx, root, VerifyOptions{})
}

// VerifyActionStoreWithOptions checks the snapshot against the full read, on the logs under
// root, without changing anything: it opens the files as a reader, builds the
// snapshot the way a store opened with OpenOptions.Index does, reads the logs in
// full the way every transaction of a store without it does, and compares the two
// record by record, field by field, and the snapshot's lookups against the
// records. On a store with segments it reads every segment, oldest first, as the
// writer does, and holds each closed segment to the size recorded when it was
// closed; a segment that is not that size is a finding, not a failure to run. It
// holds the lock of the newest segment (the store's OS lock) for the whole check,
// which waits for a running CORE's transaction and blocks the next; run it on a
// copy of a store in use. A store with an unsettled WAL is an error: nothing can
// be said about it until a writer has repaired it.
func VerifyActionStoreWithOptions(ctx context.Context, root string, options VerifyOptions) (VerifyReport, error) {
	started := time.Now()
	var report VerifyReport
	abs, err := filepath.Abs(root)
	if err != nil {
		return report, fmt.Errorf("resolve action store root: %w", err)
	}
	if info, err := os.Stat(abs); err != nil {
		return report, fmt.Errorf("action store %q: %w", root, err)
	} else if !info.IsDir() {
		return report, fmt.Errorf("action store %q is not a directory", root)
	}
	layout, err := readLayout(abs)
	if err != nil {
		return report, err
	}
	refs, err := listSegments(abs, layout.v2)
	if err != nil {
		return report, err
	}
	if layout.v2 && len(refs) < 2 {
		return report, fmt.Errorf("%w: LAYOUT v2 is present but there is no segment under %s", ErrSegmentMissing, logsDirName)
	}
	report.Layout, report.Segments = "v1", len(refs)
	if layout.v2 {
		report.Layout = "v2"
	}
	newest := refs[len(refs)-1]
	batch, err := jsonlbatch.OpenReader(newest.dir, logFilenames[:])
	if err != nil {
		return report, fmt.Errorf("open the action store as a reader: %w", err)
	}
	err = batch.Read(ctx, func() error {
		buildStarted := time.Now()
		st, buildErr := buildSnapState(ctx, layout, refs)
		report.BuildMs = time.Since(buildStarted).Milliseconds()
		if errors.Is(buildErr, ErrUnknownLayout) {
			return buildErr
		}

		readStarted := time.Now()
		actionPaths, attemptPaths := make([]string, len(refs)), make([]string, len(refs))
		for i, ref := range refs {
			paths := ref.paths()
			actionPaths[i], attemptPaths[i] = paths[fileAction], paths[fileAttempt]
		}
		actions, readErr := loadActionsFrom(ctx, actionPaths...)
		var attempts []domainaction.Attempt
		if readErr == nil {
			attempts, readErr = loadAttemptsFrom(ctx, attemptPaths...)
		}
		if readErr == nil {
			readErr = validateSnapshot(actions, attempts)
		}
		report.FullReadMs = time.Since(readStarted).Milliseconds()

		switch {
		case errors.Is(buildErr, ErrSealedSegmentChanged):
			report.problem("%s", firstLine(buildErr))
		case buildErr != nil && readErr != nil:
			report.problem("the logs are not valid: the full read refuses them (%v) and so does the snapshot build", firstLine(readErr))
		case buildErr != nil:
			report.problem("the full read accepts the logs but the snapshot build refuses them: %v", firstLine(buildErr))
		case readErr != nil:
			report.problem("the snapshot build accepts the logs but the full read refuses them: %v", firstLine(readErr))
		default:
			report.Actions, report.Attempts = len(st.actions.latest), len(st.attempts.latest)
			report.ActionBytes = st.closed[fileAction] + st.applied[fileAction]
			report.AttemptBytes = st.closed[fileAttempt] + st.applied[fileAttempt]
			for _, line := range st.check(actions, attempts) {
				report.problem("%s", line)
			}
			if options.CheckRuns != nil {
				runs := make([]modulecore.RunID, 0, len(st.byRun))
				for run := range st.byRun {
					runs = append(runs, run)
				}
				sort.Slice(runs, func(i, j int) bool { return runs[i] < runs[j] })
				missing, checkErr := options.CheckRuns(ctx, runs)
				if checkErr != nil {
					return fmt.Errorf("check the Runs the Actions name: %w", checkErr)
				}
				report.RunsChecked, report.RunsMissing = len(runs), missing
				if missing > 0 {
					report.problem("%d of the %d Runs the Actions name are owned by no Task", missing, len(runs))
				}
			}
		}
		return nil
	})
	if err != nil {
		return report, err
	}
	report.OK = report.Mismatches == 0
	report.DurationMs = time.Since(started).Milliseconds()
	return report, nil
}

// idPattern matches a CORE identifier of any kind: a short lower-case prefix, an
// underscore and a UUID.
var idPattern = regexp.MustCompile(`\b[a-z]{2,8}_[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)

// firstLine keeps an error to its first line and a bounded length, with the IDs
// it names replaced, so a report never grows with what a record contained and
// never prints an identifier.
func firstLine(err error) string {
	text := idPattern.ReplaceAllString(err.Error(), "<id>")
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			text = text[:i]
			break
		}
	}
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}
