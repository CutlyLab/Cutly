// Package httpapiv2 exposes the Document v2 Agent protocol over HTTP.
package httpapiv2

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/CutlyLab/Cutly/agent/protocolv2"
)

type Handler interface {
	HandleAgentV2(context.Context, protocolv2.Request) (protocolv2.Result, error)
}

type HandlerFunc func(context.Context, protocolv2.Request) (protocolv2.Result, error)

func (fn HandlerFunc) HandleAgentV2(ctx context.Context, request protocolv2.Request) (protocolv2.Result, error) {
	return fn(ctx, request)
}

type Server struct{ Handler Handler }

func (server Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeProblem(writer, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if server.Handler == nil {
		writeProblem(writer, http.StatusServiceUnavailable, "handler_unavailable")
		return
	}
	mediaType := request.Header.Get("Content-Type")
	if mediaType != protocolv2.MediaType && mediaType != protocolv2.MediaType+`; profile="cutly.canonical-json.v1"` {
		writeProblem(writer, http.StatusUnsupportedMediaType, "unsupported_media_type")
		return
	}
	if request.ContentLength > protocolv2.MaxRequestBytes {
		writeProblem(writer, http.StatusRequestEntityTooLarge, "request_too_large")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, protocolv2.MaxRequestBytes+1))
	if err != nil || len(raw) > protocolv2.MaxRequestBytes {
		writeProblem(writer, http.StatusBadRequest, "request_read_failed")
		return
	}
	decoded, err := protocolv2.DecodeRequest(request.Context(), raw)
	if err != nil {
		writeProblem(writer, http.StatusBadRequest, "request_invalid")
		return
	}
	result, err := server.Handler.HandleAgentV2(request.Context(), decoded)
	if err != nil {
		writeProblem(writer, http.StatusInternalServerError, "handler_failed")
		return
	}
	if result.RequestID != decoded.RequestID || result.Tool != decoded.Tool || result.Action != decoded.Action || result.ProjectID != decoded.ProjectID {
		writeProblem(writer, http.StatusInternalServerError, "handler_result_mismatch")
		return
	}
	encoded, err := protocolv2.CanonicalResult(request.Context(), result)
	if err != nil {
		writeProblem(writer, http.StatusInternalServerError, "handler_result_invalid")
		return
	}
	writer.Header().Set("Content-Type", protocolv2.MediaType)
	writer.Header().Set("Content-Length", fmt.Sprintf("%d", len(encoded)))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(encoded)
}

func writeProblem(writer http.ResponseWriter, status int, code string) {
	encoded, _ := json.Marshal(struct {
		Error string `json:"error"`
	}{Error: code})
	writer.Header().Set("Content-Type", "application/problem+json")
	writer.Header().Set("Content-Length", fmt.Sprintf("%d", len(encoded)))
	writer.WriteHeader(status)
	_, _ = writer.Write(encoded)
}
