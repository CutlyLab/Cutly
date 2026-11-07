package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	cutly "github.com/CutlyLab/Cutly/sdk/go/cutly"
)

func main() {
	endpoint := flag.String("endpoint", "http://127.0.0.1:9817/v1/agent", "Cutly Agent endpoint")
	project := flag.String("project", "demo", "project identity")
	requestID := flag.String("request-id", "example-inspect-1", "stable request identity")
	flag.Parse()

	transport, err := cutly.NewHTTPTransport(*endpoint, nil)
	if err != nil {
		fatal(err)
	}
	client, err := cutly.NewClient(transport)
	if err != nil {
		fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := client.Invoke(ctx, *requestID, cutly.ToolProject, "inspect", *project, struct{}{})
	if err != nil {
		fatal(err)
	}
	encoded, err := agentprotocol.CanonicalResult(result)
	if err != nil {
		fatal(err)
	}
	_, _ = os.Stdout.Write(append(encoded, '\n'))
	if !result.OK {
		os.Exit(3)
	}
}

func fatal(err error) {
	_, _ = fmt.Fprintf(os.Stderr, "cutly example: %v\n", err)
	os.Exit(1)
}
