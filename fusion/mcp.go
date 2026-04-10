// Package fusion provides a minimal MCP (Model Context Protocol) client for
// talking to the Fusion desktop client's MCP server. This is used to open or
// insert documents in the running Fusion application by lineage URN, which is
// more reliable than constructing fusion360:// deep-links.
//
// The Fusion MCP server is expected to be running locally at
// http://127.0.0.1:27182/mcp while Fusion is open.
package fusion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultEndpoint is the local Fusion MCP server URL.
const DefaultEndpoint = "http://127.0.0.1:27182/mcp"

// defaultTimeout bounds a single MCP operation (open or insert) end-to-end.
const defaultTimeout = 15 * time.Second

// jsonRPCRequest is a JSON-RPC 2.0 request envelope.
type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// jsonRPCResponse is a JSON-RPC 2.0 response envelope.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// toolCallResult is the payload returned by MCP tools/call responses.
type toolCallResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError,omitempty"`
}

// Client is a simple Fusion MCP client. Each public method establishes its
// own session; sessions are short-lived and not reused between calls.
type Client struct {
	Endpoint string
	HTTP     *http.Client
}

// NewClient returns a Client pointed at the default local Fusion endpoint.
func NewClient() *Client {
	return &Client{
		Endpoint: DefaultEndpoint,
		HTTP:     &http.Client{Timeout: defaultTimeout},
	}
}

// HubProject is a single project returned by ActiveHubProjects. The ID is the
// Data Management API project ID (the same format FusionDataCLI stores in
// NavItem.AltID for projects), not the GraphQL project ID.
type HubProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ActiveHubProjects returns the projects in Fusion's currently active hub.
// This does not require any document to be open — the MCP server routes the
// request through app.data directly. Callers can use the returned project
// IDs to verify that a separately-browsed hub matches Fusion's active hub.
func (c *Client) ActiveHubProjects(ctx context.Context) ([]HubProject, error) {
	sid, err := c.initSession(ctx)
	if err != nil {
		return nil, err
	}
	tr, err := c.callTool(ctx, sid, "fusion_mcp_read", map[string]any{
		"queryType": "projects",
	})
	if err != nil {
		return nil, err
	}
	if tr == nil || len(tr.Content) == 0 {
		return nil, errors.New("fusion MCP: empty projects response")
	}
	var payload struct {
		Success  *bool        `json:"success"`
		Projects []HubProject `json:"projects"`
	}
	if err := json.Unmarshal([]byte(tr.Content[0].Text), &payload); err != nil {
		return nil, fmt.Errorf("fusion MCP: decoding projects: %w", err)
	}
	if payload.Success != nil && !*payload.Success {
		return nil, errors.New("fusion MCP: projects query failed")
	}
	return payload.Projects, nil
}

// OpenDocument opens a Fusion document by its lineage URN (fileId).
// Returns an error if the Fusion MCP server is unreachable or the document
// cannot be opened.
func (c *Client) OpenDocument(ctx context.Context, fileId string) error {
	if fileId == "" {
		return errors.New("fusion: empty fileId")
	}
	sid, err := c.initSession(ctx)
	if err != nil {
		return err
	}
	_, err = c.callTool(ctx, sid, "fusion_mcp_execute", map[string]any{
		"featureType": "document",
		"object": map[string]any{
			"operation": "open",
			"fileId":    fileId,
		},
	})
	return err
}

// InsertDocument inserts the document identified by lineage URN (fileId) as
// an occurrence into the currently active Fusion design. Requires that a
// design document already be active in Fusion.
func (c *Client) InsertDocument(ctx context.Context, fileId string) error {
	if fileId == "" {
		return errors.New("fusion: empty fileId")
	}
	sid, err := c.initSession(ctx)
	if err != nil {
		return err
	}
	script := buildInsertScript(fileId)
	_, err = c.callTool(ctx, sid, "fusion_mcp_execute", map[string]any{
		"featureType": "script",
		"object": map[string]any{
			"script": script,
		},
	})
	return err
}

// buildInsertScript returns a Python script that inserts the given file as a
// new occurrence in the active design using the Fusion API.
func buildInsertScript(fileId string) string {
	// Escape single quotes for safe inlining into a Python string literal.
	safe := strings.ReplaceAll(fileId, `\`, `\\`)
	safe = strings.ReplaceAll(safe, `'`, `\'`)
	return `import adsk.core, adsk.fusion

def run(_context: str):
    app = adsk.core.Application.get()
    design = adsk.fusion.Design.cast(app.activeProduct)
    if not design:
        raise Exception("No active Fusion design to insert into")
    data_file = app.data.findFileById('` + safe + `')
    if not data_file:
        raise Exception("File not found: ` + safe + `")
    transform = adsk.core.Matrix3D.create()
    design.rootComponent.occurrences.addByInsert(data_file, transform, True)
    print("inserted")
`
}

// initSession performs the MCP initialize handshake and returns the session
// ID, if the server is running in stateful mode. The Fusion MCP server can
// also run in a stateless mode in which no session id is returned and none
// is required on subsequent calls — in that case an empty string is returned
// and the caller should not send a Mcp-Session-Id header.
func (c *Client) initSession(ctx context.Context) (string, error) {
	body, _ := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "FusionDataCLI",
				"version": "1.0",
			},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("fusion MCP unreachable (is Fusion running?): %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fusion MCP init failed (HTTP %d): %s", resp.StatusCode, string(raw))
	}
	sid := resp.Header.Get("Mcp-Session-Id")

	// Send the required "notifications/initialized" notification. In
	// stateless mode the header is simply omitted.
	noteBody, _ := json.Marshal(jsonRPCRequest{JSONRPC: "2.0", Method: "notifications/initialized"})
	noteReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(noteBody))
	if err != nil {
		return "", err
	}
	noteReq.Header.Set("Content-Type", "application/json")
	noteReq.Header.Set("Accept", "application/json, text/event-stream")
	if sid != "" {
		noteReq.Header.Set("Mcp-Session-Id", sid)
	}
	noteResp, err := c.HTTP.Do(noteReq)
	if err != nil {
		return "", err
	}
	noteResp.Body.Close()
	return sid, nil
}

// callTool invokes an MCP tool and returns the decoded result, or an error.
func (c *Client) callTool(ctx context.Context, sid, name string, args map[string]any) (*toolCallResult, error) {
	body, _ := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/call",
		Params: map[string]any{
			"name":      name,
			"arguments": args,
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fusion MCP call failed: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fusion MCP call HTTP %d: %s", resp.StatusCode, string(raw))
	}

	// Some MCP servers stream responses as SSE when the Accept header includes
	// text/event-stream. Detect and extract the JSON from the first "data:" line.
	payload := raw
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("event:")) || bytes.Contains(raw, []byte("\ndata:")) || bytes.HasPrefix(raw, []byte("data:")) {
		payload = extractSSEData(raw)
	}

	var rpc jsonRPCResponse
	if err := json.Unmarshal(payload, &rpc); err != nil {
		return nil, fmt.Errorf("fusion MCP: decoding response: %w (body: %s)", err, string(raw))
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("fusion MCP error: %s", rpc.Error.Message)
	}
	var tr toolCallResult
	if len(rpc.Result) > 0 {
		if err := json.Unmarshal(rpc.Result, &tr); err != nil {
			return nil, fmt.Errorf("fusion MCP: decoding tool result: %w", err)
		}
	}
	if tr.IsError {
		msg := "tool error"
		if len(tr.Content) > 0 {
			msg = tr.Content[0].Text
		}
		return &tr, errors.New(msg)
	}
	// Fusion MCP tools frequently report failures inside the content text
	// as JSON like {"success": false, "error": "..."} with HTTP 200 and no
	// isError flag. Detect and surface those as errors too.
	if len(tr.Content) > 0 {
		if msg := parseToolErrorText(tr.Content[0].Text); msg != "" {
			return &tr, errors.New(msg)
		}
	}
	return &tr, nil
}

// parseToolErrorText inspects the text payload returned by a Fusion MCP
// tool call. If it parses as JSON with success:false or an error field,
// returns the error message; otherwise returns "".
func parseToolErrorText(text string) string {
	t := strings.TrimSpace(text)
	if t == "" || (t[0] != '{' && t[0] != '[') {
		return ""
	}
	var payload struct {
		Success *bool  `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal([]byte(t), &payload); err != nil {
		return ""
	}
	if payload.Error != "" && (payload.Success == nil || !*payload.Success) {
		return payload.Error
	}
	if payload.Success != nil && !*payload.Success && payload.Error == "" {
		return "tool reported failure"
	}
	return ""
}

// extractSSEData pulls the concatenated JSON payload out of an SSE stream body.
func extractSSEData(raw []byte) []byte {
	var buf bytes.Buffer
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if bytes.HasPrefix(line, []byte("data:")) {
			buf.Write(bytes.TrimSpace(line[len("data:"):]))
		}
	}
	if buf.Len() == 0 {
		return raw
	}
	return buf.Bytes()
}
