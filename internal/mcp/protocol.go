package mcp

import (
	"context"
	"encoding/json"
	"strings"
)

const (
	LatestProtocolVersion = "2025-06-18"
	JSONRPCVersion        = "2.0"

	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInternalError  = -32603
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type errorObject struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *errorObject    `json:"error,omitempty"`
}

// Handle 处理一条 JSON-RPC 消息。第二个返回值表示是否需要回写响应：
// 通知（没有 id）不需要响应，给它回包会让客户端解析出错。
func (s *Server) Handle(ctx context.Context, raw []byte) ([]byte, bool) {
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		return s.replyError(nil, CodeInvalidRequest, "parse error: "+err.Error()), true
	}
	if strings.TrimSpace(req.Method) == "" {
		return s.replyError(req.ID, CodeInvalidRequest, "missing method"), true
	}

	isNotification := len(req.ID) == 0 || string(req.ID) == "null"

	switch req.Method {
	case "initialize":
		return s.reply(req.ID, s.initialize(req.Params)), true
	case "ping":
		return s.reply(req.ID, map[string]any{}), true
	case "tools/list":
		return s.reply(req.ID, map[string]any{"tools": s.toolDefinitions()}), true
	case "tools/call":
		res, err := s.callTool(ctx, req.Params)
		if err != nil {
			// 走到这里是 params 本身不是对象，属请求格式错误；
			// 工具参数错误与后端失败都由 callTool 折成 result.isError。
			return s.replyError(req.ID, CodeInvalidRequest, err.Error()), true
		}
		return s.reply(req.ID, res), true
	}

	if strings.HasPrefix(req.Method, "notifications/") {
		return nil, false
	}
	if isNotification {
		return nil, false
	}
	return s.replyError(req.ID, CodeMethodNotFound, "method not found: "+req.Method), true
}

func (s *Server) initialize(params json.RawMessage) map[string]any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	version := LatestProtocolVersion
	if err := json.Unmarshal(params, &p); err == nil && p.ProtocolVersion != "" {
		version = p.ProtocolVersion
	}
	// 回显客户端版本：MCP 允许服务器在双方都懂的范围内选用；
	// 硬编码一个版本会让旧客户端直接协商失败。
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "agent-memory", "version": ServerVersion},
	}
}

func (s *Server) reply(id json.RawMessage, result any) []byte {
	body, _ := json.Marshal(response{JSONRPC: JSONRPCVersion, ID: id, Result: result})
	return body
}

func (s *Server) replyError(id json.RawMessage, code int, message string) []byte {
	body, _ := json.Marshal(response{JSONRPC: JSONRPCVersion, ID: id, Error: &errorObject{Code: code, Message: message}})
	return body
}

const ServerVersion = "0.1.0-m3"
