package viewer

import "net/http"

// HandleTaskCenterPage serves the authenticated task management workbench.
func HandleTaskCenterPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	serveEmbeddedPage(w, "assets/task-center.html", "task center page not found")
}
