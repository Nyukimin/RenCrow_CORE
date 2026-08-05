package viewer

import (
	"os"
	"strings"
	"testing"
)

func TestViewerIdleChatRendersSeparateWordAndForecastTopicStocks(t *testing.T) {
	viewerJS, err := os.ReadFile("assets/js/viewer.js")
	if err != nil {
		t.Fatal(err)
	}
	idleChatJS, err := os.ReadFile("assets/js/tabs/idlechat.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"wordTopicStock: null", "forecastStock: null"} {
		if !strings.Contains(string(viewerJS), needle) {
			t.Fatalf("viewer state missing %q", needle)
		}
	}
	js := string(idleChatJS)
	for _, needle := range []string{
		"renderIdleWordTopicStock", "renderIdleTopicStocks", "d.word_topic_stock", "category.label", "<h3>Forecast</h3>",
	} {
		if !strings.Contains(js, needle) {
			t.Fatalf("IdleChat topic stock renderer missing %q", needle)
		}
	}
	if strings.Contains(js, "Array.from({length: Math.max(categoryCapacity") {
		t.Fatal("word topic list must not render empty capacity placeholders that inflate vertical height")
	}
}
