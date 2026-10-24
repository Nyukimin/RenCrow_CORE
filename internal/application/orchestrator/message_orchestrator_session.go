package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type canonicalSessionRepository interface {
	LoadOrCreateCanonical(context.Context, string, conversation.ChannelAddress, time.Time) (*session.Session, bool, error)
}

type canonicalSessionOwnerRepository interface {
	LoadCanonicalForOwner(context.Context, string, string, conversation.ChannelAddress) (session.CanonicalSessionOwnerResolution, error)
	LoadOrCreateCanonicalForOwner(context.Context, string, conversation.ChannelAddress, string, time.Time) (session.CanonicalSessionOwnerResolution, error)
}

type canonicalSessionBindingGenerationRepository interface {
	BindingGeneration(context.Context, conversation.SessionBinding) (conversation.BindingGeneration, error)
}

type canonicalSessionBindingUseRepository interface {
	UseBinding(context.Context, conversation.SessionBinding, func() error) error
}

// sessionBoundMioPublicationQualification composes the retained private source
// generation with the exact canonical Session binding at each writer claim.
type sessionBoundMioPublicationQualification struct {
	retained conversation.PreparedChatTurnPublicationGuard
	repo     canonicalSessionBindingUseRepository
	ctx      context.Context
	binding  conversation.SessionBinding
}

func (q *sessionBoundMioPublicationQualification) Use(claim func() error) error {
	if q == nil || q.retained == nil || q.repo == nil || claim == nil {
		return fmt.Errorf("private Mio publication qualification is unavailable")
	}
	return q.retained.Use(func() error {
		return q.repo.UseBinding(q.ctx, q.binding, claim)
	})
}

func (q *sessionBoundMioPublicationQualification) Retain() conversation.PreparedChatTurnPublicationGuard {
	if q == nil || q.retained == nil {
		return nil
	}
	retained := q.retained.Retain()
	if retained == nil {
		return nil
	}
	return &sessionBoundMioPublicationQualification{
		retained: retained, repo: q.repo, ctx: q.ctx, binding: q.binding,
	}
}

func (q *sessionBoundMioPublicationQualification) Release() {
	if q != nil && q.retained != nil {
		q.retained.Release()
	}
}

type canonicalSessionOwnerContextKey struct{}

type canonicalSessionOwnerContextValue struct {
	ownerID string
}

func withBoundCanonicalSessionOwner(ctx context.Context, ownerID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, canonicalSessionOwnerContextKey{}, canonicalSessionOwnerContextValue{ownerID: ownerID})
}

func boundCanonicalSessionOwnerFromContext(ctx context.Context) (string, bool) {
	binding, ok := conversation.SessionBindingFromContext(ctx)
	if !ok || binding.Audience() != conversation.SessionAudiencePrivate {
		return "", false
	}
	return binding.OwnerID(), true
}

type messageSessionLifecycle struct {
	sessionRepo SessionRepository
}

func newMessageSessionLifecycle(sessionRepo SessionRepository) *messageSessionLifecycle {
	return &messageSessionLifecycle{sessionRepo: sessionRepo}
}

func (l *messageSessionLifecycle) LoadForRequest(ctx context.Context, req ProcessMessageRequest) (*session.Session, error) {
	sess, err := l.load(ctx, req.SessionID)
	if err != nil {
		log.Printf("[MessageOrch] ProcessMessage ERROR: failed to load session: %v", err)
		return nil, fmt.Errorf("failed to load session: %w", err)
	}
	if sess == nil {
		return nil, fmt.Errorf("failed to load session: repository returned no Session")
	}
	log.Printf("[MessageOrch] Session loaded: %s", sess.ID())
	return sess, nil
}

func (l *messageSessionLifecycle) ResolveForRequest(ctx context.Context, req ProcessMessageRequest, now time.Time) (*session.Session, ProcessMessageRequest, error) {
	_, ownerScoped, err := canonicalSessionOwnerFromContext(ctx)
	if err != nil {
		return nil, req, err
	}
	if ownerScoped {
		return nil, req, fmt.Errorf("owner-scoped session resolution requires an owner-binding caller")
	}
	sess, resolved, _, err := l.resolvePublicForRequest(ctx, req, now)
	return sess, resolved, err
}

func (l *messageSessionLifecycle) ResolveForRequestWithOwner(ctx context.Context, req ProcessMessageRequest, now time.Time) (*session.Session, ProcessMessageRequest, string, error) {
	ownerID, ownerScoped, err := canonicalSessionOwnerFromContext(ctx)
	if err != nil {
		return nil, req, "", err
	}
	if ownerScoped {
		return l.resolveForOwner(ctx, req, ownerID, now)
	}
	return l.resolvePublicForRequest(ctx, req, now)
}

func (l *messageSessionLifecycle) resolvePublicForRequest(ctx context.Context, req ProcessMessageRequest, now time.Time) (*session.Session, ProcessMessageRequest, string, error) {
	address, err := conversation.NewChannelAddress(req.Channel, req.ChatID)
	if err != nil {
		return nil, req, "", fmt.Errorf("invalid ChannelAddress: %w", err)
	}
	if req.SessionID != "" {
		sess, err := l.LoadForRequest(ctx, req)
		if err != nil {
			return nil, req, "", err
		}
		if err := validatePublicCanonicalSession(sess, req.SessionID, address); err != nil {
			return nil, req, "", err
		}
		return sess, req, "", nil
	}
	repo, ok := l.sessionRepo.(canonicalSessionRepository)
	if !ok {
		return nil, req, "", fmt.Errorf("canonical session repository is unavailable")
	}
	sess, _, err := repo.LoadOrCreateCanonical(ctx, now.UTC().Format("2006-01-02"), address, now.UTC())
	if err != nil {
		return nil, req, "", fmt.Errorf("resolve canonical session: %w", err)
	}
	if err := validatePublicCanonicalSession(sess, "", address); err != nil {
		return nil, req, "", err
	}
	req.SessionID = sess.ID()
	return sess, req, "", nil
}

func validatePublicCanonicalSession(sess *session.Session, requestedSessionID string, address conversation.ChannelAddress) error {
	if sess == nil {
		return fmt.Errorf("public canonical session resolution returned no Session")
	}
	if sess.OwnerID() != "" {
		return fmt.Errorf("public session resolution rejected an owner-bound Session")
	}
	if err := modulecore.SessionID(sess.ID()).Validate(); err != nil || requestedSessionID != "" && sess.ID() != requestedSessionID || sess.ChannelAddress() != address {
		return fmt.Errorf("public canonical session resolution returned a conflicting identity")
	}
	return nil
}

func bindingForResolvedProcessSession(sess *session.Session, req ProcessMessageRequest, ownerID string, generation conversation.BindingGeneration) (conversation.SessionBinding, error) {
	if sess == nil {
		return conversation.SessionBinding{}, fmt.Errorf("cannot bind an unresolved Session")
	}
	address, err := conversation.NewChannelAddress(req.Channel, req.ChatID)
	if err != nil {
		return conversation.SessionBinding{}, fmt.Errorf("invalid resolved Session ChannelAddress: %w", err)
	}
	if req.SessionID == "" || sess.ID() != req.SessionID || sess.ChannelAddress() != address {
		return conversation.SessionBinding{}, fmt.Errorf("resolved Session identity does not match the ProcessMessage request")
	}
	if err := session.ValidatePublicationIncarnation(sess.PublicationIncarnation()); err != nil {
		return conversation.SessionBinding{}, fmt.Errorf("resolved Session publication incarnation is invalid: %w", err)
	}
	audience := conversation.SessionAudiencePublic
	if ownerID == "" {
		if sess.OwnerID() != "" {
			return conversation.SessionBinding{}, fmt.Errorf("public Session binding rejected an owner-bound Session")
		}
	} else {
		if err := session.ValidateOwnerID(ownerID); err != nil || sess.OwnerID() != ownerID {
			return conversation.SessionBinding{}, fmt.Errorf("private Session binding owner does not match the resolved Session")
		}
		audience = conversation.SessionAudiencePrivate
	}
	return conversation.NewSessionBindingWithPublicationIncarnation(
		modulecore.SessionID(sess.ID()), address, audience, ownerID, generation, sess.PublicationIncarnation(),
	)
}

func canonicalSessionOwnerFromContext(ctx context.Context) (string, bool, error) {
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found {
		return "", false, nil
	}
	if err := scope.Validate(); err != nil {
		return "", false, fmt.Errorf("invalid ToolExecutionScope for session resolution: %w", err)
	}
	if !scope.Allows(domaintool.DataScopeUser) {
		return "", false, nil
	}
	userProof, verified := domaintool.AuthenticatedHTTPUserScopeFromContext(ctx)
	if !verified || userProof.AuthenticatedUserID != scope.AuthenticatedUserID {
		return "", false, fmt.Errorf("owner-scoped Session resolution requires a matching authenticated HTTP User proof")
	}
	if err := session.ValidateOwnerID(userProof.AuthenticatedUserID); err != nil {
		return "", false, fmt.Errorf("invalid authenticated Session owner: %w", err)
	}
	return userProof.AuthenticatedUserID, true, nil
}

// admitPrivateMioAnswerDelta serializes one bounded transient queue admission
// with canonical Session binding revocation. The domain Agent has already
// called this only inside its prepared-turn Guard.Use; this method acquires
// SessionRepo.UseBinding exactly once for admission and returns a consumer
// revalidator that reacquires the same pair immediately before RAM fanout.
func (o *MessageOrchestrator) admitPrivateMioAnswerDelta(ctx context.Context, req ProcessMessageRequest, delta mioAnswerDelta, guard domainagent.MioTurnStreamGuardUse) (func(bool), error) {
	if o == nil || o.sessions == nil || o.sessions.sessionRepo == nil || req.mioAnswerDeltaSink == nil || req.mioAnswerDeltaSink.admit == nil {
		return nil, fmt.Errorf("private Mio answer delta admission is unavailable")
	}
	if req.Channel != "viewer" || req.To != "mio" || req.OperationSource != "RenCrow_CMD" {
		return nil, fmt.Errorf("private Mio answer delta is not an authenticated CMD Mio request")
	}
	ownerID, ownerScoped, err := canonicalSessionOwnerFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if !ownerScoped || ownerID == "" {
		return nil, fmt.Errorf("private Mio answer delta requires an authenticated HTTP User")
	}
	binding, ok := conversation.SessionBindingFromContext(ctx)
	if !ok || binding.ValidateForPublication() != nil || binding.Audience() != conversation.SessionAudiencePrivate ||
		binding.OwnerID() != ownerID || binding.BindingGeneration() == conversation.BindingGenerationUnavailable ||
		string(binding.SessionID()) != req.SessionID {
		return nil, fmt.Errorf("private Mio answer delta Session binding does not match its authenticated owner")
	}
	expectedAddress, err := conversation.NewChannelAddress(req.Channel, req.ChatID)
	if err != nil || binding.ChannelAddress() != expectedAddress {
		return nil, fmt.Errorf("private Mio answer delta ChannelAddress does not match its canonical Session binding")
	}
	if guard.OwnerID != ownerID || guard.SourceVersion == "" || guard.BindingGeneration != binding.BindingGeneration() || guard.Use == nil || guard.Retain == nil {
		return nil, fmt.Errorf("prepared Mio guard does not match the canonical private Session binding")
	}
	if delta.TurnID != req.TurnID || delta.TraceID != req.TraceID || delta.SessionID != req.SessionID || delta.MessageID != req.AgentMessageID ||
		modulecore.TurnID(delta.TurnID).Validate() != nil || modulecore.TraceID(delta.TraceID).Validate() != nil ||
		modulecore.TaskID(delta.TaskID).Validate() != nil || modulecore.RunID(delta.RunID).Validate() != nil ||
		modulecore.SessionID(delta.SessionID).Validate() != nil || modulecore.MessageID(delta.MessageID).Validate() != nil {
		return nil, fmt.Errorf("private Mio answer delta does not match the active canonical turn identity")
	}
	useRepo, ok := o.sessions.sessionRepo.(canonicalSessionBindingUseRepository)
	if !ok {
		return nil, fmt.Errorf("canonical Session binding admission is unavailable")
	}
	var finish func(bool)
	var qualification *sessionBoundMioPublicationQualification
	err = useRepo.UseBinding(ctx, binding, func() error {
		retained, retainErr := guard.Retain()
		if retainErr != nil {
			return retainErr
		}
		if retained == nil {
			return errors.New("prepared Mio delta guard returned an empty qualification")
		}
		qualification = &sessionBoundMioPublicationQualification{
			retained: retained, repo: useRepo, ctx: context.WithoutCancel(ctx), binding: binding,
		}
		var admissionErr error
		admitted := MioAnswerDelta{
			TurnID: delta.TurnID, TraceID: delta.TraceID, TaskID: delta.TaskID, RunID: delta.RunID,
			SessionID: delta.SessionID, MessageID: delta.MessageID, Text: delta.Text,
		}
		queueFinish, admissionErr := req.mioAnswerDeltaSink.admit(admitted, qualification)
		if admissionErr == nil && queueFinish == nil {
			admissionErr = errors.New("private Mio delta admission returned no completion ticket")
		}
		if admissionErr != nil {
			// The domain Agent invokes this helper from inside Guard.Use. Return
			// cleanup as the completion ticket so its caller releases the retained
			// qualification only after that outer guard has unwound.
			finish = func(bool) {
				if queueFinish != nil {
					queueFinish(false)
				}
				qualification.Release()
			}
			return admissionErr
		}
		finish = queueFinish
		return nil
	})
	if err != nil && qualification != nil && finish == nil {
		// A repository implementation may reject after entering its callback.
		// Keep the release outside both UseBinding and the caller's Guard.Use.
		finish = func(bool) { qualification.Release() }
	}
	return finish, err
}

// newPrivateMioGenerateRequestAdmission composes the prepared-turn and exact
// canonical Session binding checks for one outbound Mio generation. Its
// callback performs only short in-memory admission under both owner guards;
// GatewayProvider releases it before HTTP dispatch.
func (o *MessageOrchestrator) newPrivateMioGenerateRequestAdmission(
	ctx context.Context,
	req ProcessMessageRequest,
	guard conversation.PreparedChatTurnGuard,
) (*domainllm.GenerateRequestAdmission, error) {
	if o == nil || ctx == nil || guard == nil || o.sessions == nil || o.sessions.sessionRepo == nil {
		return nil, fmt.Errorf("private Mio generation admission is unavailable")
	}
	if req.Channel != "viewer" || req.To != "mio" || req.OperationSource != "RenCrow_CMD" {
		return nil, fmt.Errorf("private Mio generation admission requires an authenticated CMD request to Mio")
	}
	ownerID, ownerScoped, err := canonicalSessionOwnerFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if !ownerScoped || ownerID == "" || guard.OwnerID() != ownerID || strings.TrimSpace(guard.SourceVersion()) == "" {
		return nil, fmt.Errorf("private Mio generation requires the authenticated prepared User source")
	}
	binding, ok := conversation.SessionBindingFromContext(ctx)
	if !ok || binding.ValidateForPublication() != nil || binding.Audience() != conversation.SessionAudiencePrivate ||
		binding.OwnerID() != ownerID || binding.BindingGeneration() == conversation.BindingGenerationUnavailable ||
		binding.BindingGeneration() != guard.BindingGeneration() || string(binding.SessionID()) != req.SessionID {
		return nil, fmt.Errorf("private Mio generation Session binding does not match its prepared turn")
	}
	expectedAddress, err := conversation.NewChannelAddress(req.Channel, req.ChatID)
	if err != nil || binding.ChannelAddress() != expectedAddress {
		return nil, fmt.Errorf("private Mio generation ChannelAddress does not match its canonical Session binding")
	}
	if modulecore.TurnID(req.TurnID).Validate() != nil || modulecore.TraceID(req.TraceID).Validate() != nil ||
		modulecore.TaskID(req.RootTaskID).Validate() != nil || modulecore.SessionID(req.SessionID).Validate() != nil ||
		modulecore.MessageID(req.AgentMessageID).Validate() != nil {
		return nil, fmt.Errorf("private Mio generation request identity is invalid")
	}
	useRepo, ok := o.sessions.sessionRepo.(canonicalSessionBindingUseRepository)
	if !ok {
		return nil, fmt.Errorf("canonical Session binding admission is unavailable")
	}
	return domainllm.NewGenerateRequestAdmission(func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		admitted := false
		err := guard.Use(func() error {
			return useRepo.UseBinding(ctx, binding, func() error {
				admitted = true
				return nil
			})
		})
		if err != nil {
			return err
		}
		if !admitted {
			return fmt.Errorf("private Mio generation admission did not reach the Session owner")
		}
		return nil
	}), nil
}

// publishPrivateMioFinalResponse admits the canonical answer while the
// prepared-turn source and exact Session binding are guarded. It returns a
// separate retained qualification for per-chunk TTS checks; the relay takes
// ownership of the qualification passed with the canonical event.
func (o *MessageOrchestrator) publishPrivateMioFinalResponse(
	ctx context.Context,
	req ProcessMessageRequest,
	response string,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	guard conversation.PreparedChatTurnGuard,
) (conversation.PreparedChatTurnPublicationGuard, error) {
	if o == nil || o.events == nil || o.sessions == nil || o.sessions.sessionRepo == nil || guard == nil {
		return nil, fmt.Errorf("guarded Mio final publication is unavailable")
	}
	if req.Channel != "viewer" || req.To != "mio" || req.OperationSource != "RenCrow_CMD" {
		return nil, fmt.Errorf("guarded Mio final publication requires an authenticated CMD Mio request")
	}
	ownerID, ownerScoped, err := canonicalSessionOwnerFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if !ownerScoped || ownerID == "" || guard.OwnerID() != ownerID || guard.SourceVersion() == "" {
		return nil, fmt.Errorf("guarded Mio final publication requires the prepared authenticated User source")
	}
	binding, ok := conversation.SessionBindingFromContext(ctx)
	if !ok || binding.ValidateForPublication() != nil || binding.Audience() != conversation.SessionAudiencePrivate ||
		binding.OwnerID() != ownerID || binding.BindingGeneration() == conversation.BindingGenerationUnavailable ||
		binding.BindingGeneration() != guard.BindingGeneration() || string(binding.SessionID()) != req.SessionID {
		return nil, fmt.Errorf("guarded Mio final Session binding does not match its prepared turn")
	}
	expectedAddress, err := conversation.NewChannelAddress(req.Channel, req.ChatID)
	if err != nil || binding.ChannelAddress() != expectedAddress {
		return nil, fmt.Errorf("guarded Mio final ChannelAddress does not match its canonical Session binding")
	}
	if modulecore.TurnID(req.TurnID).Validate() != nil || modulecore.TraceID(req.TraceID).Validate() != nil ||
		modulecore.SessionID(req.SessionID).Validate() != nil || modulecore.MessageID(req.AgentMessageID).Validate() != nil ||
		taskID.Validate() != nil || runID.Validate() != nil || strings.TrimSpace(response) == "" {
		return nil, fmt.Errorf("guarded Mio final response identity or content is invalid")
	}
	useRepo, ok := o.sessions.sessionRepo.(canonicalSessionBindingUseRepository)
	if !ok {
		return nil, fmt.Errorf("canonical Session binding admission is unavailable")
	}

	var retained conversation.PreparedChatTurnPublicationGuard
	err = guard.Use(func() error {
		return useRepo.UseBinding(ctx, binding, func() error {
			var retainErr error
			retained, retainErr = guard.RetainPublication()
			return retainErr
		})
	})
	if err != nil {
		return nil, fmt.Errorf("retain Mio final publication qualification: %w", err)
	}
	if retained == nil {
		return nil, fmt.Errorf("prepared Mio guard returned an empty final qualification")
	}
	qualification := &sessionBoundMioPublicationQualification{
		retained: retained, repo: useRepo, ctx: context.WithoutCancel(ctx), binding: binding,
	}
	ttsQualification := qualification.Retain()
	if ttsQualification == nil {
		qualification.Release()
		return nil, fmt.Errorf("retain Mio TTS publication qualification")
	}
	admit := func(publish func() error) error {
		if publish == nil {
			return fmt.Errorf("guarded Mio final RAM publisher is unavailable")
		}
		return guard.Use(func() error {
			return useRepo.UseBinding(ctx, binding, publish)
		})
	}
	_, err = o.events.PublishGuardedMioResponse(
		"mio", response, taskID.String(), req.SessionID, req.Channel, req.ChatID, req.AgentMessageID,
		ownerID, binding.PublicationIncarnation(), admit, qualification,
	)
	if err != nil {
		ttsQualification.Release()
		return nil, err
	}
	return ttsQualification, nil
}

func (l *messageSessionLifecycle) resolveForOwner(ctx context.Context, req ProcessMessageRequest, ownerID string, now time.Time) (*session.Session, ProcessMessageRequest, string, error) {
	address, err := conversation.NewChannelAddress(req.Channel, req.ChatID)
	if err != nil {
		return nil, req, "", fmt.Errorf("invalid owner-scoped ChannelAddress: %w", err)
	}
	repo, ok := l.sessionRepo.(canonicalSessionOwnerRepository)
	if !ok {
		return nil, req, "", fmt.Errorf("owner-aware canonical session repository is unavailable")
	}

	var resolution session.CanonicalSessionOwnerResolution
	if req.SessionID != "" {
		resolution, err = repo.LoadCanonicalForOwner(ctx, req.SessionID, ownerID, address)
	} else {
		resolution, err = repo.LoadOrCreateCanonicalForOwner(ctx, now.UTC().Format("2006-01-02"), address, ownerID, now.UTC())
	}
	if err != nil {
		return nil, req, "", fmt.Errorf("resolve owner-bound canonical session: %w", err)
	}
	sess, boundOwnerID, err := validateCanonicalSessionOwnerResolution(resolution, ownerID, address, req.SessionID)
	if err != nil {
		return nil, req, "", err
	}
	req.SessionID = sess.ID()
	return sess, req, boundOwnerID, nil
}

func validateCanonicalSessionOwnerResolution(resolution session.CanonicalSessionOwnerResolution, requestedOwnerID string, address conversation.ChannelAddress, requestedSessionID string) (*session.Session, string, error) {
	switch resolution.Status {
	case session.CanonicalSessionOwnerCreated, session.CanonicalSessionOwnerMatched:
	default:
		return nil, "", fmt.Errorf("owner-bound canonical session resolution rejected: %s", resolution.Status)
	}
	if resolution.Session == nil {
		return nil, "", fmt.Errorf("owner-bound canonical session resolution returned no Session")
	}
	if err := session.ValidateOwnerID(resolution.BoundOwnerID); err != nil || resolution.BoundOwnerID != requestedOwnerID {
		return nil, "", fmt.Errorf("owner-bound canonical session resolution returned a conflicting owner")
	}
	if err := modulecore.SessionID(resolution.Session.ID()).Validate(); err != nil || resolution.Session.OwnerID() != requestedOwnerID ||
		resolution.Session.ChannelAddress() != address || requestedSessionID != "" && resolution.Session.ID() != requestedSessionID {
		return nil, "", fmt.Errorf("owner-bound canonical session resolution returned a conflicting identity")
	}
	return resolution.Session, resolution.BoundOwnerID, nil
}

func (l *messageSessionLifecycle) SaveCompletedTurnInput(ctx context.Context, sess *session.Session, input conversation.TurnInput) error {
	sess.AddTurnInput(input)
	if err := l.sessionRepo.Save(ctx, sess); err != nil {
		log.Printf("[MessageOrch] ProcessMessage ERROR: failed to save session: %v", err)
		return fmt.Errorf("failed to save session: %w", err)
	}
	return nil
}

func (l *messageSessionLifecycle) load(ctx context.Context, id string) (*session.Session, error) {
	return l.sessionRepo.Load(ctx, id)
}
