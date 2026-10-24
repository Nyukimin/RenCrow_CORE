package main

import (
	"container/list"
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/security"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

const ttsAudioAccessRegistryCapacity = 1024

var errTTSAudioAccessRegistration = errors.New("invalid TTS audio access registration")

type ttsAudioAccessRegistration struct {
	resourceKey    string
	authorityKind  ttsAudioAuthorityKind
	sessionBinding conversation.SessionBinding
	publicProof    moduletts.PublicSessionRouteProof
}

type ttsAudioAuthorityKind string

const (
	ttsAudioCanonicalSessionAuthority ttsAudioAuthorityKind = "canonical-session"
	ttsAudioIdleChatPublicAuthority   ttsAudioAuthorityKind = "idlechat-public-playback"
)

// ttsAudioAccessOwner binds each served audio resource to one tagged authority:
// a canonical Session or an exact current IdleChat public playback route.
// Canonical Session visibility remains owned by its repository; public route
// validity remains owned by PublicSessionStore.
type ttsAudioAccessOwner struct {
	outputDir         string
	baseDirValid      bool
	allowedRemoteHost string
	ownerID           string
	ownerToken        []byte
	visibility        *runtimeSessionVisibility
	publicSessions    *moduletts.PublicSessionStore

	mu        sync.RWMutex
	resources map[string]*list.Element
	order     list.List
}

func newTTSAudioAccessOwner(outputDir, ttsBaseURL, ownerID string, ownerToken []byte, visibility *runtimeSessionVisibility) *ttsAudioAccessOwner {
	return newTTSAudioAccessOwnerWithPublicSessions(outputDir, ttsBaseURL, ownerID, ownerToken, visibility, ttsPublicSessions)
}

func newTTSAudioAccessOwnerWithPublicSessions(outputDir, ttsBaseURL, ownerID string, ownerToken []byte, visibility *runtimeSessionVisibility, publicSessions *moduletts.PublicSessionStore) *ttsAudioAccessOwner {
	_, baseDirValid := normalizeLocalTTSAudioBase(outputDir)
	return &ttsAudioAccessOwner{
		outputDir:         outputDir,
		baseDirValid:      baseDirValid,
		allowedRemoteHost: allowedTTSAudioHost(ttsBaseURL),
		ownerID:           ownerID,
		ownerToken:        append([]byte(nil), ownerToken...),
		visibility:        visibility,
		publicSessions:    publicSessions,
		resources:         make(map[string]*list.Element),
	}
}

// RegisterPath binds a local TTS output file to the exact canonical Session
// binding captured when synthesis started. Producers call it after the audio
// artifact is ready and before publishing a chunk that references the file.
func (o *ttsAudioAccessOwner) RegisterPath(binding conversation.SessionBinding, audioPath string) error {
	if o == nil || !o.baseDirValid || !validCanonicalTTSAudioBinding(binding) {
		return errTTSAudioAccessRegistration
	}
	relPath, ok := localTTSAudioRelPath(o.outputDir, audioPath)
	if !ok || hasSpeechEvalPathSegment(relPath) {
		return errTTSAudioAccessRegistration
	}
	return o.registerCanonical(ttsAudioResourceKey(ttsAudioLocalResource, relPath), binding)
}

// RegisterURL binds a same-host Gateway URL to the exact canonical Session
// binding captured when synthesis started. Remote redirects are rejected when
// the URL is later fetched.
func (o *ttsAudioAccessOwner) RegisterURL(binding conversation.SessionBinding, audioURL string) error {
	if o == nil || !validCanonicalTTSAudioBinding(binding) {
		return errTTSAudioAccessRegistration
	}
	canonicalURL, ok := normalizeRegisteredTTSAudioURL(audioURL, o.allowedRemoteHost)
	if !ok {
		return errTTSAudioAccessRegistration
	}
	return o.registerCanonical(ttsAudioResourceKey(ttsAudioRemoteResource, canonicalURL), binding)
}

// RegisterPublicPath binds a local resource to an exact current IdleChat
// route proof. It never interprets the internal TTS Session ID as public.
func (o *ttsAudioAccessOwner) RegisterPublicPath(proof moduletts.PublicSessionRouteProof, audioPath string) error {
	if o == nil || !o.baseDirValid {
		return errTTSAudioAccessRegistration
	}
	relPath, ok := localTTSAudioRelPath(o.outputDir, audioPath)
	if !ok || hasSpeechEvalPathSegment(relPath) {
		return errTTSAudioAccessRegistration
	}
	return o.registerPublic(ttsAudioResourceKey(ttsAudioLocalResource, relPath), proof)
}

// RegisterPublicURL binds a same-host remote resource to an exact current
// IdleChat route proof. Remote redirects remain disabled by the read handler.
func (o *ttsAudioAccessOwner) RegisterPublicURL(proof moduletts.PublicSessionRouteProof, audioURL string) error {
	if o == nil {
		return errTTSAudioAccessRegistration
	}
	canonicalURL, ok := normalizeRegisteredTTSAudioURL(audioURL, o.allowedRemoteHost)
	if !ok {
		return errTTSAudioAccessRegistration
	}
	return o.registerPublic(ttsAudioResourceKey(ttsAudioRemoteResource, canonicalURL), proof)
}

func (o *ttsAudioAccessOwner) registerCanonical(resourceKey string, binding conversation.SessionBinding) error {
	if o == nil || resourceKey == "" || !validCanonicalTTSAudioBinding(binding) || !o.isCanonicalBindingCurrent(context.Background(), binding) {
		return errTTSAudioAccessRegistration
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	entry := ttsAudioAccessRegistration{
		resourceKey: resourceKey, authorityKind: ttsAudioCanonicalSessionAuthority, sessionBinding: binding,
	}
	return o.registerLocked(entry)
}

func validCanonicalTTSAudioBinding(binding conversation.SessionBinding) bool {
	return binding.ValidateForPublication() == nil && binding.BindingGeneration() != conversation.BindingGenerationUnavailable
}

type canonicalTTSAudioBindingGuard interface {
	UseBinding(context.Context, conversation.SessionBinding, func() error) error
}

func (o *ttsAudioAccessOwner) isCanonicalBindingCurrent(ctx context.Context, binding conversation.SessionBinding) bool {
	if o == nil || o.visibility == nil || !validCanonicalTTSAudioBinding(binding) {
		return false
	}
	guard, ok := o.visibility.repository.(canonicalTTSAudioBindingGuard)
	if !ok {
		return false
	}
	return guard.UseBinding(ctx, binding, func() error { return nil }) == nil
}

// isRegistrationCurrent revalidates the authority snapshot immediately before
// a response header or audio chunk is written. It deliberately performs no
// resource I/O and never derives an old registration from a current Session.
func (o *ttsAudioAccessOwner) isRegistrationCurrent(ctx context.Context, registration ttsAudioAccessRegistration) bool {
	if o == nil || ctx == nil || ctx.Err() != nil {
		return false
	}
	switch registration.authorityKind {
	case ttsAudioCanonicalSessionAuthority:
		return o.isCanonicalBindingCurrent(ctx, registration.sessionBinding)
	case ttsAudioIdleChatPublicAuthority:
		return o.publicSessions != nil && o.publicSessions.ValidateCurrentRouteProof(registration.publicProof)
	default:
		return false
	}
}

func (o *ttsAudioAccessOwner) registerPublic(resourceKey string, proof moduletts.PublicSessionRouteProof) error {
	if o == nil || resourceKey == "" || o.publicSessions == nil || !proof.Current {
		return errTTSAudioAccessRegistration
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.publicSessions.ValidateCurrentRouteProof(proof) {
		return errTTSAudioAccessRegistration
	}
	return o.registerLocked(ttsAudioAccessRegistration{
		resourceKey: resourceKey, authorityKind: ttsAudioIdleChatPublicAuthority, publicProof: proof,
	})
}

func (o *ttsAudioAccessOwner) registerLocked(entry ttsAudioAccessRegistration) error {
	resourceKey := entry.resourceKey
	if existing, found := o.resources[resourceKey]; found {
		current, ok := existing.Value.(ttsAudioAccessRegistration)
		if !ok || !sameTTSAudioAccessRegistration(current, entry) {
			return errTTSAudioAccessRegistration
		}
		o.order.MoveToBack(existing)
		return nil
	}
	if o.order.Len() == ttsAudioAccessRegistryCapacity {
		oldest := o.order.Front()
		if oldest != nil {
			registration := oldest.Value.(ttsAudioAccessRegistration)
			delete(o.resources, registration.resourceKey)
			o.order.Remove(oldest)
		}
	}
	o.resources[resourceKey] = o.order.PushBack(entry)
	return nil
}

func sameTTSAudioAccessRegistration(left, right ttsAudioAccessRegistration) bool {
	return left.resourceKey == right.resourceKey && left.authorityKind == right.authorityKind &&
		left.sessionBinding == right.sessionBinding && left.publicProof == right.publicProof
}

func (o *ttsAudioAccessOwner) lookupPathRegistration(relPath string) (ttsAudioAccessRegistration, bool) {
	if o == nil || !o.baseDirValid {
		return ttsAudioAccessRegistration{}, false
	}
	relPath = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(relPath))))
	if relPath == "." || relPath == "" || relPath == ".." || strings.HasPrefix(relPath, "../") || filepath.IsAbs(filepath.FromSlash(relPath)) {
		return ttsAudioAccessRegistration{}, false
	}
	return o.lookup(ttsAudioResourceKey(ttsAudioLocalResource, relPath))
}

const speechEvalAudioRoot = "speech-eval"

// isSpeechEvalArtifactPathAttempt reserves every spelling containing the
// speech-eval path segment for its static artifact authority. Invalid or
// traversing spellings must not fall through to Session-backed lookup.
func isSpeechEvalArtifactPathAttempt(rawPath string) bool {
	for _, segment := range strings.Split(strings.ReplaceAll(rawPath, `\`, "/"), "/") {
		if segment == speechEvalAudioRoot {
			return true
		}
	}
	return false
}

func hasSpeechEvalPathSegment(relPath string) bool {
	return isSpeechEvalArtifactPathAttempt(relPath)
}

// resolveSpeechEvalArtifact admits only the manifest and generated opaque WAV
// basename shapes. It follows no symlink within the dedicated public root and
// confirms the resolved target remains physically beneath that root.
func (o *ttsAudioAccessOwner) resolveSpeechEvalArtifact(rawPath string) (string, bool) {
	if o == nil || !o.baseDirValid || !validSpeechEvalArtifactPath(rawPath) {
		return "", false
	}
	baseDir, ok := normalizeLocalTTSAudioBase(o.outputDir)
	if !ok {
		return "", false
	}
	evalRoot := filepath.Join(baseDir, speechEvalAudioRoot)
	rootInfo, err := os.Lstat(evalRoot)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return "", false
	}
	resolvedBase, err := filepath.EvalSymlinks(baseDir)
	if err != nil {
		return "", false
	}
	resolvedRoot, err := filepath.EvalSymlinks(evalRoot)
	if err != nil {
		return "", false
	}
	relRoot, err := filepath.Rel(resolvedBase, resolvedRoot)
	if err != nil || filepath.Clean(relRoot) != speechEvalAudioRoot {
		return "", false
	}

	parts := strings.Split(rawPath, "/")
	target := evalRoot
	for index, part := range parts[1:] {
		target = filepath.Join(target, part)
		info, err := os.Lstat(target)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", false
		}
		if index < len(parts)-2 {
			if !info.IsDir() {
				return "", false
			}
		} else if !info.Mode().IsRegular() {
			return "", false
		}
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", false
	}
	relTarget, err := filepath.Rel(resolvedRoot, resolvedTarget)
	if err != nil {
		return "", false
	}
	relTarget = filepath.Clean(relTarget)
	if relTarget == ".." || filepath.IsAbs(relTarget) || strings.HasPrefix(relTarget, ".."+string(filepath.Separator)) {
		return "", false
	}
	return resolvedTarget, true
}

func validSpeechEvalArtifactPath(rawPath string) bool {
	if rawPath == speechEvalAudioRoot+"/manifest.json" {
		return true
	}
	parts := strings.Split(rawPath, "/")
	return len(parts) == 3 && parts[0] == speechEvalAudioRoot &&
		validLowercaseHex(parts[1], 32) && validLowercaseHex(strings.TrimSuffix(parts[2], ".wav"), 32) &&
		strings.HasSuffix(parts[2], ".wav") && len(parts[2]) == 36
}

func validLowercaseHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func (o *ttsAudioAccessOwner) lookupURLRegistration(canonicalURL string) (ttsAudioAccessRegistration, bool) {
	if o == nil || canonicalURL == "" {
		return ttsAudioAccessRegistration{}, false
	}
	return o.lookupRegistration(ttsAudioResourceKey(ttsAudioRemoteResource, canonicalURL))
}

// lookupPathRel and lookupURL retain the canonical Session ID lookup shape
// used by existing producer-side diagnostics. Public authority callers use
// the registration variants so the authority tag cannot be lost.
func (o *ttsAudioAccessOwner) lookupPathRel(relPath string) (string, bool) {
	registration, ok := o.lookupPathRegistration(relPath)
	if !ok || registration.authorityKind != ttsAudioCanonicalSessionAuthority {
		return "", false
	}
	return string(registration.sessionBinding.SessionID()), true
}

func (o *ttsAudioAccessOwner) lookupURL(canonicalURL string) (string, bool) {
	registration, ok := o.lookupURLRegistration(canonicalURL)
	if !ok || registration.authorityKind != ttsAudioCanonicalSessionAuthority {
		return "", false
	}
	return string(registration.sessionBinding.SessionID()), true
}

func (o *ttsAudioAccessOwner) lookup(resourceKey string) (ttsAudioAccessRegistration, bool) {
	return o.lookupRegistration(resourceKey)
}

func (o *ttsAudioAccessOwner) lookupRegistration(resourceKey string) (ttsAudioAccessRegistration, bool) {
	if o == nil {
		return ttsAudioAccessRegistration{}, false
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	element, found := o.resources[resourceKey]
	if !found || element == nil {
		return ttsAudioAccessRegistration{}, false
	}
	registration, ok := element.Value.(ttsAudioAccessRegistration)
	if !ok || registration.resourceKey != resourceKey {
		return ttsAudioAccessRegistration{}, false
	}
	if registration.authorityKind == ttsAudioIdleChatPublicAuthority &&
		(o.publicSessions == nil || !o.publicSessions.ValidateCurrentRouteProof(registration.publicProof)) {
		return ttsAudioAccessRegistration{}, false
	}
	return registration, true
}

func (o *ttsAudioAccessOwner) authorize(r *http.Request, registration ttsAudioAccessRegistration) (private bool, status int) {
	if o == nil || r == nil {
		return false, http.StatusNotFound
	}
	switch registration.authorityKind {
	case ttsAudioIdleChatPublicAuthority:
		return false, 0
	case ttsAudioCanonicalSessionAuthority:
		return o.authorizeCanonicalSession(r, registration.sessionBinding)
	default:
		return false, http.StatusNotFound
	}
}

func (o *ttsAudioAccessOwner) authorizeCanonicalSession(r *http.Request, binding conversation.SessionBinding) (private bool, status int) {
	if !validCanonicalTTSAudioBinding(binding) || !o.isCanonicalBindingCurrent(r.Context(), binding) {
		return false, http.StatusNotFound
	}
	private = binding.Audience() == conversation.SessionAudiencePrivate
	if !private && binding.Audience() != conversation.SessionAudiencePublic {
		return false, http.StatusNotFound
	}
	if private && !security.IsDirectLoopbackRequest(r) {
		return true, http.StatusNotFound
	}
	credentialOwnerID, hasCredential, credentialOK := o.authenticatedOwner(r.Header)
	if !credentialOK {
		return private, http.StatusUnauthorized
	}
	contextOwnerID, hasContextOwner, contextErr := viewer.AuthenticatedViewerOwnerID(r.Context())
	if contextErr != nil || (hasContextOwner && (!hasCredential || contextOwnerID != credentialOwnerID)) {
		return private, http.StatusNotFound
	}
	if !private {
		return false, 0
	}
	if !hasCredential {
		return true, http.StatusUnauthorized
	}
	canonicalOwnerID := binding.OwnerID()
	if !validTTSAudioOwnerID(o.ownerID) || canonicalOwnerID != o.ownerID || credentialOwnerID != canonicalOwnerID {
		return true, http.StatusNotFound
	}
	return true, 0
}

func (o *ttsAudioAccessOwner) authenticatedOwner(headers http.Header) (string, bool, bool) {
	values := headers.Values("Authorization")
	if len(values) == 0 {
		return "", false, true
	}
	if len(values) != 1 || o == nil || len(o.ownerToken) == 0 || !validTTSAudioOwnerID(o.ownerID) {
		return "", true, false
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || scheme != "Bearer" || token == "" || strings.IndexFunc(token, func(r rune) bool {
		return unicode.IsSpace(r)
	}) >= 0 {
		return "", true, false
	}
	if !constantTimeAgentOpsTokenEqual(o.ownerToken, []byte(token)) {
		return "", true, false
	}
	return o.ownerID, true, true
}

// UnregisterSession removes every resource registered for a canonical
// Session, for producer cleanup after its artifacts are no longer addressable.
func (o *ttsAudioAccessOwner) UnregisterSession(sessionID string) int {
	if o == nil || !validTTSAudioSessionID(sessionID) {
		return 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	removed := 0
	for element := o.order.Front(); element != nil; {
		next := element.Next()
		registration := element.Value.(ttsAudioAccessRegistration)
		if registration.authorityKind == ttsAudioCanonicalSessionAuthority && string(registration.sessionBinding.SessionID()) == sessionID {
			delete(o.resources, registration.resourceKey)
			o.order.Remove(element)
			removed++
		}
		element = next
	}
	return removed
}

func (o *ttsAudioAccessOwner) UnregisterPath(audioPath string) bool {
	if o == nil || !o.baseDirValid {
		return false
	}
	relPath, ok := localTTSAudioRelPath(o.outputDir, audioPath)
	if !ok {
		return false
	}
	return o.unregister(ttsAudioResourceKey(ttsAudioLocalResource, relPath))
}

func (o *ttsAudioAccessOwner) UnregisterURL(audioURL string) bool {
	if o == nil {
		return false
	}
	canonicalURL, ok := normalizeRegisteredTTSAudioURL(audioURL, o.allowedRemoteHost)
	if !ok {
		return false
	}
	return o.unregister(ttsAudioResourceKey(ttsAudioRemoteResource, canonicalURL))
}

func (o *ttsAudioAccessOwner) unregister(resourceKey string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	element, found := o.resources[resourceKey]
	if !found || element == nil {
		return false
	}
	delete(o.resources, resourceKey)
	o.order.Remove(element)
	return true
}

func (o *ttsAudioAccessOwner) RegisteredResourceCount() int {
	if o == nil {
		return 0
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.order.Len()
}

func validTTSAudioSessionID(sessionID string) bool {
	return sessionID != "" && strings.TrimSpace(sessionID) == sessionID && modulecore.SessionID(sessionID).Validate() == nil
}

func validTTSAudioOwnerID(ownerID string) bool {
	return ownerID != "" && strings.TrimSpace(ownerID) == ownerID && domainsession.ValidateOwnerID(ownerID) == nil
}

func ttsAudioResourceKey(kind ttsAudioResourceKind, resource string) string {
	if kind == "" || resource == "" {
		return ""
	}
	return string(kind) + ":" + resource
}

func normalizeRegisteredTTSAudioURL(rawURL, allowedHost string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil || !parsed.IsAbs() || parsed.Opaque != "" || parsed.User != nil || parsed.Fragment != "" {
		return "", false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}
	if !isAllowedRemoteTTSAudioHost(parsed.Host, allowedHost) || parsed.Hostname() == "" {
		return "", false
	}
	return parsed.String(), true
}
