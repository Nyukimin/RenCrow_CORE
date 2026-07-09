package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/glossary/domain/entity"
)

type fakeGlossaryRepository struct {
	saved      *entity.GlossaryItem
	saveErr    error
	byTerm     *entity.GlossaryItem
	recent     []*entity.GlossaryItem
	byCategory []*entity.GlossaryItem
}

func (f *fakeGlossaryRepository) Save(ctx context.Context, item *entity.GlossaryItem) error {
	f.saved = item
	return f.saveErr
}

func (f *fakeGlossaryRepository) FindByTerm(ctx context.Context, term string) (*entity.GlossaryItem, error) {
	return f.byTerm, nil
}

func (f *fakeGlossaryRepository) FindRecent(ctx context.Context, limit int) ([]*entity.GlossaryItem, error) {
	return f.recent, nil
}

func (f *fakeGlossaryRepository) FindByCategory(ctx context.Context, category string, limit int) ([]*entity.GlossaryItem, error) {
	return f.byCategory, nil
}

func (f *fakeGlossaryRepository) Delete(ctx context.Context, id string) error {
	return nil
}

func TestGlossaryServiceAddAndQueries(t *testing.T) {
	repo := &fakeGlossaryRepository{
		byTerm:     entity.NewGlossaryItem("Mio", "chat agent", "manual", "agent"),
		recent:     []*entity.GlossaryItem{entity.NewGlossaryItem("Shiro", "worker", "manual", "agent")},
		byCategory: []*entity.GlossaryItem{entity.NewGlossaryItem("Aka", "coder", "manual", "agent")},
	}
	svc := NewGlossaryService(repo)

	added, err := svc.AddGlossaryItem(context.Background(), "RenCrow", "assistant", "manual", "project")
	if err != nil {
		t.Fatalf("AddGlossaryItem failed: %v", err)
	}
	if repo.saved != added || added.Term != "RenCrow" || added.Category != "project" {
		t.Fatalf("unexpected saved item: %#v", repo.saved)
	}

	found, err := svc.SearchByTerm(context.Background(), "Mio")
	if err != nil || found.Term != "Mio" {
		t.Fatalf("SearchByTerm = %#v, %v", found, err)
	}
	recent, err := svc.GetRecentGlossary(context.Background(), 1)
	if err != nil || len(recent) != 1 || recent[0].Term != "Shiro" {
		t.Fatalf("GetRecentGlossary = %#v, %v", recent, err)
	}
	category, err := svc.GetByCategory(context.Background(), "agent", 1)
	if err != nil || len(category) != 1 || category[0].Term != "Aka" {
		t.Fatalf("GetByCategory = %#v, %v", category, err)
	}
}

func TestGlossaryServiceAddReturnsSaveError(t *testing.T) {
	saveErr := errors.New("save failed")
	svc := NewGlossaryService(&fakeGlossaryRepository{saveErr: saveErr})

	item, err := svc.AddGlossaryItem(context.Background(), "RenCrow", "assistant", "manual", "project")
	if !errors.Is(err, saveErr) {
		t.Fatalf("expected save error, got %v", err)
	}
	if item != nil {
		t.Fatalf("item should be nil on save error, got %#v", item)
	}
}
