package image

import "net/http"

type Routes struct {
	Generate http.HandlerFunc
	Result   http.HandlerFunc
}

func RegisterRoutes(mux *http.ServeMux, routes Routes) {
	if mux == nil {
		return
	}
	if routes.Generate != nil {
		mux.HandleFunc("/viewer/image/generate", routes.Generate)
	}
	if routes.Result != nil {
		mux.HandleFunc("/viewer/image/result", routes.Result)
	}
}
