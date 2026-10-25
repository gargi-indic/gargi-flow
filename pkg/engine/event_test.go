package engine

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestEventType_Valid(t *testing.T) {
	tests := []struct {
		name string
		et   EventType
		want bool
	}{
		{"NodeStateChanged", EventTypeNodeStateChanged, true},
		{"NodeCompleted", EventTypeNodeCompleted, true},
		{"NodeFailed", EventTypeNodeFailed, true},
		{"ExternalSignalReceived", EventTypeExternalSignalReceived, true},
		{"GraphCompleted", EventTypeGraphCompleted, true},
		{"GraphFailed", EventTypeGraphFailed, true},
		{"Invalid", EventType("invalid_event"), false},
		{"Empty", EventType(""), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.et.Valid(); got != tt.want {
				t.Errorf("EventType.Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewEvent_Validation(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name    string
		execID  ExecutionID
		seq     uint64
		ts      time.Time
		et      EventType
		nodeID  NodeID
		payload any
		wantErr bool
	}{
		{"valid node event", "exec-1", 1, now, EventTypeNodeCompleted, "node-1", nil, false},
		{"valid graph event", "exec-1", 1, now, EventTypeGraphCompleted, "", nil, false},
		{"empty execID", "", 1, now, EventTypeNodeCompleted, "node-1", nil, true},
		{"zero sequence", "exec-1", 0, now, EventTypeNodeCompleted, "node-1", nil, true},
		{"zero timestamp", "exec-1", 1, time.Time{}, EventTypeNodeCompleted, "node-1", nil, true},
		{"invalid event type", "exec-1", 1, now, EventType("bad"), "node-1", nil, true},
		{"missing nodeID for node event", "exec-1", 1, now, EventTypeNodeCompleted, "", nil, true},
		{"missing nodeID for node state changed", "exec-1", 1, now, EventTypeNodeStateChanged, "", nil, true},
		{"missing nodeID for node failed", "exec-1", 1, now, EventTypeNodeFailed, "", nil, true},
		{"missing nodeID for external signal", "exec-1", 1, now, EventTypeExternalSignalReceived, "", nil, true},
		{"unexpected nodeID for graph event", "exec-1", 1, now, EventTypeGraphCompleted, "node-1", nil, true},
		{"unexpected nodeID for graph failed", "exec-1", 1, now, EventTypeGraphFailed, "node-1", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewEvent(tt.execID, tt.seq, tt.ts, tt.et, tt.nodeID, tt.payload)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewEvent() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNewEvent_UTCNormalization(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2023, 10, 26, 12, 0, 0, 0, loc)
	e, err := NewEvent("exec-1", 1, ts, EventTypeGraphCompleted, "", nil)
	if err != nil {
		t.Fatalf("NewEvent() error = %v", err)
	}
	if e.Timestamp().Location() != time.UTC {
		t.Errorf("Expected UTC timestamp, got %v", e.Timestamp().Location())
	}
	if !e.Timestamp().Equal(ts) {
		t.Errorf("Expected equal time, got %v != %v", e.Timestamp(), ts)
	}
}

func TestNewEvent_Immutability_NodeStateChanged(t *testing.T) {
	patch := map[string]any{"k": "v"}
	payload := NodeStateChangedPayload{
		OldStatus:   StatusPending,
		NewStatus:   StatusRunning,
		OutputPatch: patch,
	}

	e, err := NewEvent("exec-1", 1, time.Now(), EventTypeNodeStateChanged, "node-1", payload)
	if err != nil {
		t.Fatal(err)
	}

	// Mutate original
	patch["k"] = "mutated"

	retrieved := e.Payload().(NodeStateChangedPayload)
	if retrieved.OutputPatch["k"] != "v" {
		t.Errorf("Expected original value 'v', got %v", retrieved.OutputPatch["k"])
	}

	// Mutate retrieved
	retrieved.OutputPatch["k"] = "mutated_again"
	retrieved2 := e.Payload().(NodeStateChangedPayload)
	if retrieved2.OutputPatch["k"] != "v" {
		t.Errorf("Expected original value 'v', got %v", retrieved2.OutputPatch["k"])
	}
}

func TestNewEvent_Immutability_NodeCompleted(t *testing.T) {
	output := map[string]any{"k": "v"}
	payload := NodeCompletedPayload{
		Status: StatusSucceeded,
		Output: output,
	}

	e, err := NewEvent("exec-1", 1, time.Now(), EventTypeNodeCompleted, "node-1", payload)
	if err != nil {
		t.Fatal(err)
	}

	// Mutate original
	output["k"] = "mutated"

	retrieved := e.Payload().(NodeCompletedPayload)
	if retrieved.Output["k"] != "v" {
		t.Errorf("Expected original value 'v', got %v", retrieved.Output["k"])
	}

	// Mutate retrieved
	retrieved.Output["k"] = "mutated_again"
	retrieved2 := e.Payload().(NodeCompletedPayload)
	if retrieved2.Output["k"] != "v" {
		t.Errorf("Expected original value 'v', got %v", retrieved2.Output["k"])
	}
}

func TestNewEvent_Immutability_ExternalSignalReceived(t *testing.T) {
	signalPayload := map[string]any{"k": "v"}
	payload := ExternalSignalReceivedPayload{
		SignalName: "sig1",
		Payload:    signalPayload,
	}

	e, err := NewEvent("exec-1", 1, time.Now(), EventTypeExternalSignalReceived, "node-1", payload)
	if err != nil {
		t.Fatal(err)
	}

	// Mutate original
	signalPayload["k"] = "mutated"

	retrieved := e.Payload().(ExternalSignalReceivedPayload)
	retrievedMap := retrieved.Payload.(map[string]any)
	if retrievedMap["k"] != "v" {
		t.Errorf("Expected original value 'v', got %v", retrievedMap["k"])
	}

	// Mutate retrieved
	retrievedMap["k"] = "mutated_again"
	retrieved2 := e.Payload().(ExternalSignalReceivedPayload)
	retrievedMap2 := retrieved2.Payload.(map[string]any)
	if retrievedMap2["k"] != "v" {
		t.Errorf("Expected original value 'v', got %v", retrievedMap2["k"])
	}
}

func TestEvent_JSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond) // JSON precision
	execID := ExecutionID("exec-1")

	tests := []struct {
		name      string
		eventType EventType
		nodeID    NodeID
		payload   any
	}{
		{
			name:      "NodeStateChanged",
			eventType: EventTypeNodeStateChanged,
			nodeID:    "node-1",
			payload: NodeStateChangedPayload{
				OldStatus:   StatusPending,
				NewStatus:   StatusRunning,
				OutputPatch: map[string]any{"key": "value"},
				Version:     1,
			},
		},
		{
			name:      "NodeCompleted",
			eventType: EventTypeNodeCompleted,
			nodeID:    "node-1",
			payload: NodeCompletedPayload{
				Status:  StatusSucceeded,
				Output:  map[string]any{"result": float64(42)}, // json unmarshals numbers to float64
				Version: 2,
			},
		},
		{
			name:      "NodeCompletedWithPolicy",
			eventType: EventTypeNodeCompleted,
			nodeID:    "node-decision",
			payload: NodeCompletedPayload{
				Status:        StatusSucceeded,
				Output:        map[string]any{"choice": "approve"},
				Version:       2,
				PolicyID:      "underwriting-policy",
				PolicyVersion: "v1.2",
			},
		},
		{
			name:      "NodeFailed",
			eventType: EventTypeNodeFailed,
			nodeID:    "node-1",
			payload: NodeFailedPayload{
				Status:       StatusFailed,
				ErrorClass:   ClassRetryable,
				ErrorMessage: "timeout",
				Version:      3,
			},
		},
		{
			name:      "ExternalSignalReceived",
			eventType: EventTypeExternalSignalReceived,
			nodeID:    "node-1",
			payload: ExternalSignalReceivedPayload{
				SignalName: "proceed",
				Payload:    map[string]any{"foo": "bar"},
			},
		},
		{
			name:      "GraphCompleted",
			eventType: EventTypeGraphCompleted,
			nodeID:    "",
			payload: GraphCompletedPayload{
				FinalStatus: StatusSucceeded,
				Version:     4,
			},
		},
		{
			name:      "GraphFailed",
			eventType: EventTypeGraphFailed,
			nodeID:    "",
			payload: GraphFailedPayload{
				ErrorMessage: "critical failure",
				Version:      5,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test Value roundtrip
			originalValue, err := NewEvent(execID, 1, now, tt.eventType, tt.nodeID, tt.payload)
			if err != nil {
				t.Fatalf("NewEvent failed: %v", err)
			}

			// Value marshaling
			bValue, err := json.Marshal(*originalValue)
			if err != nil {
				t.Fatalf("json.Marshal (value) failed: %v", err)
			}

			var decodedValue Event
			if err := json.Unmarshal(bValue, &decodedValue); err != nil {
				t.Fatalf("json.Unmarshal failed: %v", err)
			}

			if !reflect.DeepEqual(*originalValue, decodedValue) {
				t.Errorf("Value unmarshal mismatch.\nGot: %#v\nWant: %#v", decodedValue, *originalValue)
			}

			if reflect.TypeOf(decodedValue.Payload()) != reflect.TypeOf(tt.payload) {
				t.Errorf("Expected payload type %T, got %T", tt.payload, decodedValue.Payload())
			}

			// Test Pointer roundtrip
			bPtr, err := json.Marshal(originalValue)
			if err != nil {
				t.Fatalf("json.Marshal (pointer) failed: %v", err)
			}

			var decodedPtr *Event
			if err := json.Unmarshal(bPtr, &decodedPtr); err != nil {
				t.Fatalf("json.Unmarshal (pointer) failed: %v", err)
			}

			if !reflect.DeepEqual(originalValue, decodedPtr) {
				t.Errorf("Pointer unmarshal mismatch.\nGot: %#v\nWant: %#v", decodedPtr, originalValue)
			}
		})
	}
}

func TestEvent_JSONUnmarshal_Invalid(t *testing.T) {
	invalidJSONs := []string{
		`{"executionId": "", "sequence": 1, "timestamp": "2023-10-26T12:00:00Z", "type": "node_completed", "nodeId": "node-1"}`,        // empty execID
		`{"executionId": "exec-1", "sequence": 0, "timestamp": "2023-10-26T12:00:00Z", "type": "node_completed", "nodeId": "node-1"}`,  // zero sequence
		`{"executionId": "exec-1", "sequence": 1, "timestamp": "0001-01-01T00:00:00Z", "type": "node_completed", "nodeId": "node-1"}`,  // zero timestamp
		`{"executionId": "exec-1", "sequence": 1, "timestamp": "2023-10-26T12:00:00Z", "type": "invalid_type", "nodeId": "node-1"}`,    // invalid type
		`{"executionId": "exec-1", "sequence": 1, "timestamp": "2023-10-26T12:00:00Z", "type": "node_completed"}`,                      // missing nodeId
		`{"executionId": "exec-1", "sequence": 1, "timestamp": "2023-10-26T12:00:00Z", "type": "graph_completed", "nodeId": "node-1"}`, // nodeId present for graph event
	}

	for i, j := range invalidJSONs {
		var e Event
		if err := json.Unmarshal([]byte(j), &e); err == nil {
			t.Errorf("expected error unmarshaling invalid json[%d]: %s", i, j)
		}
	}
}

func TestEvent_Accessors_NilReceiver(t *testing.T) {
	var e *Event // nil

	if got := e.ExecutionID(); got != "" {
		t.Errorf("ExecutionID() on nil = %v, want empty", got)
	}
	if got := e.Sequence(); got != 0 {
		t.Errorf("Sequence() on nil = %v, want 0", got)
	}
	if got := e.Timestamp(); !got.IsZero() {
		t.Errorf("Timestamp() on nil = %v, want zero time", got)
	}
	if got := e.Type(); got != "" {
		t.Errorf("Type() on nil = %v, want empty", got)
	}
	if got := e.NodeID(); got != "" {
		t.Errorf("NodeID() on nil = %v, want empty", got)
	}
	if got := e.Payload(); got != nil {
		t.Errorf("Payload() on nil = %v, want nil", got)
	}
}

func TestNewEvent_PointerPayloadsDereferenced(t *testing.T) {
	tests := []struct {
		name      string
		eventType EventType
		nodeID    NodeID
		payload   any
		wantType  reflect.Type
	}{
		{
			name:      "NodeStateChanged pointer",
			eventType: EventTypeNodeStateChanged,
			nodeID:    "node-1",
			payload:   &NodeStateChangedPayload{},
			wantType:  reflect.TypeOf(NodeStateChangedPayload{}),
		},
		{
			name:      "NodeCompleted pointer",
			eventType: EventTypeNodeCompleted,
			nodeID:    "node-1",
			payload:   &NodeCompletedPayload{},
			wantType:  reflect.TypeOf(NodeCompletedPayload{}),
		},
		{
			name:      "NodeFailed pointer",
			eventType: EventTypeNodeFailed,
			nodeID:    "node-1",
			payload:   &NodeFailedPayload{},
			wantType:  reflect.TypeOf(NodeFailedPayload{}),
		},
		{
			name:      "ExternalSignalReceived pointer",
			eventType: EventTypeExternalSignalReceived,
			nodeID:    "node-1",
			payload:   &ExternalSignalReceivedPayload{},
			wantType:  reflect.TypeOf(ExternalSignalReceivedPayload{}),
		},
		{
			name:      "GraphCompleted pointer",
			eventType: EventTypeGraphCompleted,
			nodeID:    "",
			payload:   &GraphCompletedPayload{},
			wantType:  reflect.TypeOf(GraphCompletedPayload{}),
		},
		{
			name:      "GraphFailed pointer",
			eventType: EventTypeGraphFailed,
			nodeID:    "",
			payload:   &GraphFailedPayload{},
			wantType:  reflect.TypeOf(GraphFailedPayload{}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, err := NewEvent("exec-1", 1, time.Now(), tt.eventType, tt.nodeID, tt.payload)
			if err != nil {
				t.Fatalf("NewEvent failed: %v", err)
			}

			gotType := reflect.TypeOf(e.Payload())
			if gotType != tt.wantType {
				t.Errorf("Expected payload type %v, got %v", tt.wantType, gotType)
			}
		})
	}
}
