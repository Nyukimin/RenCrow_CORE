package main

import (
	"context"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/personrelatedcatalog"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	personRelatedDailySourceID          = "runtime.person-related-workers"
	personRelatedCollectionDefinitionID = "person_related.collection"
	personRelatedSummaryDefinitionID    = "person_related.summary"
	personRelatedIdentityDefinitionID   = "person_related.identity"
)

func registerRuntimePersonRelatedDailySource(
	deps *Dependencies,
	cfg *config.Config,
	collection *runtimePersonRelatedCollectionWorker,
	summary *runtimePersonRelatedSummaryWorker,
	identity *runtimePersonRelatedIdentityWorker,
) {
	if deps == nil {
		return
	}
	definitions := []taskcenter.DailyDefinitionDescriptor{
		personRelatedDailyDefinition(personRelatedCollectionDefinitionID, "person_related.collection", "Related work catalog collection", "person_related.collection", "person_catalog_collection_worker", "config.person_related_catalog.provider_url"),
		personRelatedDailyDefinition(personRelatedSummaryDefinitionID, "person_related.summary", "Related work summary worker", "person_related.summary", "person_catalog_summary_worker", "config.person_related_catalog.summary_worker"),
		personRelatedDailyDefinition(personRelatedIdentityDefinitionID, "person_related.identity", "Person identity mapping worker", "person_related.identity", "person_identity_worker", "config.person_related_catalog.identity_mapping"),
	}
	descriptor := taskcenter.DailySourceDescriptor{SourceID: personRelatedDailySourceID, Definitions: definitions}
	registerTaskCenterDailySource(deps, newRuntimeDailySource(descriptor, func(ctx context.Context, _ tool.ToolExecutionScope) (taskcenter.DailySourceSnapshot, error) {
		if err := ctx.Err(); err != nil {
			return taskcenter.DailySourceSnapshot{}, err
		}
		now := time.Now().UTC()
		providerURLConfigured := cfg != nil && strings.TrimSpace(cfg.PersonRelatedCatalog.ProviderURL) != ""
		collectionObservation, collectionIssue := personRelatedCollectionObservation(
			ctx, collection, cfg != nil, providerURLConfigured,
			deps.personRelatedCollectionCancel != nil,
		)
		summaryObservation := personRelatedQueueWorkerObservation(
			personRelatedSummaryDefinitionID, "person_related.summary",
			cfg != nil, providerURLConfigured, summary != nil, deps.personRelatedSummaryCancel != nil,
			cfg != nil && cfg.PersonRelatedCatalog.SummaryWorker.Enabled != nil && !*cfg.PersonRelatedCatalog.SummaryWorker.Enabled,
			personRelatedSummaryInterval(summary), "config.person_related_catalog.summary_worker.interval",
		)
		identityObservation := personRelatedQueueWorkerObservation(
			personRelatedIdentityDefinitionID, "person_related.identity",
			cfg != nil, providerURLConfigured, identity != nil, deps.personRelatedIdentityCancel != nil,
			cfg != nil && cfg.PersonRelatedCatalog.IdentityMapping.Enabled != nil && !*cfg.PersonRelatedCatalog.IdentityMapping.Enabled,
			personRelatedIdentityInterval(identity), "config.person_related_catalog.identity_mapping.interval",
		)
		issues := []taskcenter.DailyIssue{}
		if collectionIssue {
			issues = append(issues, taskcenter.DailyIssue{OwnerRef: "person_related.collection", Code: taskcenter.DailyIssueSourceReported})
		}
		return taskcenter.DailySourceSnapshot{
			ObservedAt:   now,
			Observations: []taskcenter.DailyObservation{collectionObservation, summaryObservation, identityObservation},
			Issues:       issues,
		}, nil
	}))
}

func personRelatedDailyDefinition(definitionID, kindID, label, ownerRef, resourceType, reference string) taskcenter.DailyDefinitionDescriptor {
	refs := []taskcenter.DailyResourceRef{runtimeDailyResourceRef(personRelatedDailySourceID, ownerRef, "runtime_configuration", reference, "", tool.DataScopePublic, label)}
	switch definitionID {
	case personRelatedCollectionDefinitionID:
		refs = append(refs, runtimeDailyResourceRef(personRelatedDailySourceID, ownerRef, "native_read_port", "personrelatedcatalog.LoadCollectionSweepState", "", tool.DataScopePublic, "Collection sweep checkpoint read"))
	case personRelatedSummaryDefinitionID:
		refs = append(refs, runtimeDailyResourceRef(personRelatedDailySourceID, ownerRef, "native_read_port", "personrelatedcatalog.GetSummaryJob", "", tool.DataScopePublic, "Summary job point read; no job enumeration"))
	case personRelatedIdentityDefinitionID:
		refs = append(refs, runtimeDailyResourceRef(personRelatedDailySourceID, ownerRef, "native_read_port", "personrelatedcatalog.GetIdentityJob", "", tool.DataScopePublic, "Identity job point read; no job enumeration"))
	}
	return taskcenter.DailyDefinitionDescriptor{
		DefinitionID: definitionID,
		KindID:       kindID,
		Label:        label,
		OwnerRef:     ownerRef,
		ResourceType: resourceType,
		DataScope:    tool.DataScopePublic,
		Implementation: runtimeDailyAxis(
			taskcenter.DailyImplementationImplemented,
			"cmd.rencrow/person-related-worker",
			"job_history_enumeration_not_exposed",
		),
		SourceRefs: refs,
	}
}

func personRelatedCollectionObservation(ctx context.Context, worker *runtimePersonRelatedCollectionWorker, configKnown, providerConfigured, workerStarted bool) (taskcenter.DailyObservation, bool) {
	const sourceRef = "personrelatedcatalog.LoadCollectionSweepState"
	configured := runtimeDailyAxis(taskcenter.DailyConfiguredUnknown, "config.person_related_catalog.provider_url", "configuration_unavailable")
	if configKnown {
		if providerConfigured && worker != nil {
			configured = runtimeDailyAxis(taskcenter.DailyConfiguredConfigured, "config.person_related_catalog.provider_url", "")
		} else {
			configured = runtimeDailyAxis(taskcenter.DailyConfiguredUnconfigured, "config.person_related_catalog.provider_url", "provider_or_worker_configuration_missing")
		}
	}
	effective := personRelatedWorkerEffective(configKnown, providerConfigured, worker != nil, workerStarted, "person_related.collection")
	timing := personRelatedWorkerTiming(runtimePersonCollectionCycle, "runtime.routine.person_related.collection_cycle")
	observation := personRelatedWorkerObservationBase(personRelatedCollectionDefinitionID, configured, effective, timing, "person_related.collection")
	observation.NextRunAt = runtimeDailyUnknown[time.Time](sourceRef, "sweep_checkpoint_not_read")
	if worker == nil || strings.TrimSpace(worker.hobbyGraphPath) == "" {
		observation.Observed = runtimeDailyAxis(taskcenter.DailyObservedNotObserved, sourceRef, "collection_checkpoint_owner_unavailable")
		return observation, false
	}
	db, err := openRuntimePersonRelatedCatalogReadOnly(worker.hobbyGraphPath)
	if err != nil {
		observation.Observed = runtimeDailyAxis(taskcenter.DailyObservedUnavailable, sourceRef, "collection_checkpoint_read_unavailable")
		return observation, true
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	state, err := personrelatedcatalog.LoadCollectionSweepState(ctx, db)
	if err != nil {
		observation.Observed = runtimeDailyAxis(taskcenter.DailyObservedUnavailable, sourceRef, "collection_checkpoint_read_unavailable")
		return observation, true
	}
	observation.Observed = runtimeDailyAxis(taskcenter.DailyObservedAvailable, sourceRef, "")
	if !state.NextCycleAt.IsZero() {
		observation.NextRunAt = runtimeDailyKnown(state.NextCycleAt.UTC(), sourceRef+".next_cycle_at")
	} else {
		observation.NextRunAt = runtimeDailyUnknown[time.Time](sourceRef, "next_cycle_not_initialized")
	}
	return observation, false
}

func personRelatedQueueWorkerObservation(
	definitionID, ownerRef string,
	configKnown, providerConfigured, workerPrepared, workerStarted, explicitlyDisabled bool,
	interval time.Duration,
	intervalSource string,
) taskcenter.DailyObservation {
	configured := runtimeDailyAxis(taskcenter.DailyConfiguredUnknown, ownerRef, "configuration_unavailable")
	if configKnown {
		switch {
		case explicitlyDisabled:
			configured = runtimeDailyAxis(taskcenter.DailyConfiguredDisabled, ownerRef, "worker_disabled")
		case !providerConfigured || !workerPrepared:
			configured = runtimeDailyAxis(taskcenter.DailyConfiguredUnconfigured, ownerRef, "provider_or_worker_configuration_missing")
		default:
			configured = runtimeDailyAxis(taskcenter.DailyConfiguredConfigured, ownerRef, "")
		}
	}
	effective := personRelatedWorkerEffective(configKnown, providerConfigured, workerPrepared, workerStarted, ownerRef)
	if explicitlyDisabled {
		effective = runtimeDailyAxis(taskcenter.DailyEffectiveDisabled, ownerRef, "worker_disabled")
	}
	timing := personRelatedWorkerTiming(interval, intervalSource)
	observation := personRelatedWorkerObservationBase(definitionID, configured, effective, timing, ownerRef)
	observation.NextRunAt = runtimeDailyUnknown[time.Time](ownerRef, "poller_phase_and_due_job_not_exposed")
	observation.Observed = runtimeDailyAxis(taskcenter.DailyObservedNotObserved, ownerRef, "job_state_enumeration_not_exposed")
	return observation
}

func personRelatedWorkerEffective(configKnown, providerConfigured, workerPrepared, workerStarted bool, ownerRef string) taskcenter.DailyAxis {
	axis := runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, ownerRef, "configuration_unavailable")
	switch {
	case !configKnown:
	case !providerConfigured:
		axis = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, ownerRef, "provider_unavailable")
	case !workerPrepared || !workerStarted:
		axis = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, ownerRef, "worker_not_started")
	default:
		axis = runtimeDailyAxis(taskcenter.DailyEffectiveEnabled, ownerRef, "runtime_worker_started")
	}
	return axis
}

func personRelatedWorkerTiming(interval time.Duration, intervalSource string) taskcenter.DailyTimingProjection {
	timing := runtimeDailyTimingUnsupported(intervalSource, "worker_interval_is_not_a_per_job_recurrence")
	timing.Timezone = runtimeDailyNone[string](intervalSource, "interval_clock_is_timezone_independent")
	timing.ScheduledStartAt = runtimeDailyNone[time.Time](intervalSource, "interval_worker_has_no_fixed_start")
	timing.NotBefore = runtimeDailyUnknown[time.Time](intervalSource, "per_job_due_time_not_enumerated")
	timing.AllowedWindows = runtimeDailyNone[[]modulecore.TaskTimeWindow](intervalSource, "no_time_window_gate")
	timing.DeadlineAt = runtimeDailyUnknown[time.Time](intervalSource, "job_deadline_not_exposed")
	timing.EstimatedDurationSeconds = runtimeDailyUnsupported[int64](intervalSource, "worker_duration_not_estimated")
	if interval > 0 {
		seconds := int64(interval / time.Second)
		recurrence := modulecore.TaskRecurrence{Frequency: "interval", IntervalSeconds: &seconds}
		timing.Recurrence = runtimeDailyKnown(recurrence, intervalSource)
	}
	return timing
}

func personRelatedWorkerObservationBase(definitionID string, configured, effective taskcenter.DailyAxis, timing taskcenter.DailyTimingProjection, sourceRef string) taskcenter.DailyObservation {
	return taskcenter.DailyObservation{
		DefinitionID:    definitionID,
		Configured:      configured,
		Effective:       effective,
		Observed:        runtimeDailyAxis(taskcenter.DailyObservedNotObserved, sourceRef, "job_state_not_observed"),
		Timing:          timing,
		NextRunAt:       runtimeDailyUnknown[time.Time](sourceRef, "next_run_not_exposed"),
		TargetCount:     runtimeDailyUnknown[int](sourceRef, "target_count_not_exposed"),
		SourceRefs:      []taskcenter.DailyResourceRef{},
		NativeRefs:      []taskcenter.DailyResourceRef{},
		NativeRefCount:  runtimeDailyUnknown[int](sourceRef, "native_job_list_not_exposed"),
		LatestEvidence:  runtimeDailyUnknownEvidence(sourceRef, "durable_run_receipt_not_projected"),
		Occurrences:     []taskcenter.DailyOccurrence{},
		OccurrenceCount: runtimeDailyUnknown[int](sourceRef, "durable_job_state_enumeration_not_exposed"),
		Issues:          []taskcenter.DailyIssue{},
	}
}

func personRelatedSummaryInterval(worker *runtimePersonRelatedSummaryWorker) time.Duration {
	if worker == nil {
		return 0
	}
	return worker.Interval()
}

func personRelatedIdentityInterval(worker *runtimePersonRelatedIdentityWorker) time.Duration {
	if worker == nil {
		return 0
	}
	return worker.Interval()
}
