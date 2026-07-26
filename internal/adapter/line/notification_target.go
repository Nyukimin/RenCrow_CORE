package line

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const directUserTargetRelativePath = "state/line_notification_target"

// DirectUserTargetStore keeps the first verified direct-message sender as a
// local notification target. It is only a fallback when heartbeat.chat_id is
// absent or still contains an unresolved environment reference.
type DirectUserTargetStore struct {
	path string
}

func NewDirectUserTargetStore(workspaceDir string) *DirectUserTargetStore {
	return &DirectUserTargetStore{
		path: filepath.Join(strings.TrimSpace(workspaceDir), directUserTargetRelativePath),
	}
}

func (s *DirectUserTargetStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Record writes the first direct LINE user ID and never replaces it
// implicitly. The boolean is true only when this call created the target.
func (s *DirectUserTargetStore) Record(userID string) (bool, error) {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return false, fmt.Errorf("LINE direct user target store path is empty")
	}
	kind, err := TargetKind(userID)
	if err != nil {
		return false, err
	}
	if kind != "user" {
		return false, fmt.Errorf("LINE notification enrollment requires a direct user ID")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return false, fmt.Errorf("create LINE notification target directory: %w", err)
	}
	file, err := os.OpenFile(s.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("create LINE notification target: %w", err)
	}
	if _, err := file.WriteString(strings.TrimSpace(userID) + "\n"); err != nil {
		_ = file.Close()
		return false, fmt.Errorf("write LINE notification target: %w", err)
	}
	if err := file.Close(); err != nil {
		return false, fmt.Errorf("close LINE notification target: %w", err)
	}
	return true, nil
}

func (s *DirectUserTargetStore) Load() (string, error) {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return "", fmt.Errorf("LINE direct user target store path is empty")
	}
	info, err := os.Lstat(s.path)
	if err != nil {
		return "", fmt.Errorf("read LINE notification target metadata: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("LINE notification target must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("LINE notification target permissions must be 0600")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return "", fmt.Errorf("read LINE notification target: %w", err)
	}
	target := strings.TrimSpace(string(data))
	kind, err := TargetKind(target)
	if err != nil {
		return "", err
	}
	if kind != "user" {
		return "", fmt.Errorf("recorded LINE notification target is not a direct user")
	}
	return target, nil
}

func TargetKind(target string) (string, error) {
	target = strings.TrimSpace(target)
	if len(target) != 33 {
		return "", fmt.Errorf("invalid LINE target ID")
	}
	for _, char := range target[1:] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", char) {
			return "", fmt.Errorf("invalid LINE target ID")
		}
	}
	switch target[0] {
	case 'U':
		return "user", nil
	case 'C':
		return "group", nil
	case 'R':
		return "room", nil
	default:
		return "", fmt.Errorf("LINE target ID must identify a user, group, or room")
	}
}

func MaskTargetID(target string) string {
	target = strings.TrimSpace(target)
	if len(target) <= 8 {
		return "[masked]"
	}
	return target[:4] + "************************" + target[len(target)-4:]
}
