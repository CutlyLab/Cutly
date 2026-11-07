package cutly

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

const AgentMediaType = agentprotocol.MediaType

// HTTPTransport sends canonical Agent envelopes over one fixed POST endpoint.
// Endpoint redirects are rejected by the default client to avoid moving a
// project-bearing request to a different origin.
type HTTPTransport struct {
	endpoint string
	client   *http.Client
}

func NewHTTPTransport(endpoint string, client *http.Client) (*HTTPTransport, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("cutly sdk: parse endpoint: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("cutly sdk: endpoint must be an absolute http or https URL")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return nil, fmt.Errorf("cutly sdk: endpoint must not contain user info or a fragment")
	}
	if client == nil {
		client = &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}}
	}
	return &HTTPTransport{endpoint: parsed.String(), client: client}, nil
}

func (transport *HTTPTransport) RoundTrip(ctx context.Context, canonicalRequest []byte) ([]byte, error) {
	if transport == nil || transport.client == nil || transport.endpoint == "" {
		return nil, errors.New("uninitialized HTTP transport")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, transport.endpoint, bytes.NewReader(canonicalRequest))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", AgentMediaType+"; profile=\""+canonicaljson.Profile+"\"")
	request.Header.Set("Accept", AgentMediaType+", application/json")
	response, err := transport.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	limited := io.LimitReader(response.Body, agentprotocol.MaxResultBytes+1)
	body, readErr := io.ReadAll(limited)
	if readErr != nil {
		return nil, readErr
	}
	if len(body) > agentprotocol.MaxResultBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", agentprotocol.MaxResultBytes)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP status %d: %s", response.StatusCode, boundedStatusText(body, response.StatusCode))
	}
	if !agentprotocol.ValidContentType(response.Header.Get("Content-Type")) {
		return nil, fmt.Errorf("unexpected Content-Type %q", response.Header.Get("Content-Type"))
	}
	return append([]byte(nil), body...), nil
}

func boundedStatusText(raw []byte, status int) string {
	const maximum = 1024
	if len(raw) > maximum {
		raw = raw[:maximum]
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return http.StatusText(status)
	}
	return text
}
