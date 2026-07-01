package main

import (
	"context"
	"fmt"
	"log"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/glossary/application/service"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/glossary/infrastructure/feed"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/glossary/infrastructure/persistence"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/glossary/interface/controller"
)

func main() {
	ctx := context.Background()
	
	// Initialize repository
	repo, err := persistence.NewSQLiteGlossaryRepository("./glossary.db")
	if err != nil {
		log.Fatal("Failed to initialize repository:", err)
	}
	
	// Initialize service
	glossaryService := service.NewGlossaryService(repo)
	
	// Initialize controller
	controller := controller.NewGlossaryController(glossaryService)
	
	// Example: Add some test data
	testItem, err := controller.AddItem(ctx, "AI", "Artificial Intelligence technology", "test", "technology")
	if err != nil {
		log.Println("Warning: Failed to add test item:", err)
	} else {
		fmt.Printf("Added test item: %s\n", testItem.Term)
	}
	
	// Example: Fetch RSS feeds
	feedURLs := []string{
		"https://rss.nytimes.com/services/xml/rss/nyt/Technology.xml",
		"https://feeds.bbci.co.uk/news/technology/rss.xml",
	}
	
	rssParser := feed.NewRSSParser(feedURLs)
	items, err := rssParser.FetchAndParse(ctx)
	if err != nil {
		log.Println("Warning: RSS fetch failed:", err)
	} else {
		fmt.Printf("Fetched %d items from RSS\n", len(items))
		
		// Save fetched items
		for _, item := range items {
			_, err := controller.AddItem(ctx, item.Term, item.Explanation, item.Source, item.Category)
			if err != nil {
				log.Println("Failed to save item:", err)
			}
		}
	}
	
	// Example: Get recent glossary
	recent, err := controller.GetRecent(ctx, 5)
	if err != nil {
		log.Println("Failed to get recent glossary:", err)
	} else {
		fmt.Println("\nRecent glossary items:")
		for _, item := range recent {
			fmt.Printf("- %s: %s\n", item.Term, item.Explanation)
		}
	}
	
	fmt.Println("\nGlossary component initialized successfully!")
}
