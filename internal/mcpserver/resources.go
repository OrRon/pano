package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/flow"
)

func (s *Server) registerResources() {
	s.mcp.AddResource(&mcp.Resource{
		URI: "pano://ca.pem", Name: "pano CA certificate", MIMEType: "application/x-pem-file",
		Description: "The local root certificate pano signs with. Hand it to tools via SSL_CERT_FILE / NODE_EXTRA_CA_CERTS.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		pem, err := s.c.CAPEM(ctx)
		if err != nil {
			return nil, errors.New(describe(err))
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "application/x-pem-file", Text: string(pem)}}}, nil
	})

	s.mcp.AddResource(&mcp.Resource{
		URI: "pano://status", Name: "pano status", MIMEType: "text/plain",
		Description: "Daemon, capture, CA and system-proxy state.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		st, err := s.c.Status(ctx)
		if err != nil {
			return nil, errors.New(describe(err))
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "text/plain", Text: FormatStatus(st)}}}, nil
	})

	s.mcp.AddResource(&mcp.Resource{
		URI: "pano://flows/latest", Name: "latest flows", MIMEType: "text/plain",
		Description: "The 50 most recent flows, one line each.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		list, err := s.c.Flows(ctx, api.FlowFilter{Limit: 50})
		if err != nil {
			return nil, errors.New(describe(err))
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "text/plain", Text: FormatRows(list.Flows)}}}, nil
	})

	s.mcp.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "pano://flows/{id}", Name: "flow summary", MIMEType: "text/markdown",
		Description: "One flow rendered with view=summary (headers + body digest).",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		rest := strings.TrimPrefix(req.Params.URI, "pano://flows/")
		idStr, part, _ := strings.Cut(rest, "/")
		id, err := parseID(idStr)
		if err != nil {
			return nil, errors.New(describe(err))
		}
		if part != "" {
			return s.readRawBody(ctx, req.Params.URI, id, part)
		}
		d, err := s.c.Flow(ctx, id, api.FlowQuery{View: api.ViewSummary})
		if err != nil {
			return nil, errors.New(describe(err))
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "text/markdown", Text: d.Text}}}, nil
	})

	s.mcp.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "pano://flows/{id}/raw.{part}", Name: "raw body", MIMEType: "application/octet-stream",
		Description: "The decoded request or response body (part = request|response), up to 1 MiB.",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		rest := strings.TrimPrefix(req.Params.URI, "pano://flows/")
		idStr, tail, _ := strings.Cut(rest, "/")
		id, err := parseID(idStr)
		if err != nil {
			return nil, errors.New(describe(err))
		}
		part := strings.TrimPrefix(tail, "raw.")
		return s.readRawBody(ctx, req.Params.URI, id, part)
	})
}

func (s *Server) readRawBody(ctx context.Context, uri string, id flow.ID, part string) (*mcp.ReadResourceResult, error) {
	part = strings.TrimPrefix(part, "raw.")
	if part != "request" && part != "response" {
		return nil, fmt.Errorf("part must be request or response")
	}
	b, err := s.c.Body(ctx, id, part, true)
	if err != nil {
		return nil, errors.New(describe(err))
	}
	if len(b) > 1<<20 {
		b = b[:1<<20]
	}
	rc := &mcp.ResourceContents{URI: uri}
	if isText(b) {
		rc.MIMEType, rc.Text = "text/plain", string(b)
	} else {
		rc.MIMEType, rc.Blob = "application/octet-stream", b
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{rc}}, nil
}

func isText(b []byte) bool {
	for i, c := range b {
		if i > 4096 {
			break
		}
		if c == 0 {
			return false
		}
	}
	return true
}
