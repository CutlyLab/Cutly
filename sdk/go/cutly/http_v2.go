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

	"github.com/CutlyLab/Cutly/agent/protocolv2"
)

// AgentHTTPTransportV2 sends canonical Document v2 Agent envelopes over one
// fixed POST endpoint and never falls back to the v1 media type.
type AgentHTTPTransportV2 struct {
	endpoint string
	client   *http.Client
}

func NewAgentHTTPTransportV2(endpoint string, client *http.Client) (*AgentHTTPTransportV2, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("cutly sdk: parse Agent v2 endpoint: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("cutly sdk: Agent v2 endpoint must be an absolute http or https URL without user info or fragment")
	}
	if client == nil {
		client = &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &AgentHTTPTransportV2{endpoint: parsed.String(), client: client}, nil
}

func (transport *AgentHTTPTransportV2) RoundTrip(ctx context.Context, canonicalRequest []byte) ([]byte, error) {
	if transport == nil || transport.client == nil || transport.endpoint == "" {
		return nil, errors.New("uninitialized Agent v2 HTTP transport")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, transport.endpoint, bytes.NewReader(canonicalRequest))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", protocolv2.MediaType)
	request.Header.Set("Accept", protocolv2.MediaType)
	response, err := transport.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, protocolv2.MaxResultBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > protocolv2.MaxResultBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", protocolv2.MaxResultBytes)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP status %d: %s", response.StatusCode, boundedStatusText(body, response.StatusCode))
	}
	if strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]) != protocolv2.MediaType {
		return nil, fmt.Errorf("unexpected Content-Type %q", response.Header.Get("Content-Type"))
	}
	return append([]byte(nil), body...), nil
}
