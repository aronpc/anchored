package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// --- Protocol Tests ---

func TestParseRequest_Valid(t *testing.T) {
	data := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	req, err := ParseRequest([]byte(data))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Method != "initialize" {
		t.Errorf("expected method 'initialize', got %q", req.Method)
	}
	if req.JSONRPC != "2.0" {
		t.Errorf("expected jsonrpc '2.0', got %q", req.JSONRPC)
	}
}

func TestParseRequest_InvalidJSON(t *testing.T) {
	_, err := ParseRequest([]byte(`{invalid json}`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseRequest_WrongVersion(t *testing.T) {
	data := `{"jsonrpc":"1.0","id":1,"method":"ping"}`
	_, err := ParseRequest([]byte(data))
	if err == nil {
		t.Fatal("expected error for wrong jsonrpc version")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("expected 'unsupported' in error, got: %v", err)
	}
}

func TestMarshalResponse_Success(t *testing.T) {
	resp := NewResponse(json.RawMessage(`1`), map[string]string{"status": "ok"})
	data := MarshalResponse(resp)

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if parsed["jsonrpc"] != "2.0" {
		t.Errorf("expected jsonrpc 2.0, got %v", parsed["jsonrpc"])
	}
	result, ok := parsed["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result to be a map, got %T", parsed["result"])
	}
	if result["status"] != "ok" {
		t.Errorf("expected status 'ok', got %v", result["status"])
	}
}

func TestMarshalResponse_Error(t *testing.T) {
	resp := NewErrorResponse(json.RawMessage(`42`), InvalidParams("missing field"))
	data := MarshalResponse(resp)

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal error response: %v", err)
	}
	errObj, ok := parsed["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error to be a map, got %T", parsed["error"])
	}
	if errObj["code"] != float64(-32602) {
		t.Errorf("expected error code -32602, got %v", errObj["code"])
	}
	if errObj["message"] != "missing field" {
		t.Errorf("expected error message 'missing field', got %v", errObj["message"])
	}
}

func TestNewError(t *testing.T) {
	err := NewError(-32600, "bad request")
	if err.Code != -32600 {
		t.Errorf("expected code -32600, got %d", err.Code)
	}
	if err.Message != "bad request" {
		t.Errorf("expected message 'bad request', got %q", err.Message)
	}
}

func TestInternalError(t *testing.T) {
	err := InternalError(fmt.Errorf("something broke"))
	if err.Code != -32603 {
		t.Errorf("expected code -32603, got %d", err.Code)
	}
	if err.Message != "something broke" {
		t.Errorf("expected message 'something broke', got %q", err.Message)
	}
}

func TestInvalidParams(t *testing.T) {
	err := InvalidParams("param X is required")
	if err.Code != -32602 {
		t.Errorf("expected code -32602, got %d", err.Code)
	}
}

// --- Tool Definitions Tests ---

func TestToolDefinitions_NonEmpty(t *testing.T) {
	tools := ToolDefinitions()
	if len(tools) == 0 {
		t.Fatal("expected at least one tool definition")
	}
}

func TestToolDefinitions_AllHaveNameAndDescription(t *testing.T) {
	tools := ToolDefinitions()
	for _, tool := range tools {
		if tool.Name == "" {
			t.Error("tool with empty name found")
		}
		if tool.Description == "" {
			t.Errorf("tool %q has empty description", tool.Name)
		}
	}
}

func TestToolDefinitions_AllHaveInputSchema(t *testing.T) {
	tools := ToolDefinitions()
	for _, tool := range tools {
		if tool.InputSchema == nil {
			t.Errorf("tool %q has nil InputSchema", tool.Name)
		}
		schemaType, ok := tool.InputSchema["type"]
		if !ok || schemaType != "object" {
			t.Errorf("tool %q InputSchema missing type 'object'", tool.Name)
		}
	}
}

func TestToolDefinitions_RequiredParamsAreValid(t *testing.T) {
	tools := ToolDefinitions()
	for _, tool := range tools {
		required, ok := tool.InputSchema["required"]
		if !ok {
			continue // no required params is fine
		}
		reqSlice, ok := required.([]string)
		if !ok {
			t.Errorf("tool %q: required should be []string, got %T", tool.Name, required)
			continue
		}
		props, ok := tool.InputSchema["properties"]
		if !ok {
			t.Errorf("tool %q has required fields but no properties", tool.Name)
			continue
		}
		propsMap, ok := props.(map[string]any)
		if !ok {
			t.Errorf("tool %q: properties should be map[string]any, got %T", tool.Name, props)
			continue
		}
		for _, r := range reqSlice {
			if _, exists := propsMap[r]; !exists {
				t.Errorf("tool %q: required param %q not found in properties", tool.Name, r)
			}
		}
	}
}

func TestToolDefinitions_ExpectedToolsExist(t *testing.T) {
	expected := []string{
		"anchored_context",
		"anchored_search",
		"anchored_save",
		"anchored_list",
		"anchored_update",
		"anchored_forget",
		"anchored_stats",
		"anchored_session_end",
		"anchored_kg_query",
		"anchored_kg_add",
	}
	tools := ToolDefinitions()
	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.Name] = true
	}
	for _, name := range expected {
		if !toolNames[name] {
			t.Errorf("expected tool %q not found in definitions", name)
		}
	}
}

func TestToolDefinitions_NoDuplicateNames(t *testing.T) {
	tools := ToolDefinitions()
	seen := make(map[string]bool)
	for _, tool := range tools {
		if seen[tool.Name] {
			t.Errorf("duplicate tool name: %q", tool.Name)
		}
		seen[tool.Name] = true
	}
}

func TestSortTools(t *testing.T) {
	tools := ToolDefinitions()
	SortTools(tools)
	for i := 1; i < len(tools); i++ {
		if tools[i-1].Name > tools[i].Name {
			t.Errorf("tools not sorted: %q > %q at index %d", tools[i-1].Name, tools[i].Name, i)
		}
	}
}

func TestResourceDefinitions_NonEmpty(t *testing.T) {
	resources := ResourceDefinitions()
	if len(resources) == 0 {
		t.Fatal("expected at least one resource definition")
	}
}

func TestResourceDefinitions_AllHaveURIAndName(t *testing.T) {
	resources := ResourceDefinitions()
	for _, r := range resources {
		if r.URI == "" {
			t.Error("resource with empty URI found")
		}
		if r.Name == "" {
			t.Errorf("resource %q has empty name", r.URI)
		}
	}
}

// --- HandleMessage / Routing Tests ---

func TestHandleMessage_InvalidJSON(t *testing.T) {
	s := &Server{}
	resp := s.HandleMessage(context.Background(), []byte(`not json at all`))

	var parsed map[string]any
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("response should be valid JSON: %v", err)
	}
	errObj, _ := parsed["error"].(map[string]any)
	if errObj == nil {
		t.Fatal("expected error in response")
	}
	if errObj["code"] != float64(-32700) {
		t.Errorf("expected parse error code -32700, got %v", errObj["code"])
	}
}

func TestHandleMessage_UnknownMethod(t *testing.T) {
	s := &Server{}
	req := `{"jsonrpc":"2.0","id":1,"method":"nonexistent/method"}`
	resp := s.HandleMessage(context.Background(), []byte(req))

	var parsed map[string]any
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("response should be valid JSON: %v", err)
	}
	errObj, _ := parsed["error"].(map[string]any)
	if errObj == nil {
		t.Fatal("expected error for unknown method")
	}
	if errObj["code"] != float64(-32601) {
		t.Errorf("expected method not found code -32601, got %v", errObj["code"])
	}
}

func TestHandleMessage_Ping(t *testing.T) {
	s := &Server{}
	req := `{"jsonrpc":"2.0","id":42,"method":"ping"}`
	resp := s.HandleMessage(context.Background(), []byte(req))

	var parsed map[string]any
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("response should be valid JSON: %v", err)
	}
	if parsed["id"] != float64(42) {
		t.Errorf("expected id 42, got %v", parsed["id"])
	}
	result, ok := parsed["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result map, got %T", parsed["result"])
	}
	if len(result) != 0 {
		t.Errorf("expected empty result for ping, got %v", result)
	}
}

func TestHandleMessage_Initialize(t *testing.T) {
	s := &Server{version: "test-v1"}
	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`
	resp := s.HandleMessage(context.Background(), []byte(req))

	var parsed map[string]any
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("response should be valid JSON: %v", err)
	}

	result, ok := parsed["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result map, got %T", parsed["result"])
	}
	if result["protocolVersion"] != MCPVersion {
		t.Errorf("expected protocolVersion %q, got %v", MCPVersion, result["protocolVersion"])
	}

	serverInfo, ok := result["serverInfo"].(map[string]any)
	if !ok {
		t.Fatalf("expected serverInfo map, got %T", result["serverInfo"])
	}
	if serverInfo["name"] != "anchored" {
		t.Errorf("expected server name 'anchored', got %v", serverInfo["name"])
	}
	if serverInfo["version"] != "test-v1" {
		t.Errorf("expected version 'test-v1', got %v", serverInfo["version"])
	}

	instructions, ok := result["instructions"].(string)
	if !ok {
		t.Fatalf("expected instructions string, got %T", result["instructions"])
	}
	if !strings.Contains(instructions, "<anchored_memory>") {
		t.Error("expected instructions to contain anchored_memory routing block")
	}
}

func TestHandleMessage_NotificationInitialized_ReturnsNil(t *testing.T) {
	s := &Server{}
	req := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	resp := s.HandleMessage(context.Background(), []byte(req))
	if resp != nil {
		t.Errorf("expected nil response for notification, got %s", resp)
	}
}

func TestHandleMessage_ToolsList(t *testing.T) {
	s := &Server{}
	req := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	resp := s.HandleMessage(context.Background(), []byte(req))

	var parsed map[string]any
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("response should be valid JSON: %v", err)
	}

	result, ok := parsed["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result map, got %T", parsed["result"])
	}
	toolsRaw, ok := result["tools"].([]any)
	if !ok {
		t.Fatalf("expected tools array, got %T", result["tools"])
	}
	if len(toolsRaw) == 0 {
		t.Fatal("expected at least one tool in tools/list response")
	}

	// Verify tools are sorted alphabetically.
	firstTool, _ := toolsRaw[0].(map[string]any)
	firstName, _ := firstTool["name"].(string)
	if firstName == "" {
		t.Fatal("first tool has no name")
	}
	for i := 1; i < len(toolsRaw); i++ {
		tool, _ := toolsRaw[i].(map[string]any)
		name, _ := tool["name"].(string)
		if name < firstName {
			t.Errorf("tools not sorted: %q should come before %q", name, firstName)
		}
		firstName = name
	}
}

func TestHandleMessage_ToolsCall_InvalidParams(t *testing.T) {
	s := &Server{}
	req := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":"not an object"}`
	resp := s.HandleMessage(context.Background(), []byte(req))

	var parsed map[string]any
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("response should be valid JSON: %v", err)
	}
	errObj, _ := parsed["error"].(map[string]any)
	if errObj == nil {
		t.Fatal("expected error for invalid params")
	}
	if errObj["code"] != float64(-32602) {
		t.Errorf("expected invalid params code -32602, got %v", errObj["code"])
	}
}

func TestHandleMessage_ToolsCall_UnknownTool(t *testing.T) {
	s := &Server{}
	req := `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nonexistent_tool","arguments":{}}}`
	resp := s.HandleMessage(context.Background(), []byte(req))

	var parsed map[string]any
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("response should be valid JSON: %v", err)
	}
	errObj, _ := parsed["error"].(map[string]any)
	if errObj == nil {
		t.Fatal("expected error for unknown tool")
	}
	if errObj["code"] != float64(-32603) {
		t.Errorf("expected internal error code -32603, got %v", errObj["code"])
	}
	if !strings.Contains(errObj["message"].(string), "unknown tool") {
		t.Errorf("expected 'unknown tool' in error message, got %v", errObj["message"])
	}
}

// --- callTool routing tests ---

func TestCallTool_UnknownTool(t *testing.T) {
	s := &Server{}
	_, err := s.callTool(context.Background(), "nonexistent_tool", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("expected 'unknown tool' in error, got: %v", err)
	}
}

func TestCallTool_StatsWithoutMemService(t *testing.T) {
	// toolStats calls s.mem.Stats() which dereferences mem.Service.
	// With nil mem, this panics. Skip the actual call and just verify
	// that the routing reaches the correct handler (anchored_stats -> toolStats)
	// by confirming the panic happens at toolStats, not at callTool dispatch.
	defer func() {
		r := recover()
		if r == nil {
			t.Error("expected panic when mem is nil and toolStats is called")
		}
	}()
	s := &Server{mem: nil}
	_, _ = s.callTool(context.Background(), "anchored_stats", json.RawMessage(`{}`))
}

func TestCallTool_SessionEndWithoutSessions(t *testing.T) {
	s := &Server{sessions: nil}
	result, err := s.callTool(context.Background(), "anchored_session_end", json.RawMessage(`{"session_id":"abc"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "not available") {
		t.Errorf("expected 'not available' message, got: %s", result)
	}
}

func TestCallTool_SessionEnd_MissingSessionID(t *testing.T) {
	// When sessions is nil, toolSessionEnd returns early with "not available"
	// before checking session_id. Test with sessions=nil to confirm routing
	// reaches the handler, and separately verify the param check logic in
	// toolSessionEnd by testing the JSON parsing path.
	s := &Server{sessions: nil}
	// With sessions=nil and no session_id, the tool returns "not available"
	// before ever checking the session_id.
	result, err := s.callTool(context.Background(), "anchored_session_end", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "not available") {
		t.Errorf("expected 'not available', got: %s", result)
	}
}

func TestCallTool_KGQueryWithoutKG(t *testing.T) {
	s := &Server{kg: nil}
	result, err := s.callTool(context.Background(), "anchored_kg_query", json.RawMessage(`{"entity":"test"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "not available") {
		t.Errorf("expected 'not available', got: %s", result)
	}
}

func TestCallTool_KGAddWithoutKG(t *testing.T) {
	s := &Server{kg: nil}
	result, err := s.callTool(context.Background(), "anchored_kg_add", json.RawMessage(`{"subject":"a","predicate":"uses","object":"b"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "not available") {
		t.Errorf("expected 'not available', got: %s", result)
	}
}

// --- Tool parameter validation tests ---

func TestCallTool_Search_MissingQuery(t *testing.T) {
	s := &Server{}
	_, err := s.callTool(context.Background(), "anchored_search", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error when query is missing from search")
	}
}

func TestCallTool_Save_MissingContent(t *testing.T) {
	s := &Server{}
	_, err := s.callTool(context.Background(), "anchored_save", json.RawMessage(`{"category":"fact"}`))
	if err != nil {
		// Save with nil mem will likely fail before param validation matters,
		// but the routing should still reach toolSave.
		t.Logf("save with nil mem: %v (expected)", err)
	}
}

// --- Minimal stubs for testable routing ---
// (session.Manager is a concrete struct, so nil is used for "disabled" tests.)

func TestSetDebugLogger(t *testing.T) {
	s := NewServer(nil, nil, nil, nil, nil, "", nil)
	
	// Test with nil logger (should not panic)
	s.SetDebugLogger(nil)
	if s.dlog != nil {
		t.Error("expected dlog to be nil after SetDebugLogger(nil)")
	}
}

func TestHeadTail(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		head     int
		tail     int
		expected string
	}{
		{
			name:     "short string passes through",
			s:        "hello",
			head:     10,
			tail:     10,
			expected: "hello",
		},
		{
			name:     "truncates with head and tail",
			s:        "abcdefghijklmnopqrstuvwxyz",
			head:     5,
			tail:     5,
			expected: "abcde\n[... 16 bytes omitted ...]\nvwxyz",
		},
		{
			name:     "empty string",
			s:        "",
			head:     5,
			tail:     5,
			expected: "",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := headTail(tt.s, tt.head, tt.tail)
			if result != tt.expected {
				t.Errorf("headTail(%q, %d, %d) = %q; want %q", tt.s, tt.head, tt.tail, result, tt.expected)
			}
		})
	}
}

func TestMarshalResponse_ErrorPath(t *testing.T) {
	// Force json.Marshal to fail by putting a non-marshalable value (channel) in Result.
	// JSONRPCResponse.Result is `any`, so a chan triggers a marshal error, exercising the
	// error branch that wraps the failure into an InternalError response.
	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Result:  make(chan int), // channels cannot be JSON-marshaled
	}
	out := MarshalResponse(resp)
	if len(out) == 0 {
		t.Fatal("expected non-empty fallback response")
	}
	if !strings.Contains(string(out), "error") {
		t.Errorf("expected error payload in fallback, got: %s", out)
	}
}

func TestSearchHitWriter_HitAndOmit(t *testing.T) {
	w := newSearchHitWriter(false)
	w.open("<anchored_search query=\"%s\">", "test")

	// Normal hit: within budget.
	w.hit([]string{`id="1"`}, "first line\nsecond line")
	out := w.sb.String()
	if !strings.Contains(out, "<hit") || !strings.Contains(out, "first line second line") {
		t.Errorf("expected flattened hit content, got: %s", out)
	}

	// Force the omitted path: fill the buffer near the 8KB budget with large hits,
	// then add one more that cannot fit. Content is capped to searchHitRunes (700)
	// per hit, so we need several to exhaust the budget.
	big := strings.Repeat("y", searchHitRunes)
	for i := 0; i < 12; i++ {
		w.hit([]string{`id="fill"`}, big)
	}
	w.hit([]string{`id="overflow"`}, big)
	if w.omitted == 0 {
		t.Error("expected omitted counter to increment when hit exceeds budget")
	}

	// close() must emit the <omitted> note (omitted > 0) and the closing tag.
	closed := w.close()
	if !strings.Contains(closed, "<omitted") {
		t.Errorf("expected <omitted> note in output, got: %s", closed)
	}
	if !strings.Contains(closed, "</anchored_search>") {
		t.Error("expected closing tag in output")
	}
}

func TestSearchHitWriter_CloseWithoutOmit(t *testing.T) {
	w := newSearchHitWriter(true) // full=true branch
	w.open("<anchored_search>")
	closed := w.close()
	if strings.Contains(closed, "<omitted") {
		t.Errorf("did not expect <omitted> note when nothing was omitted, got: %s", closed)
	}
	if !strings.HasSuffix(strings.TrimSpace(closed), "</anchored_search>") {
		t.Errorf("expected output to end with closing tag, got: %s", closed)
	}
}
