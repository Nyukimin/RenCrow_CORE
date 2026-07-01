package main

import (
	"log"

	vocabApp "github.com/Nyukimin/picoclaw_multiLLM/application/vocabulary"
	vocabInfra "github.com/Nyukimin/picoclaw_multiLLM/infrastructure/vocabulary"
)

func main() {
	// Initialize repository
	repo := vocabInfra.NewMemoryRepository()
	
	// Configure RSS sources
	sources := []vocabApp.RSSSource{
		{
			URL:      "https://example.com/news/rss",
			Name:     "Example News",
			Category: "general",
		},
		// Add more sources as needed
	}
	
	// Create service
	service := vocabApp.NewAppService(repo, sources)
	
	// Update from sources
	count, err := service.UpdateFromSources()
	if err != nil {
		log.Fatalf("Failed to update vocabulary: %v", err)
	}
	
	log.Printf("Updated vocabulary store with %d new entries", count)
	
	// Get context for testing
	context, err := service.GetContext(10)
	if err != nil {
		log.Fatalf("Failed to get context: %v", err)
	}
	
	log.Println("Recent context:")
	log.Println(context)
}
