package protocol

import (
	"encoding/json"
	"testing"
)

func TestThreadStartParams_MarshalRoundTrip(t *testing.T) {
	cwd := "/tmp/test"
	model := "claude-sonnet-4-20250514"
	params := ThreadStartParams{
		Cwd:   &cwd,
		Model: &model,
	}

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ThreadStartParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Cwd == nil || *decoded.Cwd != cwd {
		t.Errorf("cwd: got %v, want %q", decoded.Cwd, cwd)
	}
	if decoded.Model == nil || *decoded.Model != model {
		t.Errorf("model: got %v, want %q", decoded.Model, model)
	}
}

func TestTurnStartParams_MarshalRoundTrip(t *testing.T) {
	threadID := "thread-123"
	params := TurnStartParams{
		ThreadID: threadID,
		Input:    []UserInput{},
	}

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded TurnStartParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.ThreadID != threadID {
		t.Errorf("threadId: got %q, want %q", decoded.ThreadID, threadID)
	}
}

func TestClientRequest_UnmarshalAndGetParams(t *testing.T) {
	raw := `{
		"id": "req-1",
		"method": "thread/start",
		"params": {
			"cwd": "/home/user/project",
			"model": "claude-sonnet-4-20250514"
		}
	}`

	var req ClientRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if req.Method != MethodThreadStart {
		t.Errorf("method: got %q, want %q", req.Method, MethodThreadStart)
	}

	params, err := req.ThreadStartParams()
	if err != nil {
		t.Fatalf("ThreadStartParams: %v", err)
	}

	if params.Cwd == nil || *params.Cwd != "/home/user/project" {
		t.Errorf("cwd: got %v, want %q", params.Cwd, "/home/user/project")
	}
}

func TestServerNotification_Unmarshal(t *testing.T) {
	raw := `{
		"method": "item/agentMessage/delta",
		"params": {
			"delta": "Hello from the agent",
			"itemId": "item-1",
			"threadId": "thread-1"
		}
	}`

	var notif ServerNotification
	if err := json.Unmarshal([]byte(raw), &notif); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if notif.Method != NotifItemAgentMessageDelta {
		t.Errorf("method: got %q, want %q", notif.Method, NotifItemAgentMessageDelta)
	}

	params, err := notif.ItemAgentMessageDeltaParams()
	if err != nil {
		t.Fatalf("params: %v", err)
	}
	if params.Delta != "Hello from the agent" {
		t.Errorf("delta: got %q, want %q", params.Delta, "Hello from the agent")
	}
}

func TestEnumValues(t *testing.T) {
	tests := []struct {
		name string
		val  string
		want string
	}{
		{"SandboxMode", string(SandboxModeDangerFullAccess), "danger-full-access"},
		{"CommandExecutionStatus", string(CommandExecutionStatusCompleted), "completed"},
		{"ModeKind", string(ModeKindPlan), "plan"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.val != tt.want {
				t.Errorf("got %q, want %q", tt.val, tt.want)
			}
		})
	}
}

func TestMethodConstants(t *testing.T) {
	if MethodInitialize != "initialize" {
		t.Errorf("MethodInitialize: got %q", MethodInitialize)
	}
	if MethodThreadStart != "thread/start" {
		t.Errorf("MethodThreadStart: got %q", MethodThreadStart)
	}
	if MethodTurnStart != "turn/start" {
		t.Errorf("MethodTurnStart: got %q", MethodTurnStart)
	}
	if NotifError != "error" {
		t.Errorf("NotifError: got %q", NotifError)
	}
	if ReqItemCommandExecutionRequestApproval != "item/commandExecution/requestApproval" {
		t.Errorf("ReqItemCommandExecutionRequestApproval: got %q", ReqItemCommandExecutionRequestApproval)
	}
}

func TestNullableFieldsOmitWhenNil(t *testing.T) {
	params := ThreadStartParams{}

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Should be mostly empty object since all fields are optional
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}

	// None of the optional pointer fields should be present
	if _, ok := m["cwd"]; ok {
		t.Error("nil cwd should be omitted")
	}
	if _, ok := m["model"]; ok {
		t.Error("nil model should be omitted")
	}
}
