package image

import "net/http"

type Routes struct {
	Options       http.HandlerFunc
	Generate      http.HandlerFunc
	EditOptions   http.HandlerFunc
	Edit          http.HandlerFunc
	Result        http.HandlerFunc
	VideoSession  http.HandlerFunc
	VideoOptions  http.HandlerFunc
	VideoGenerate http.HandlerFunc
	VideoStatus   http.HandlerFunc
	VideoResult   http.HandlerFunc
}

func RegisterRoutes(mux *http.ServeMux, routes Routes) {
	if mux == nil {
		return
	}
	if routes.Options != nil {
		mux.HandleFunc("/viewer/image/options", routes.Options)
	}
	if routes.Generate != nil {
		mux.HandleFunc("/viewer/image/generate", routes.Generate)
	}
	if routes.EditOptions != nil {
		mux.HandleFunc("/viewer/image/edit/options", routes.EditOptions)
	}
	if routes.Edit != nil {
		mux.HandleFunc("/viewer/image/edit", routes.Edit)
	}
	if routes.Result != nil {
		mux.HandleFunc("/viewer/image/result", routes.Result)
	}
	if routes.VideoSession != nil {
		mux.HandleFunc("/viewer/image/video/session", routes.VideoSession)
	}
	if routes.VideoOptions != nil {
		mux.HandleFunc("/viewer/image/video/options", routes.VideoOptions)
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
