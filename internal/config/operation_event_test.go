package config

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestOperationEventWriterPreservesSplitUTF8AndTypedEvents(t *testing.T) {
	var output bytes.Buffer
	writer := NewOperationEventWriter(&output)
	check := []byte(GlyphOK)
	if _, err := writer.Write(check[:1]); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(check[1:]); err != nil {
		t.Fatal(err)
	}
	Logger{Out: writer}.OK("current")
	Logger{Out: writer}.Version("v1.2.3")
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	decoder := json.NewDecoder(&output)
	var events []OperationEvent
	for decoder.More() {
		var event OperationEvent
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if len(events) != 3 || events[0] != (OperationEvent{Kind: OperationOutput, Text: GlyphOK}) ||
		events[1] != (OperationEvent{Kind: OperationOK, Text: "current"}) ||
		events[2] != (OperationEvent{Kind: OperationVersion, Text: "v1.2.3"}) {
		t.Fatalf("events = %#v", events)
	}
}
