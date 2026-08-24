package viewer

import (
	"os"
	"strings"
	"testing"
)

func TestViewerUserMemoryProvidesSearchStateFilterAndPagination(t *testing.T) {
	htmlData, err := os.ReadFile("viewer.html")
	if err != nil {
		t.Fatalf("read viewer.html: %v", err)
	}
	jsData, err := os.ReadFile("assets/js/tabs/memory.js")
	if err != nil {
		t.Fatalf("read memory.js: %v", err)
	}
	html := string(htmlData)
	js := string(jsData)
	for _, want := range []string{
		`id="userMemoryStateFilter"`, `id="userMemoryQuery"`,
		`id="userMemoryPrevBtn"`, `id="userMemoryNextBtn"`,
		`id="userMemoryPageStatus"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("Viewer User Memory missing %q", want)
		}
	}
	for _, want := range []string{
		`params.set('offset', String(userMemoryOffset))`,
		`params.set('state', userMemoryStateFilter.value)`,
		`params.set('q', userMemoryQuery.value.trim())`,
		`function previousUserMemoryPage()`, `function nextUserMemoryPage()`,
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("Viewer User Memory paging contract missing %q", want)
		}
	}
}
