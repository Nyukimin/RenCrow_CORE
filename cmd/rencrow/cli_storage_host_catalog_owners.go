package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	moviecatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/moviecatalog"
	musiccatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/musiccatalog"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/storagehost"
)

type closeableMovieCatalogOwner struct {
	storagehost.MovieCatalogGroupOwner
	db *sql.DB
}

func (owner *closeableMovieCatalogOwner) Close() error {
	if owner == nil || owner.db == nil {
		return nil
	}
	return owner.db.Close()
}

type closeableHobbyGraphOwner struct {
	storagehost.HobbyGraphGroupOwner
	db *sql.DB
}

func (owner *closeableHobbyGraphOwner) Close() error {
	if owner == nil || owner.db == nil {
		return nil
	}
	return owner.db.Close()
}

func openStorageHostMovieCatalogStore(path string) (storageHostMovieCatalogStore, error) {
	db, err := openStorageHostCatalogDB(path)
	if err != nil {
		return nil, err
	}
	owner, err := moviecatalogapp.NewStorageHostSQLiteStore(context.Background(), db)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("prepare typed Movie Catalog owner: %w", err)
	}
	return &closeableMovieCatalogOwner{MovieCatalogGroupOwner: owner, db: db}, nil
}

func openStorageHostHobbyGraphStore(path string) (storageHostHobbyGraphStore, error) {
	db, err := openStorageHostCatalogDB(path)
	if err != nil {
		return nil, err
	}
	owner, err := musiccatalogapp.NewStorageHostSQLiteStore(context.Background(), db)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("prepare typed Hobby Graph owner: %w", err)
	}
	return &closeableHobbyGraphOwner{HobbyGraphGroupOwner: owner, db: db}, nil
}

func openStorageHostCatalogDB(path string) (*sql.DB, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("catalog database path must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("catalog database is unavailable")
	}
	if info.IsDir() {
		return nil, fmt.Errorf("catalog database path is a directory")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_time_format=sqlite&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open catalog database")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect catalog database")
	}
	return db, nil
}
