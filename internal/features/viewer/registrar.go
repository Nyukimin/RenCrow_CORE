package viewer

import (
	"context"
	"net/http"
)

// Dependencies groups feature dependencies supplied by cmd/rencrow.
type Dependencies struct {
	Ports Ports
	Base  BaseRoutes
}

// BaseRoutes groups Viewer base/static route handlers supplied by cmd/rencrow.
// Handler implementations remain in adapter/cmd packages; this registrar owns
// route registration and dependency handoff.
type BaseRoutes struct {
	Page                         http.HandlerFunc
	XBookmarksPage               http.HandlerFunc
	Asset                        http.HandlerFunc
	RuntimeConfig                http.HandlerFunc
	Logo                         http.HandlerFunc
	MioLipSyncClosed             http.HandlerFunc
	MioLipSyncOpen               http.HandlerFunc
	MioPortrait                  http.HandlerFunc
	ShiroPortrait                http.HandlerFunc
	ShiroLipSyncClosed           http.HandlerFunc
	ShiroLipSyncOpen             http.HandlerFunc
	CharacterState               http.HandlerFunc
	CharacterManifest            http.HandlerFunc
	LayeredCharacterState        http.HandlerFunc
	LayeredCharacterMouth        http.HandlerFunc
	LayeredCharacterManifest     http.HandlerFunc
	Live2DCharacter              http.HandlerFunc
	Live2DCharacterEmbed         http.HandlerFunc
	Live2DAsset                  http.HandlerFunc
	Live2DEmotionControl         http.HandlerFunc
	Events                       http.HandlerFunc
	RecipientSelection           http.HandlerFunc
	DebugSystem                  http.HandlerFunc
	DocsSearch                   http.HandlerFunc
	DocsDetail                   http.HandlerFunc
	HistoryRepairJSONL           http.HandlerFunc
	PackageValidation            http.HandlerFunc
	ExtensionHealth              http.HandlerFunc
	OTELExport                   http.HandlerFunc
	ArtifactCleanup              http.HandlerFunc
	AssetsGitStatus              http.HandlerFunc
	ConversationArchiveDatabase  http.HandlerFunc
	GlossaryDatabase             http.HandlerFunc
	ToolRegistryDatabase         http.HandlerFunc
	DataCapabilityCatalog        http.HandlerFunc
	RuntimeCapabilities          http.HandlerFunc
	PersonRelatedCatalog         http.HandlerFunc
	PersonRelatedCatalogPeople   http.HandlerFunc
	MovieCatalog                 http.HandlerFunc
	MovieCatalogFetch            http.HandlerFunc
	MovieCatalogPreference       http.HandlerFunc
	MovieTopicCandidatesGenerate http.HandlerFunc
	HobbyGraph                   http.HandlerFunc
	HobbyGraphBootstrap          http.HandlerFunc
	HobbyGraphInteraction        http.HandlerFunc
	HobbyGraphRelation           http.HandlerFunc
	HobbyMusicImport             http.HandlerFunc
	HobbyTopicCandidatesGenerate http.HandlerFunc
	InvestmentStatus             http.HandlerFunc
	InvestmentNotify             http.HandlerFunc
}

// RegisterRoutes reserves the feature route boundary.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	RegisterBaseRoutes(mux, deps)
}

// RegisterBaseRoutes registers Viewer base/static routes without owning handler
// implementations.
func RegisterBaseRoutes(mux *http.ServeMux, deps Dependencies) {
	base := deps.Base
	registerRoute(mux, "/viewer", base.Page)
	registerRoute(mux, "/viewer/x-bookmarks", base.XBookmarksPage)
	registerRoute(mux, "/viewer/assets/", base.Asset)
	registerRoute(mux, "/viewer/runtime-config", base.RuntimeConfig)
	registerRoute(mux, "/viewer/logo.png", base.Logo)
	registerRoute(mux, "/viewer/mio-lipsync-closed.svg", base.MioLipSyncClosed)
	registerRoute(mux, "/viewer/mio-lipsync-open.svg", base.MioLipSyncOpen)
	registerRoute(mux, "/viewer/mio-portrait.png", base.MioPortrait)
	registerRoute(mux, "/viewer/shiro-portrait.png", base.ShiroPortrait)
	registerRoute(mux, "/viewer/shiro-lipsync-closed.svg", base.ShiroLipSyncClosed)
	registerRoute(mux, "/viewer/shiro-lipsync-open.svg", base.ShiroLipSyncOpen)
	registerRoute(mux, "/viewer/character/state", base.CharacterState)
	registerRoute(mux, "/viewer/character/manifest", base.CharacterManifest)
	registerRoute(mux, "/viewer/character/layered/state", base.LayeredCharacterState)
	registerRoute(mux, "/viewer/character/layered/mouth", base.LayeredCharacterMouth)
	registerRoute(mux, "/viewer/character/layered/manifest", base.LayeredCharacterManifest)
	registerRoute(mux, "/viewer/live2d/character", base.Live2DCharacter)
	registerRoute(mux, "/viewer/live2d/embed", base.Live2DCharacterEmbed)
	registerRoute(mux, "/viewer/live2d/asset", base.Live2DAsset)
	registerRoute(mux, "/viewer/live2d/emotion", base.Live2DEmotionControl)
	registerRoute(mux, "/viewer/events", base.Events)
	registerRoute(mux, "/viewer/recipient-selection", base.RecipientSelection)
	registerRoute(mux, "/viewer/debug/system", base.DebugSystem)
	registerRoute(mux, "/viewer/docs/search", base.DocsSearch)
	registerRoute(mux, "/viewer/docs/detail", base.DocsDetail)
	registerRoute(mux, "/viewer/history-repair/jsonl", base.HistoryRepairJSONL)
	registerRoute(mux, "/viewer/package-validation", base.PackageValidation)
	registerRoute(mux, "/viewer/extensions/health", base.ExtensionHealth)
	registerRoute(mux, "/viewer/otel/export", base.OTELExport)
	registerRoute(mux, "/viewer/artifact-cleanup", base.ArtifactCleanup)
	registerRoute(mux, "/viewer/assets-git/status", base.AssetsGitStatus)
	registerRoute(mux, "/viewer/databases/conversation-archive", base.ConversationArchiveDatabase)
	registerRoute(mux, "/viewer/databases/glossary", base.GlossaryDatabase)
	registerRoute(mux, "/viewer/databases/tool-registry", base.ToolRegistryDatabase)
	registerRoute(mux, "/viewer/databases/catalog", base.DataCapabilityCatalog)
	registerRoute(mux, "/viewer/capabilities", base.RuntimeCapabilities)
	registerRoute(mux, "/viewer/movie-catalog/person-related", base.PersonRelatedCatalog)
	registerRoute(mux, "/viewer/movie-catalog/person-related/people", base.PersonRelatedCatalogPeople)
	registerRoute(mux, "/viewer/movie-catalog", base.MovieCatalog)
	registerRoute(mux, "/viewer/movie-catalog/fetch", base.MovieCatalogFetch)
	registerRoute(mux, "/viewer/movie-catalog/preference", base.MovieCatalogPreference)
	registerRoute(mux, "/viewer/movie-catalog/topic-candidates/generate", base.MovieTopicCandidatesGenerate)
	registerRoute(mux, "/viewer/hobby-graph", base.HobbyGraph)
	registerRoute(mux, "/viewer/hobby-graph/bootstrap", base.HobbyGraphBootstrap)
	registerRoute(mux, "/viewer/hobby-graph/interaction", base.HobbyGraphInteraction)
	registerRoute(mux, "/viewer/hobby-graph/relation", base.HobbyGraphRelation)
	registerRoute(mux, "/viewer/hobby-graph/music/import", base.HobbyMusicImport)
	registerRoute(mux, "/viewer/hobby-graph/topic-candidates/generate", base.HobbyTopicCandidatesGenerate)
	registerRoute(mux, "/viewer/investment/status", base.InvestmentStatus)
	registerRoute(mux, "/viewer/investment/notify", base.InvestmentNotify)
}

func registerRoute(mux *http.ServeMux, pattern string, handler http.HandlerFunc) {
	if mux == nil || pattern == "" || handler == nil {
		return
	}
	mux.HandleFunc(pattern, handler)
}

// StartBackground reserves the feature background-job boundary.
func StartBackground(ctx context.Context, deps Dependencies) error {
	_ = ctx
	_ = deps
	return nil
}
