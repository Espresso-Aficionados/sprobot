package bot

import (
	"encoding/json"
	"testing"
)

func TestWelcomeStateJSONRoundTrip(t *testing.T) {
	st := &welcomeState{Message: "Hello new member!", Enabled: true}

	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}

	var loaded welcomeState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}

	if loaded.Message != "Hello new member!" {
		t.Errorf("expected 'Hello new member!', got %q", loaded.Message)
	}
	if !loaded.Enabled {
		t.Error("expected enabled to be true")
	}
}

func TestWelcomeStateJSONOmitsMutex(t *testing.T) {
	st := &welcomeState{Message: "Welcome!", Enabled: true}

	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}

	s := string(data)
	if s != `{"message":"Welcome!","enabled":true}` {
		t.Errorf("unexpected JSON: %s", s)
	}
}

func TestWelcomeStateEmptyMessage(t *testing.T) {
	st := &welcomeState{}

	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}

	var loaded welcomeState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}

	if loaded.Message != "" {
		t.Errorf("expected empty message, got %q", loaded.Message)
	}
	if loaded.Enabled {
		t.Error("expected enabled to be false by default")
	}
}

func TestWelcomeStateClear(t *testing.T) {
	st := &welcomeState{Message: "Hello!", Enabled: true}
	st.Message = ""

	if st.Message != "" {
		t.Errorf("expected empty message after clear, got %q", st.Message)
	}
	if !st.Enabled {
		t.Error("expected enabled to remain true after clearing message")
	}
}

func TestWelcomeStateDisablePreservesMessage(t *testing.T) {
	st := &welcomeState{Message: "Hello!", Enabled: true}
	st.Enabled = false

	if st.Message != "Hello!" {
		t.Errorf("expected message preserved, got %q", st.Message)
	}
	if st.Enabled {
		t.Error("expected enabled to be false")
	}

	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}

	var loaded welcomeState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}

	if loaded.Message != "Hello!" {
		t.Errorf("expected message preserved after round-trip, got %q", loaded.Message)
	}
	if loaded.Enabled {
		t.Error("expected disabled after round-trip")
	}
}

func TestWelcomeStateEnableAfterDisable(t *testing.T) {
	st := &welcomeState{Message: "Hello!", Enabled: false}
	st.Enabled = true

	if !st.Enabled {
		t.Error("expected enabled to be true")
	}
	if st.Message != "Hello!" {
		t.Errorf("expected message preserved, got %q", st.Message)
	}
}

func TestWelcomeStateBackwardsCompatibility(t *testing.T) {
	// Old data without "enabled" field should default to false
	data := []byte(`{"message":"Old message"}`)

	var st welcomeState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}

	if st.Message != "Old message" {
		t.Errorf("expected 'Old message', got %q", st.Message)
	}
	if st.Enabled {
		t.Error("expected enabled to default to false for old data")
	}
}
