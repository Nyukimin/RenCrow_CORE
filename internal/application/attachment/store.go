package attachment

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	domainattachment "github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	domainsecurity "github.com/Nyukimin/RenCrow_CORE/internal/domain/security"
)

// IncomingFile is a transport-neutral upload passed into the attachment store.
type IncomingFile struct {
	Filename    string
	ContentType string
	SizeBytes   int64
	Reader      io.Reader
}

// Store persists user attachments under the workspace.
type Store struct {
	WorkspaceDir string
	Limits       domainattachment.Limits
	Now          func() time.Time
	NewID        func() string
}

// NewStore creates an attachment store rooted in workspaceDir.
func NewStore(workspaceDir string) *Store {
	return &Store{
		WorkspaceDir: workspaceDir,
		Limits:       domainattachment.DefaultLimits,
		Now:          time.Now,
		NewID:        randomID,
	}
}

// SaveAll validates, stores, and returns domain attachments.
func (s *Store) SaveAll(ctx context.Context, files []IncomingFile) ([]domainattachment.Attachment, error) {
	if len(files) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(s.WorkspaceDir) == "" {
		return nil, fmt.Errorf("workspace dir is required")
	}
	limits := s.Limits
	if limits.MaxFileBytes <= 0 {
		limits.MaxFileBytes = domainattachment.DefaultLimits.MaxFileBytes
	}
	if limits.MaxTotalBytes <= 0 {
		limits.MaxTotalBytes = domainattachment.DefaultLimits.MaxTotalBytes
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	newID := randomID
	if s.NewID != nil {
		newID = s.NewID
	}

	var total int64
	out := make([]domainattachment.Attachment, 0, len(files))
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if f.Reader == nil {
			return nil, fmt.Errorf("attachment %q reader is nil", f.Filename)
		}
		if closer, ok := f.Reader.(io.Closer); ok {
			defer closer.Close()
		}
		data, err := io.ReadAll(io.LimitReader(f.Reader, limits.MaxFileBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read attachment %q: %w", f.Filename, err)
		}
		if int64(len(data)) > limits.MaxFileBytes {
			return nil, fmt.Errorf("attachment %q exceeds max file size", f.Filename)
		}
		total += int64(len(data))
		if total > limits.MaxTotalBytes {
			return nil, fmt.Errorf("attachments exceed max total size")
		}

		contentType := strings.TrimSpace(f.ContentType)
		if contentType == "" {
			contentType = http.DetectContentType(data)
		}
		kind, ok := domainattachment.KindFromContentType(contentType)
		if !ok {
			if fallbackKind, fallbackOK := domainattachment.KindFromFilename(f.Filename); fallbackOK {
				kind = fallbackKind
			} else {
				return nil, fmt.Errorf("unsupported attachment content type %q", contentType)
			}
		}

		id := strings.TrimSpace(newID())
		if id == "" {
			id = randomID()
		}
		filename := domainattachment.SafeFilename(f.Filename)
		dir := filepath.Join(s.WorkspaceDir, "viewer_uploads", now().Format("20060102"), "viewer", id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create attachment dir: %w", err)
		}
		path := filepath.Join(dir, filename)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, fmt.Errorf("write attachment: %w", err)
		}
		sum := sha256.Sum256(data)
		extractedText, extractionError, extractionTruncated := extractAttachmentText(filename, contentType, data)
		securityWarnings := domainsecurity.DetectPromptInjectionWarnings(extractedText)
		out = append(out, domainattachment.Attachment{
			ID:                  id,
			Kind:                kind,
			Filename:            filename,
			ContentType:         contentType,
			SizeBytes:           int64(len(data)),
			Path:                path,
			SHA256:              hex.EncodeToString(sum[:]),
			ExtractedText:       extractedText,
			ExtractionError:     extractionError,
			ExtractionTruncated: extractionTruncated,
			SecurityWarnings:    securityWarnings,
			Data:                data,
		})
	}
	return out, nil
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
