package image

import "net/http"

type Routes struct {
	Generate      http.HandlerFunc
	Result        http.HandlerFunc
	VideoSession  http.HandlerFunc
	VideoGenerate http.HandlerFunc
	VideoStatus   http.HandlerFunc
	VideoResult   http.HandlerFunc
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
	if routes.VideoSession != nil {
		mux.HandleFunc("/viewer/image/video/session", routes.VideoSession)
	}
	if routes.VideoGenerate != nil {
		mux.HandleFunc("/viewer/image/video/generations", routes.VideoGenerate)
	}
	if routes.VideoStatus != nil {
		mux.HandleFunc("/viewer/image/video/generations/", routes.VideoStatus)
	}
	if routes.VideoResult != nil {
		mux.HandleFunc("/viewer/image/video/", routes.VideoResult)
	}
}
