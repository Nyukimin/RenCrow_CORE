package viewer

import "net/http"

func requireViewerMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func requireViewerStore(w http.ResponseWriter, missing bool, message string) bool {
	if !missing {
		return true
	}
	http.Error(w, message, http.StatusServiceUnavailable)
	return false
}
