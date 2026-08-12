package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	moviecatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/moviecatalog"
	personrelatedcatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/personrelatedcatalog"
)

type runtimePersonRelatedCatalogCollector struct {
	movieCatalogPath string
	hobbyGraphPath   string
	provider         personrelatedcatalogapp.Collector
}

func prepareRuntimePersonRelatedCatalogCollector(ctx context.Context, movieCatalogPath, hobbyGraphPath, providerURL string) (*runtimePersonRelatedCatalogCollector, error) {
	if strings.TrimSpace(providerURL) == "" {
		return nil, fmt.Errorf("%w: set RENCROW_PERSON_RELATED_CATALOG_PROVIDER_URL", personrelatedcatalogapp.ErrCollectorUnavailable)
	}
	resolvedMovieCatalogPath, err := resolveRuntimePersonRelatedCatalogDatabasePath(movieCatalogPath, "movie catalog")
	if err != nil {
		return nil, err
	}
	resolvedHobbyGraphPath, err := resolveRuntimePersonRelatedCatalogWritableDatabasePath(hobbyGraphPath, "hobby graph")
	if err != nil {
		return nil, err
	}
	return &runtimePersonRelatedCatalogCollector{
		movieCatalogPath: resolvedMovieCatalogPath,
		hobbyGraphPath:   resolvedHobbyGraphPath,
		provider:         personrelatedcatalogapp.NewHTTPCollector(providerURL, 90*time.Second),
	}, nil
}

func (c *runtimePersonRelatedCatalogCollector) Collect(ctx context.Context, personName, category string) (any, error) {
	if c == nil || c.provider == nil || strings.TrimSpace(c.movieCatalogPath) == "" || strings.TrimSpace(c.hobbyGraphPath) == "" {
		return nil, fmt.Errorf("person related catalog collection is unavailable")
	}
	if !validRuntimePersonRelatedCatalogCollectCategory(category) {
		return nil, fmt.Errorf("person related catalog collection category %q is invalid", category)
	}
	movieDB, err := openRuntimeMovieCatalogReadOnly(c.movieCatalogPath)
	if err != nil {
		return nil, fmt.Errorf("open movie catalog read-only for collection: %w", err)
	}
	defer movieDB.Close()
	movieDB.SetMaxOpenConns(1)
	if err := movieDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect movie catalog read-only for collection: %w", err)
	}
	lookup, err := moviecatalogapp.Lookup(movieDB, moviecatalogapp.LookupRequest{Kind: "person", Name: personName, Information: "profile", Limit: 2})
	if err != nil {
		return nil, fmt.Errorf("resolve movie catalog person for collection: %w", err)
	}
	personID, err := resolveRuntimePersonID(personName, lookup)
	if err != nil {
		return nil, err
	}
	eligible, found, err := personrelatedcatalogapp.EligiblePersonByID(ctx, movieDB, personID)
	if err != nil {
		return nil, fmt.Errorf("verify eligible person assessment: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("person %q is not explicitly eligible for collection", personName)
	}

	hobbyDB, err := openRuntimePersonRelatedCatalogReadWrite(c.hobbyGraphPath)
	if err != nil {
		return nil, fmt.Errorf("open hobby graph read-write for collection: %w", err)
	}
	defer hobbyDB.Close()
	hobbyDB.SetMaxOpenConns(1)
	if err := hobbyDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect hobby graph read-write for collection: %w", err)
	}
	if err := personrelatedcatalogapp.EnsureHobbySchema(ctx, hobbyDB); err != nil {
		return nil, fmt.Errorf("prepare hobby graph collection schema: %w", err)
	}
	identityDecision, err := personrelatedcatalogapp.IdentityScheduleDecision(ctx, hobbyDB, eligible.MovieCatalogPersonID)
	if err != nil {
		return nil, fmt.Errorf("resolve exact person identity for collection: %w", err)
	}
	if (category == personrelatedcatalogapp.CategoryAward || category == personrelatedcatalogapp.CategoryNovel) && !identityDecision.Allowed {
		return personrelatedcatalogapp.CollectionPlanResult{
			PlanRevision: personrelatedcatalogapp.CollectionPlanRevision, PersonRefID: "eiga:" + eligible.MovieCatalogPersonID,
			MovieCatalogPersonID: eligible.MovieCatalogPersonID, Category: category,
			Status: personrelatedcatalogapp.CollectionStatusAmbiguous, ReasonCode: identityDecision.Reason,
			StopReason: personrelatedcatalogapp.StopReasonIdentityAmbiguous,
		}, nil
	}
	identityMappings, err := personrelatedcatalogapp.ListPersonIdentityMappings(ctx, hobbyDB, eligible.MovieCatalogPersonID, 20)
	if err != nil {
		return nil, fmt.Errorf("list exact person identities for collection: %w", err)
	}
	personRefID := "eiga:" + eligible.MovieCatalogPersonID
	seedPlan, err := personrelatedcatalogapp.BuildCollectionPlan(personrelatedcatalogapp.PlanRequest{
		PersonRefID: personRefID, MovieCatalogPersonID: eligible.MovieCatalogPersonID, Categories: []string{category},
	})
	if err != nil {
		return nil, fmt.Errorf("build initial collection plan: %w", err)
	}
	now := time.Now().UTC()
	fresh := make([]personrelatedcatalogapp.CollectionAttempt, 0, len(seedPlan.Batches))
	for _, batch := range seedPlan.Batches {
		attempt, ok, lookupErr := personrelatedcatalogapp.LookupFreshCollectionAttempt(ctx, hobbyDB, personRefID, eligible.MovieCatalogPersonID, category, batch.Source, now)
		if lookupErr != nil {
			return nil, fmt.Errorf("load collection attempt for %s: %w", batch.Source, lookupErr)
		}
		if ok {
			fresh = append(fresh, attempt)
		}
	}
	plan, err := personrelatedcatalogapp.BuildCollectionPlan(personrelatedcatalogapp.PlanRequest{
		PersonRefID: personRefID, MovieCatalogPersonID: eligible.MovieCatalogPersonID, Categories: []string{category}, FreshAttempts: fresh, Now: now,
	})
	if err != nil {
		return nil, fmt.Errorf("build collection plan: %w", err)
	}
	result := personrelatedcatalogapp.CollectionPlanResult{
		PlanRevision: plan.PlanRevision, PersonRefID: personRefID, MovieCatalogPersonID: eligible.MovieCatalogPersonID,
		Category: category, StopReason: plan.StopReason, NextSource: plan.NextSource, Attempts: append([]personrelatedcatalogapp.CollectionAttempt(nil), plan.Attempts...),
	}
	if len(plan.Batches) == 0 {
		if len(result.Attempts) > 0 {
			last := result.Attempts[len(result.Attempts)-1]
			result.Status, result.ReasonCode = last.Status, last.ReasonCode
		}
		return result, nil
	}
	for index, batch := range plan.Batches {
		var wikidataQID, wikidataURL, ndlAuthorityURI string
		for _, mapping := range identityMappings {
			if mapping.State != personrelatedcatalogapp.IdentityStatusConfirmed {
				continue
			}
			switch mapping.Authority {
			case "wikidata_qid":
				wikidataQID, wikidataURL = mapping.ExternalID, mapping.CanonicalURL
			case "ndl_authority_uri":
				ndlAuthorityURI = mapping.ExternalID
			}
		}
		collection, collectErr := c.provider.Collect(ctx, personrelatedcatalogapp.CollectionRequest{
			MovieCatalogPersonID: eligible.MovieCatalogPersonID, PersonName: eligible.Name, PersonURL: eligible.URL,
			Category: category, Source: batch.Source, WikidataQID: wikidataQID,
			WikidataCanonicalURL: wikidataURL, NDLAuthorityURI: ndlAuthorityURI,
		})
		if collectErr != nil {
			return nil, fmt.Errorf("collect category %s from provider source %s: %w", category, batch.Source, collectErr)
		}
		if collection.Source != batch.Source {
			return nil, fmt.Errorf("collect category %s: provider returned source %q for requested source %q", category, collection.Source, batch.Source)
		}
		retrievedAt := strings.TrimSpace(collection.RetrievedAt)
		if retrievedAt == "" {
			retrievedAt = time.Now().UTC().Format(time.RFC3339)
		}
		attempt := personrelatedcatalogapp.CollectionAttempt{
			RunID: fmt.Sprintf("collect-%d-%s-%s", time.Now().UTC().UnixNano(), category, batch.Source), Source: batch.Source,
			Category: category, PersonRefID: personRefID, MovieCatalogPersonID: eligible.MovieCatalogPersonID,
			Status: collection.Status, ReasonCode: collection.ReasonCode, Retryable: collection.Retryable,
			RetryAfterSeconds: int(collection.RetryAfter / time.Second), RetrievedAt: retrievedAt, PlanRevision: plan.PlanRevision,
		}
		if collection.Status == personrelatedcatalogapp.CollectionStatusReady {
			imported, importErr := personrelatedcatalogapp.Import(ctx, hobbyDB, collection.Artifact, collection.ArtifactSHA256, collection.ArtifactBytes)
			if importErr != nil {
				return imported, fmt.Errorf("import collected category %s from %s: %w", category, batch.Source, importErr)
			}
			attempt.RunID = imported.RunID
			attempt.ItemCount = imported.ItemCount
			attempt.StopReason = personrelatedcatalogapp.StopReasonEnoughValidatedResults
			result.Status = personrelatedcatalogapp.CollectionStatusReady
			result.StopReason = personrelatedcatalogapp.StopReasonEnoughValidatedResults
			result.NextSource = ""
		} else if collection.Status == personrelatedcatalogapp.CollectionStatusAmbiguous {
			attempt.StopReason = personrelatedcatalogapp.StopReasonIdentityAmbiguous
			result.StopReason = personrelatedcatalogapp.StopReasonIdentityAmbiguous
		} else if index+1 < len(plan.Batches) {
			attempt.NextSource = plan.Batches[index+1].Source
			result.NextSource = attempt.NextSource
		} else {
			attempt.StopReason = personrelatedcatalogapp.StopReasonAllSourcesTerminal
			result.StopReason = personrelatedcatalogapp.StopReasonAllSourcesTerminal
			result.NextSource = ""
		}
		if err := personrelatedcatalogapp.RecordCollectionAttempt(ctx, hobbyDB, attempt); err != nil {
			return nil, fmt.Errorf("record collection attempt for %s: %w", batch.Source, err)
		}
		result.Attempts = append(result.Attempts, attempt)
		result.Status, result.ReasonCode = attempt.Status, attempt.ReasonCode
		if collection.Status == personrelatedcatalogapp.CollectionStatusReady || collection.Status == personrelatedcatalogapp.CollectionStatusAmbiguous {
			return result, nil
		}
	}
	return result, nil
}

func validRuntimePersonRelatedCatalogCollectCategory(category string) bool {
	switch category {
	case personrelatedcatalogapp.CategoryDrama, personrelatedcatalogapp.CategoryAward,
		personrelatedcatalogapp.CategoryMusic, personrelatedcatalogapp.CategoryAnime,
		personrelatedcatalogapp.CategoryNovel, personrelatedcatalogapp.CategoryManga:
		return true
	default:
		return false
	}
}
