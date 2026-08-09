package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// backgroundDryRunAppState extracts body.appState from a dry-run PATCH envelope.
func backgroundDryRunAppState(t *testing.T, data map[string]any) map[string]any {
	t.Helper()
	body, ok := data["body"].(map[string]any)
	if !ok {
		t.Fatalf("body not object: %v", data["body"])
	}
	appState, ok := body["appState"].(map[string]any)
	if !ok {
		t.Fatalf("appState not object: %v", body["appState"])
	}
	return appState
}

func TestSceneBackgroundSetDryRun(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[],"files":{},"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "background", "set", "d1", "#f8f9fa")
	if err != nil {
		t.Fatal(err)
	}
	data := dryRunData(t, out)
	appState := backgroundDryRunAppState(t, data)
	if appState["viewBackgroundColor"] != "#f8f9fa" {
		t.Fatalf("viewBackgroundColor=%v", appState["viewBackgroundColor"])
	}
}

func TestSceneBackgroundResetWritesDefault(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[],"files":{},"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "background", "reset", "d1")
	if err != nil {
		t.Fatal(err)
	}
	appState := backgroundDryRunAppState(t, dryRunData(t, out))
	if appState["viewBackgroundColor"] != defaultViewBackgroundColor {
		t.Fatalf("reset must write %q, got %v", defaultViewBackgroundColor, appState["viewBackgroundColor"])
	}
}

func TestSceneBackgroundSetTrimsWhitespace(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[],"files":{},"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "background", "set", "d1", "  #abc  ")
	if err != nil {
		t.Fatal(err)
	}
	appState := backgroundDryRunAppState(t, dryRunData(t, out))
	if appState["viewBackgroundColor"] != "#abc" {
		t.Fatalf("expected trimmed #abc, got %v", appState["viewBackgroundColor"])
	}
}

func TestSceneBackgroundSetRejectsInvalidColor(t *testing.T) {
	for _, bad := range []string{
		"not a color!",
		"#12",                                   // too-short hex
		"#zzzzzz",                               // non-hex digits
		"javascript:x",                          // punctuation-bearing garbage
		"rgb(1;2;3)",                            // illegal separators
		"#1234567890abcdef1234567890abcdef1234", // over length bound
	} {
		// A GET must never happen: validation fails before the scene is read.
		_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("invalid color %q must not trigger a request (%s)", bad, r.Method)
		})
		_, _, err := execRoot(t, cap.f, "docs", "scene", "background", "set", "d1", bad)
		if err == nil {
			t.Fatalf("expected rejection for %q", bad)
		}
		if cap.requests != 0 {
			t.Fatalf("color %q sent %d requests; want 0", bad, cap.requests)
		}
	}
}

func TestSceneBackgroundSetRoundTrip(t *testing.T) {
	var patch map[string]any
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"elements":[],"files":{},"appState":{"viewBackgroundColor":"#111111"},"baseVersion":"BV"}`)
			return
		}
		if r.Header.Get("If-Match") != "BV" {
			t.Fatalf("If-Match=%q", r.Header.Get("If-Match"))
		}
		_ = json.NewDecoder(r.Body).Decode(&patch)
		io.WriteString(w, `{"docId":"d1","baseVersion":"BV2"}`)
	})
	_, _, err := execRoot(t, cap.f, "docs", "scene", "background", "set", "d1", "#222222")
	if err != nil {
		t.Fatal(err)
	}
	appState, _ := patch["appState"].(map[string]any)
	if appState["viewBackgroundColor"] != "#222222" {
		t.Fatalf("patch appState=%v", patch["appState"])
	}
}

func TestSceneBackgroundGet(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("background get must not %s", r.Method)
		}
		io.WriteString(w, `{"elements":[],"files":{},"appState":{"viewBackgroundColor":"#abcdef"},"baseVersion":"BV"}`)
	})
	out, _, err := execRoot(t, cap.f, "docs", "scene", "background", "get", "d1")
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope: %v\n%s", err, out)
	}
	data, _ := env["data"].(map[string]any)
	if data["viewBackgroundColor"] != "#abcdef" {
		t.Fatalf("viewBackgroundColor=%v", data["viewBackgroundColor"])
	}
	if data["isDefault"] != false {
		t.Fatalf("isDefault=%v", data["isDefault"])
	}
}

func TestSceneBackgroundGetDefaultsWhenUnset(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"elements":[],"files":{},"baseVersion":"BV"}`)
	})
	out, _, err := execRoot(t, cap.f, "docs", "scene", "background", "get", "d1")
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	_ = json.Unmarshal([]byte(out), &env)
	data, _ := env["data"].(map[string]any)
	if data["viewBackgroundColor"] != defaultViewBackgroundColor {
		t.Fatalf("expected default color, got %v", data["viewBackgroundColor"])
	}
	if data["isDefault"] != true {
		t.Fatalf("isDefault=%v", data["isDefault"])
	}
}

func TestIsValidCanvasColor(t *testing.T) {
	valid := []string{"#fff", "#ffff", "#ffffff", "#ffffffff", "rgb(1,2,3)", "rgba(1,2,3,0.5)", "hsl(1,2%,3%)", "transparent", "red"}
	for _, c := range valid {
		if !isValidCanvasColor(normalizeCanvasColor(c)) {
			t.Errorf("expected %q valid", c)
		}
	}
	invalid := []string{"", "#12", "#gggggg", "not a color!", "rgb(1;2;3)", "#12345678901234567890123456789012345"}
	for _, c := range invalid {
		if isValidCanvasColor(normalizeCanvasColor(c)) {
			t.Errorf("expected %q invalid", c)
		}
	}
}
