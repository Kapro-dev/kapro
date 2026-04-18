package mcp

import (
	"encoding/json"
	"net/http"
)

// handler dispatches JSON-RPC 2.0 requests for the MCP server.
type handler struct {
	server *Server
}

// ServeHTTP handles POST /mcp — all MCP requests arrive here.
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, nil, ErrParseError, "parse error: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")

	switch req.Method {
	case "initialize":
		h.handleInitialize(w, &req)
	case "tools/list":
		h.handleToolsList(w, &req)
	case "tools/call":
		h.handleToolsCall(w, r, &req)
	case "resources/list":
		h.handleResourcesList(w, &req)
	case "resources/read":
		h.handleResourcesRead(w, r, &req)
	default:
		writeError(w, req.ID, ErrMethodNotFound, "method not found: "+req.Method)
	}
}

func (h *handler) handleInitialize(w http.ResponseWriter, req *Request) {
	result := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]interface{}{
			"tools":     map[string]interface{}{},
			"resources": map[string]interface{}{},
		},
		"serverInfo": map[string]interface{}{
			"name":    "kapro",
			"version": "v1alpha1",
		},
	}
	writeResult(w, req.ID, result)
}

func (h *handler) handleToolsList(w http.ResponseWriter, req *Request) {
	writeResult(w, req.ID, map[string]interface{}{
		"tools": allTools(),
	})
}

func (h *handler) handleToolsCall(w http.ResponseWriter, r *http.Request, req *Request) {
	var params struct {
		Name      string                 `json:"name"`
		Arguments map[string]interface{} `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeError(w, req.ID, ErrInvalidParams, "invalid params: "+err.Error())
		return
	}

	t := &tools{client: h.server.client}
	result, err := t.call(r.Context(), params.Name, params.Arguments)
	if err != nil {
		writeError(w, req.ID, ErrInternal, err.Error())
		return
	}
	writeResult(w, req.ID, map[string]interface{}{
		"content": []map[string]interface{}{
			{"type": "text", "text": result},
		},
	})
}

func (h *handler) handleResourcesList(w http.ResponseWriter, req *Request) {
	writeResult(w, req.ID, map[string]interface{}{
		"resources": allResources(),
	})
}

func (h *handler) handleResourcesRead(w http.ResponseWriter, r *http.Request, req *Request) {
	var params struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeError(w, req.ID, ErrInvalidParams, "invalid params: "+err.Error())
		return
	}

	res := &resources{client: h.server.client}
	content, err := res.read(r.Context(), params.URI)
	if err != nil {
		writeError(w, req.ID, ErrInternal, err.Error())
		return
	}
	writeResult(w, req.ID, map[string]interface{}{
		"contents": []ResourceContent{*content},
	})
}

// writeResult serialises a successful JSON-RPC 2.0 response.
func writeResult(w http.ResponseWriter, id interface{}, result interface{}) {
	resp := Response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// writeError serialises a JSON-RPC 2.0 error response.
func writeError(w http.ResponseWriter, id interface{}, code int, msg string) {
	resp := Response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &RPCError{Code: code, Message: msg},
	}
	_ = json.NewEncoder(w).Encode(resp)
}
