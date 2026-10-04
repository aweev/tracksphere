package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tracksphere/tracksphere/internal/model"
	"github.com/tracksphere/tracksphere/internal/shipments"
)

// Minimal MCP (Model Context Protocol) endpoint: JSON-RPC 2.0 over POST with
// tools/list + tools/call, authenticated by session or Bearer API key.
// Lets agents (and the "no per-label fee" pitch) drive TrackSphere:
// track_shipment, list_shipments, list_exceptions.

type mcpRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params"`
}

func mcpResult(id any, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

func mcpError(id any, code int, message string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": message}}
}

// handleMCP POST /api/v1/mcp
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	var req mcpRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	user := currentUser(r)
	switch req.Method {
	case "initialize":
		writeJSON(w, http.StatusOK, mcpResult(req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo":      map[string]any{"name": "tracksphere", "version": buildVersion},
			"capabilities":    map[string]any{"tools": map[string]any{}},
		}))
	case "tools/list":
		writeJSON(w, http.StatusOK, mcpResult(req.ID, map[string]any{"tools": []map[string]any{
			{"name": "track_shipment", "description": "Public tracking lookup by tracking number",
				"inputSchema": map[string]any{"type": "object",
					"properties": map[string]any{"trackingNumber": map[string]any{"type": "string"}},
					"required":   []string{"trackingNumber"}}},
			{"name": "list_shipments", "description": "List tenant shipments (status, limit)",
				"inputSchema": map[string]any{"type": "object",
					"properties": map[string]any{
						"status": map[string]any{"type": "string"},
						"limit":  map[string]any{"type": "integer"}}}},
			{"name": "list_exceptions", "description": "List open alerts with shipment context",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
		}}))
	case "tools/call":
		name, _ := req.Params["name"].(string)
		args, _ := req.Params["arguments"].(map[string]any)
		if args == nil {
			args = map[string]any{}
		}
		s.mcpCall(w, r, req.ID, user, name, args)
	default:
		writeJSON(w, http.StatusOK, mcpError(req.ID, -32601, "method not found"))
	}
}

func (s *Server) mcpCall(w http.ResponseWriter, r *http.Request, id any, user *model.User, name string, args map[string]any) {
	text := func(v any) string {
		raw, _ := json.Marshal(v)
		return string(raw)
	}
	switch name {
	case "track_shipment":
		tn, _ := args["trackingNumber"].(string)
		ship, err := s.shipments.ByTrackingNumber(r.Context(), strings.TrimSpace(tn))
		if err != nil {
			writeJSON(w, http.StatusOK, mcpError(id, -32004, "shipment not found"))
			return
		}
		writeJSON(w, http.StatusOK, mcpResult(id, map[string]any{
			"content": []map[string]any{{"type": "text", "text": text(ship)}}}))
	case "list_shipments":
		status, _ := args["status"].(string)
		limit := 10
		if n, ok := args["limit"].(float64); ok && n >= 1 && n <= 50 {
			limit = int(n)
		}
		if status != "" && !model.ValidStatus(status) {
			writeJSON(w, http.StatusOK, mcpError(id, -32602, "unknown status"))
			return
		}
		rows, _, err := s.shipments.List(r.Context(), user.TenantID,
			shipments.ListInput{Status: status, Limit: limit})
		if err != nil {
			writeJSON(w, http.StatusOK, mcpError(id, -32000, err.Error()))
			return
		}
		writeJSON(w, http.StatusOK, mcpResult(id, map[string]any{
			"content": []map[string]any{{"type": "text", "text": text(rows)}}}))
	case "list_exceptions":
		rows, err := s.listAlerts(r.Context(), user.TenantID, "open")
		if err != nil {
			writeJSON(w, http.StatusOK, mcpError(id, -32000, err.Error()))
			return
		}
		writeJSON(w, http.StatusOK, mcpResult(id, map[string]any{
			"content": []map[string]any{{"type": "text", "text": text(rows)}}}))
	default:
		writeJSON(w, http.StatusOK, mcpError(id, -32602, "unknown tool"))
	}
}
