package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	domainworkflow "github.com/Nyukimin/RenCrow_CORE/internal/domain/xbookmarkworkflow"
)

const maxXBookmarkWorkflowRequestBytes = 64 << 10

type XBookmarkWorkflowService interface {
	Run(context.Context, domainworkflow.RunRequest) (domainworkflow.Result, error)
	List(context.Context, domainworkflow.ResultQuery) ([]domainworkflow.Result, error)
}

func HandleXBookmarkWorkflow(service XBookmarkWorkflowService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if service == nil {
			http.Error(w, "x bookmark workflow unavailable", http.StatusServiceUnavailable)
			return
		}
		switch r.Method {
		case http.MethodGet:
			limit := 50
			if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
				parsed, err := strconv.Atoi(raw)
				if err != nil || parsed < 1 || parsed > 200 {
					http.Error(w, "invalid x bookmark workflow limit", http.StatusBadRequest)
					return
				}
				limit = parsed
			}
			values, err := service.List(r.Context(), domainworkflow.ResultQuery{
				SourceRecordID: strings.TrimSpace(r.URL.Query().Get("source_record_id")),
				Workflow:       strings.TrimSpace(r.URL.Query().Get("workflow")),
				Limit:          limit,
			})
			if err != nil {
				http.Error(w, "failed to list x bookmark workflow results", http.StatusInternalServerError)
				return
			}
			writeMonitorJSON(w, map[string]interface{}{"results": values})
		case http.MethodPost:
			var request domainworkflow.RunRequest
			decoder := json.NewDecoder(io.LimitReader(r.Body, maxXBookmarkWorkflowRequestBytes))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&request); err != nil {
				http.Error(w, "invalid x bookmark workflow request", http.StatusBadRequest)
				return
			}
			result, err := service.Run(r.Context(), request)
			if err != nil {
				status := http.StatusBadGateway
				if errors.Is(err, domainworkflow.ErrSourceNotFound) {
					status = http.StatusNotFound
				}
				http.Error(w, "x bookmark workflow failed", status)
				return
			}
			writeMonitorJSON(w, map[string]interface{}{"result": result})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}
