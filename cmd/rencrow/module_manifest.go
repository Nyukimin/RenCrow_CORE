package main

import (
	"net/http"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func handleModuleManifest() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !modulecore.RequireHTTPMethod(w, r, http.MethodGet) {
			return
		}
		_ = modulecore.WriteJSON(w, modulecore.BuildManifestSnapshot(currentModuleDescriptors(), time.Now().UTC()))
	}
}

func currentModuleDescriptors() []modulecore.ModuleDescriptor {
	return modulecore.CurrentModuleDescriptors()
}
