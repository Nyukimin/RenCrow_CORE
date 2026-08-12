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
	resolvedHobbyGraphPath, err := resolveRuntimePersonRelatedCatalogDatabasePath(hobbyGraphPath, "hobby graph")
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
	collection, err := c.provider.Collect(ctx, personrelatedcatalogapp.CollectionRequest{
		MovieCatalogPersonID: eligible.MovieCatalogPersonID,
		PersonName:           eligible.Name,
		PersonURL:            eligible.URL,
		Category:             category,
	})
	if err != nil {
		return nil, fmt.Errorf("collect category %s from provider: %w", category, err)
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
	result, err := personrelatedcatalogapp.Import(ctx, hobbyDB, collection.Artifact, collection.ArtifactSHA256, collection.ArtifactBytes)
	if err != nil {
		return result, fmt.Errorf("import collected category %s: %w", category, err)
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
