package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

// Handler executes one already validated request. Returning an error means no
// business result exists and is mapped to an HTTP 500 transport failure.
type Handler interface {
	HandleAgent(context.Context, agentprotocol.Request) (agentprotocol.Result, error)
}

type HandlerFunc func(context.Context, agentprotocol.Request) (agentprotocol.Result, error)

func (fn HandlerFunc) HandleAgent(ctx context.Context, request agentprotocol.Request) (agentprotocol.Result, error) {
	return fn(ctx, request)
}

// Server is an http.Handler and contains no ambient global registry. Handler
// implementations can therefore be scoped to one tenant, workspace or daemon.
type Server struct {
	Handler Handler
}

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
	if !validContentType(request.Header.Get("Content-Type")) {
		writeProblem(writer, http.StatusUnsupportedMediaType, "unsupported_media_type")
		return
	}
	if request.ContentLength > agentprotocol.MaxRequestBytes {
		writeProblem(writer, http.StatusRequestEntityTooLarge, "request_too_large")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, agentprotocol.MaxRequestBytes+1))
	if err != nil {
		writeProblem(writer, http.StatusBadRequest, "request_read_failed")
		return
	}
	if len(raw) > agentprotocol.MaxRequestBytes {
		writeProblem(writer, http.StatusRequestEntityTooLarge, "request_too_large")
		return
	}
	decoded, err := agentprotocol.DecodeRequest(raw)
	if err != nil {
		writeProblem(writer, http.StatusBadRequest, "request_invalid")
		return
	}
	result, err := server.Handler.HandleAgent(request.Context(), decoded)
	if err != nil {
		writeProblem(writer, http.StatusInternalServerError, "handler_failed")
		return
	}
	if err := correlate(decoded, result); err != nil {
		writeProblem(writer, http.StatusInternalServerError, "handler_result_mismatch")
		return
	}
	encoded, err := agentprotocol.CanonicalResult(result)
	if err != nil {
		writeProblem(writer, http.StatusInternalServerError, "handler_result_invalid")
		return
	}
	writer.Header().Set("Content-Type", agentprotocol.MediaType+"; profile=\""+canonicaljson.Profile+"\"")
	writer.Header().Set("Content-Length", fmt.Sprintf("%d", len(encoded)))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(encoded)
}

func correlate(request agentprotocol.Request, result agentprotocol.Result) error {
	if result.RequestID != request.RequestID || result.Tool != request.Tool || result.Action != request.Action {
		return errors.New("request identity, tool or action mismatch")
	}
	if request.ProjectID != "" && result.ProjectID != request.ProjectID {
		return errors.New("project identity mismatch")
	}
	return nil
}

func validContentType(value string) bool {
	mediaType, parameters, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	if mediaType != agentprotocol.MediaType && mediaType != "application/json" {
		return false
	}
	for name, value := range parameters {
		if strings.ToLower(name) != "profile" || value != canonicaljson.Profile {
			return false
		}
	}
	return true
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
