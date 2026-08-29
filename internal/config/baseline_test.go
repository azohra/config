package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestClassify(t *testing.T) {
	base := Baseline{Content: json.RawMessage(`{"value":"base"}`)}
	tests := []struct {
		name        string
		saved, live string
		hasBase     bool
		want        State
	}{
		{"current", `{"value":"same"}`, `{"value":"same"}`, false, Current},
		{"unavailable", ``, `{"value":"live"}`, false, Unavailable},
		{"unknown", `{"value":"saved"}`, `{"value":"live"}`, false, Unknown},
		{"live changed", `{"value":"base"}`, `{"value":"live"}`, true, LiveChanged},
		{"saved changed", `{"value":"saved"}`, `{"value":"base"}`, true, SavedChanged},
		{"conflict", `{"value":"saved"}`, `{"value":"live"}`, true, Conflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(json.RawMessage(tt.saved), json.RawMessage(tt.live), base, tt.hasBase)
			if got != tt.want {
				t.Fatalf("Classify() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBaselinesRoundTrip(t *testing.T) {
	store := Baselines{Dir: t.TempDir()}
	want := json.RawMessage(`{"value":["one","two"]}`)
	if err := store.Save("dock", want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.Load("dock")
	if err != nil || !ok {
		t.Fatalf("Load() ok=%v err=%v", ok, err)
	}
	if string(got.Content) != string(want) {
		t.Fatalf("content = %s, want %s", got.Content, want)
	}
	path := filepath.Join(store.Dir, "dock.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("dock", want); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("unchanged baseline was rewritten")
	}
}
