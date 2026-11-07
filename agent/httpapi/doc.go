// Package httpapi exposes the Cutly Agent envelope through a small, strict
// HTTP boundary. Business rejection remains a canonical HTTP 200 Agent result;
// malformed requests and handler failures are transport-level HTTP errors.
package httpapi
