package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

type coderWorkspaceEntry struct {
	kind string
	mode uint32
	hash string
}

func canonicalCoderTaskRoot(raw string, ctx context.Context) (string, bool, error) {
	if strings.TrimSpace(raw) == "" {
		return "", false, errors.New("authoritative Task.ModuleRoot is required")
	}
	abs, err := filepath.Abs(strings.TrimSpace(raw))
	if err != nil {
		return "", false, fmt.Errorf("resolve Task.ModuleRoot: %w", err)
	}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", false, fmt.Errorf("canonicalize Task.ModuleRoot: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", false, fmt.Errorf("inspect Task.ModuleRoot: %w", err)
	}
	if !info.IsDir() {
		return "", false, errors.New("Task.ModuleRoot must be an existing directory")
	}
	root = filepath.Clean(root)
	switch repositoryWorkspaceKind(root) {
	case "nested":
		return "", false, errors.New("Task.ModuleRoot is nested inside another Git repository")
	case "scratch":
		return root, true, nil
	case "repository":
		gitEntry, err := os.Lstat(filepath.Join(root, ".git"))
		if err != nil || gitEntry.Mode()&os.ModeSymlink != 0 || !(gitEntry.IsDir() || gitEntry.Mode().IsRegular()) {
			return "", false, errors.New("Task.ModuleRoot has an invalid Git metadata entry")
		}
		ctx, cancel := context.WithTimeout(ctx, workerCommandTimeout(config.DefaultWorkerCommandTimeoutSeconds))
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--show-toplevel")
		cmd.Env = coderSnapshotGitEnv()
		output, err := cmd.Output()
		if err != nil {
			return "", false, fmt.Errorf("resolve Git worktree root: %w", err)
		}
		gitRoot, err := filepath.EvalSymlinks(strings.TrimRight(string(output), "\r\n"))
		if err != nil {
			return "", false, fmt.Errorf("canonicalize Git worktree root: %w", err)
		}
		if filepath.Clean(gitRoot) != root {
			return "", false, fmt.Errorf("Task.ModuleRoot %q is not the independent Git worktree root %q", root, gitRoot)
		}
		return root, false, nil
	default:
		return "", false, errors.New("Task.ModuleRoot workspace kind is unsupported")
	}
}

func captureCoderWorkspaceSnapshot(ctx context.Context, root string, scratch bool, timeoutSeconds int) (map[string]coderWorkspaceEntry, error) {
	if ctx == nil {
		return nil, errors.New("workspace snapshot context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	paths := make([]string, 0)
	if scratch {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path == root {
				return nil
			}
			if entry.IsDir() {
				if entry.Name() == ".git" {
					return fmt.Errorf("scratch Task.ModuleRoot contains nested Git metadata: %s", path)
				}
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			paths = append(paths, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk scratch workspace: %w", err)
		}
	} else {
		if timeoutSeconds <= 0 {
			timeoutSeconds = config.DefaultWorkerCommandTimeoutSeconds
		}
		ctx, cancel := context.WithTimeout(ctx, workerCommandTimeout(timeoutSeconds))
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", "-C", root, "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "ls-files", "--cached", "--others", "--exclude-standard", "-z")
		cmd.Env = coderSnapshotGitEnv()
		output, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("list tracked and non-ignored untracked files: %w", err)
		}
		for _, name := range strings.Split(string(output), "\x00") {
			if name != "" {
				paths = append(paths, filepath.ToSlash(name))
			}
		}
	}
	sort.Strings(paths)
	snapshot := make(map[string]coderWorkspaceEntry, len(paths))
	for _, rel := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if filepath.IsAbs(filepath.FromSlash(rel)) || rel == ".." || strings.HasPrefix(rel, "../") {
			return nil, fmt.Errorf("workspace snapshot contains an escaping path %q", rel)
		}
		entry, exists, err := snapshotCoderWorkspaceEntry(root, filepath.FromSlash(rel))
		if err != nil {
			return nil, fmt.Errorf("snapshot %q: %w", rel, err)
		}
		if exists {
			snapshot[filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))] = entry
		}
	}
	return snapshot, nil
}

func snapshotCoderWorkspaceEntry(root, rel string) (coderWorkspaceEntry, bool, error) {
	path := filepath.Join(root, rel)
	if err := rejectSymlinkParents(root, rel); err != nil {
		return coderWorkspaceEntry{}, false, err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return coderWorkspaceEntry{}, false, nil
	}
	if err != nil {
		return coderWorkspaceEntry{}, false, err
	}
	entry := coderWorkspaceEntry{mode: uint32(info.Mode().Perm())}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		entry.kind = "symlink"
		target, err := os.Readlink(path)
		if err != nil {
			return coderWorkspaceEntry{}, false, err
		}
		after, err := os.Lstat(path)
		if err != nil || !os.SameFile(info, after) {
			return coderWorkspaceEntry{}, false, errors.New("symlink changed while snapshotting")
		}
		digest := sha256.Sum256([]byte(target))
		entry.hash = hex.EncodeToString(digest[:])
	case info.Mode().IsRegular():
		entry.kind = "file"
		file, err := os.Open(path)
		if err != nil {
			return coderWorkspaceEntry{}, false, err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return coderWorkspaceEntry{}, false, copyErr
		}
		if closeErr != nil {
			return coderWorkspaceEntry{}, false, closeErr
		}
		after, err := os.Lstat(path)
		if err != nil || !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) || info.Mode() != after.Mode() {
			return coderWorkspaceEntry{}, false, errors.New("file changed while snapshotting")
		}
		entry.hash = hex.EncodeToString(hash.Sum(nil))
	default:
		return coderWorkspaceEntry{}, false, fmt.Errorf("unsupported workspace file type %s", info.Mode().Type())
	}
	return entry, true, nil
}

func rejectSymlinkParents(root, rel string) error {
	parts := strings.Split(filepath.Clean(rel), string(os.PathSeparator))
	current := root
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace path parent is a symlink: %s", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("workspace path parent is not a directory: %s", current)
		}
	}
	return nil
}

func changedCoderWorkspaceFiles(baseline, current map[string]coderWorkspaceEntry) []string {
	paths := make(map[string]struct{}, len(baseline)+len(current))
	for path := range baseline {
		paths[path] = struct{}{}
	}
	for path := range current {
		paths[path] = struct{}{}
	}
	changed := make([]string, 0)
	for path := range paths {
		before, beforeExists := baseline[path]
		after, afterExists := current[path]
		if !beforeExists || !afterExists || before != after {
			changed = append(changed, filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))))
		}
	}
	sort.Strings(changed)
	return changed
}

func equalCoderWorkspaceSnapshots(left, right map[string]coderWorkspaceEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for path, entry := range left {
		if other, exists := right[path]; !exists || other != entry {
			return false
		}
	}
	return true
}

func coderPathWithin(path, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != "." && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func coderSnapshotGitEnv() []string {
	blocked := []string{
		"GIT_DIR=", "GIT_WORK_TREE=", "GIT_COMMON_DIR=", "GIT_INDEX_FILE=",
		"GIT_OBJECT_DIRECTORY=", "GIT_ALTERNATE_OBJECT_DIRECTORIES=", "GIT_CEILING_DIRECTORIES=",
		"GIT_CONFIG=", "GIT_CONFIG_COUNT=", "GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_",
	}
	env := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		blockedValue := false
		for _, prefix := range blocked {
			if strings.HasPrefix(value, prefix) {
				blockedValue = true
				break
			}
		}
		if !blockedValue {
			env = append(env, value)
		}
	}
	return append(env, "GIT_OPTIONAL_LOCKS=0")
}

func workerCommandTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		seconds = config.DefaultWorkerCommandTimeoutSeconds
	}
	maxSeconds := int64(1<<63-1) / int64(time.Second)
	if int64(seconds) > maxSeconds {
		return time.Duration(1<<63 - 1)
	}
	return time.Duration(seconds) * time.Second
}
