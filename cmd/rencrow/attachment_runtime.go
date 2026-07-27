package main

import (
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	attachmentapp "github.com/Nyukimin/RenCrow_CORE/internal/application/attachment"
	domainattachment "github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
)

func newRuntimeAttachmentStore(cfg *config.Config) *attachmentapp.Store {
	store := attachmentapp.NewStore(cfg.WorkspaceDir)
	store.Limits = domainattachment.Limits{
		MaxFileBytes:  domainattachment.DefaultLimits.MaxFileBytes,
		MaxTotalBytes: maxInt64(domainattachment.DefaultLimits.MaxTotalBytes, cfg.Vision.MaxImageBytes+cfg.Vision.MaxVideoBytes),
		MaxImageBytes: cfg.Vision.MaxImageBytes,
		MaxVideoBytes: cfg.Vision.MaxVideoBytes,
	}
	return store
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
