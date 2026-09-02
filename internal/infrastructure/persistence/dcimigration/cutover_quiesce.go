package dcimigration

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type cutoverActiveQuiesceEvidence struct {
	SQLiteSources     int
	BusyZero          int
	JournalModeDelete int
	SameFile          int
	SidecarZero       int
}

func (e cutoverActiveQuiesceEvidence) valid() bool {
	return e.SQLiteSources == 4 && e.BusyZero == 1 && e.JournalModeDelete == 1 &&
		e.SameFile == 1 && e.SidecarZero == 1
}

func quiesceCutoverActiveSQLiteSources(ctx context.Context, options cutoverActiveOptions) (cutoverActiveQuiesceEvidence, error) {
	if ctx == nil {
		return cutoverActiveQuiesceEvidence{}, newCodedError("active_quiesce", "active SQLite quiesce context is invalid")
	}
	if err := ctx.Err(); err != nil {
		return cutoverActiveQuiesceEvidence{}, err
	}
	if err := validateCutoverActiveOptions(options); err != nil {
		return cutoverActiveQuiesceEvidence{}, err
	}
	paths, err := resolveCutoverActivePaths(options)
	if err != nil {
		return cutoverActiveQuiesceEvidence{}, newCodedError("active_quiesce", "resolve active SQLite quiesce sources")
	}
	items := []struct {
		path   string
		sqlite bool
	}{
		{path: paths.dci, sqlite: true},
		{path: paths.dciJSONL, sqlite: false},
		{path: paths.eventStore, sqlite: true},
		{path: paths.l1, sqlite: true},
		{path: paths.archive, sqlite: true},
	}
	bindings := make([]cutoverBoundFile, 0, len(items))
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return cutoverActiveQuiesceEvidence{}, err
		}
		// Sidecars are expected at this boundary. Bind the regular base file
		// first, then require sidecar-zero after the checkpoint below.
		binding, err := bindCutoverFile(item.path, false, false)
		if err != nil {
			return cutoverActiveQuiesceEvidence{}, newCodedError("active_quiesce", "bind active SQLite quiesce source")
		}
		binding.sqlite = item.sqlite
		bindings = append(bindings, binding)
	}
	if err := validateCutoverActiveAliases(nil, bindings); err != nil {
		return cutoverActiveQuiesceEvidence{}, newCodedError("active_quiesce", "active SQLite quiesce sources alias")
	}

	postBindings := make([]cutoverBoundFile, 0, len(items))
	for index, item := range items {
		if !item.sqlite {
			postBindings = append(postBindings, bindings[index])
			continue
		}
		post, err := quiesceCutoverActiveSQLiteSource(ctx, bindings[index])
		if err != nil {
			return cutoverActiveQuiesceEvidence{}, err
		}
		postBindings = append(postBindings, post)
	}
	for _, binding := range postBindings {
		if err := ctx.Err(); err != nil {
			return cutoverActiveQuiesceEvidence{}, err
		}
		if err := verifyCutoverBoundFile(binding); err != nil {
			return cutoverActiveQuiesceEvidence{}, newCodedError("active_quiesce", "active SQLite quiesce source changed")
		}
	}
	return cutoverActiveQuiesceEvidence{
		SQLiteSources: 4, BusyZero: 1, JournalModeDelete: 1, SameFile: 1, SidecarZero: 1,
	}, nil
}

func quiesceCutoverActiveSQLiteSource(ctx context.Context, before cutoverBoundFile) (cutoverBoundFile, error) {
	if err := ctx.Err(); err != nil {
		return cutoverBoundFile{}, err
	}
	current, err := os.Lstat(before.path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || !os.SameFile(before.info, current) {
		return cutoverBoundFile{}, newCodedError("active_quiesce", "active SQLite source changed before quiesce")
	}
	db, err := sql.Open("sqlite", sqliteReadWriteNoWaitDSN(before.path))
	if err != nil {
		return cutoverBoundFile{}, newCodedError("active_quiesce", "open active SQLite source")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return cutoverBoundFile{}, err
		}
		return cutoverBoundFile{}, newCodedError("active_quiesce", "bind active SQLite connection")
	}

	var busy, logFrames, checkpointedFrames int
	operationErr := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointedFrames)
	if operationErr == nil && busy != 0 {
		operationErr = errors.New("active SQLite source is busy")
	}
	var mode string
	if operationErr == nil {
		operationErr = conn.QueryRowContext(ctx, "PRAGMA journal_mode=DELETE").Scan(&mode)
	}
	if operationErr == nil && !strings.EqualFold(mode, "delete") {
		operationErr = errors.New("active SQLite journal mode is not delete")
	}
	closeErr := errors.Join(conn.Close(), db.Close())
	if operationErr != nil {
		if errors.Is(operationErr, context.Canceled) || errors.Is(operationErr, context.DeadlineExceeded) {
			return cutoverBoundFile{}, operationErr
		}
		return cutoverBoundFile{}, newCodedError("active_quiesce", "quiesce active SQLite source")
	}
	if closeErr != nil {
		return cutoverBoundFile{}, newCodedError("active_quiesce", "close active SQLite source after quiesce")
	}
	if err := ctx.Err(); err != nil {
		return cutoverBoundFile{}, err
	}
	after, err := bindCutoverFile(before.path, false, true)
	if err != nil || !os.SameFile(before.info, after.info) || (runtime.GOOS != "windows" && before.info.Mode().Perm() != after.info.Mode().Perm()) {
		return cutoverBoundFile{}, newCodedError("active_quiesce", "active SQLite source binding changed during quiesce")
	}
	return after, nil
}

func sqliteReadWriteNoWaitDSN(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=rw&_pragma=busy_timeout%3d0"}).String()
}
