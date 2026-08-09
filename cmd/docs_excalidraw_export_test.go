package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClearElementsForExport(t *testing.T) {
	elements := []map[string]any{
		{"id": "a", "type": "rectangle", "isDeleted": false},
		{"id": "b", "type": "rectangle", "isDeleted": true}, // dropped
		{"id": "c", "type": "arrow", "lastCommittedPoint": []any{float64(1), float64(2)}},
		{"id": "d", "type": "line", "lastCommittedPoint": []any{float64(3), float64(4)}},
	}
	out := clearElementsForExport(elements)
	if len(out) != 3 {
		t.Fatalf("expected 3 surviving elements, got %d", len(out))
	}
	if out[0]["id"] != "a" || out[1]["id"] != "c" || out[2]["id"] != "d" {
		t.Fatalf("order/content wrong: %v", out)
	}
	if out[1]["lastCommittedPoint"] != nil || out[2]["lastCommittedPoint"] != nil {
		t.Fatalf("linear lastCommittedPoint must be nulled: %v %v", out[1], out[2])
	}
	// The original element must not be mutated (clone-on-write for linear).
	if elements[2]["lastCommittedPoint"] == nil {
		t.Fatal("clearElementsForExport mutated the source element")
	}
}

func TestCleanAppStateForExport(t *testing.T) {
	in := map[string]any{
		"viewBackgroundColor": "#ffffff",
		"gridSize":            float64(20),
		"gridStep":            float64(5),
		"gridModeEnabled":     true,
		"theme":               "dark",     // stripped
		"scrollX":             float64(9), // stripped
		"name":                "board",    // stripped
	}
	out := cleanAppStateForExport(in)
	if len(out) != 4 {
		t.Fatalf("expected only the 4 allowlisted keys, got %v", out)
	}
	for _, k := range []string{"viewBackgroundColor", "gridSize", "gridStep", "gridModeEnabled"} {
		if _, ok := out[k]; !ok {
			t.Errorf("missing allowlisted key %q", k)
		}
	}
	for _, k := range []string{"theme", "scrollX", "name"} {
		if _, ok := out[k]; ok {
			t.Errorf("non-allowlisted key %q leaked into export", k)
		}
	}
}

func TestReferencedFileIDs(t *testing.T) {
	elements := []map[string]any{
		{"id": "img1", "type": "image", "fileId": "f1"},
		{"id": "img2", "type": "image", "fileId": "f2"},
		{"id": "rect", "type": "rectangle"},
	}
	ids := referencedFileIDs(elements)
	if len(ids) != 2 || !ids["f1"] || !ids["f2"] {
		t.Fatalf("referenced fileIds = %v", ids)
	}
}

func TestMarshalExcalidrawEnvelopeOrderAndEscaping(t *testing.T) {
	elements := []map[string]any{{"id": "a", "type": "text", "text": "1 < 2 && 3 > 2"}}
	data, err := marshalExcalidrawEnvelope(elements, map[string]any{"viewBackgroundColor": "#fff"}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	// Top-level key order must match serializeAsJSON: type,version,source,elements,appState,files.
	order := []string{`"type"`, `"version"`, `"source"`, `"elements"`, `"appState"`, `"files"`}
	last := -1
	for _, key := range order {
		i := strings.Index(s, key)
		if i < 0 {
			t.Fatalf("missing key %s in %s", key, s)
		}
		if i < last {
			t.Fatalf("key %s out of order in %s", key, s)
		}
		last = i
	}
	// HTML escaping disabled: < > & are written literally (as JSON.stringify does),
	// not as the < / > / & forms Go's default encoder emits.
	if !strings.Contains(s, "1 < 2 && 3 > 2") {
		t.Fatalf("expected unescaped text, got %s", s)
	}
	for _, esc := range []string{"\\u003c", "\\u003e", "\\u0026"} {
		if strings.Contains(s, esc) {
			t.Fatalf("HTML escaping must be disabled (found %s): %s", esc, s)
		}
	}
	// A valid, re-parseable version-2 envelope.
	var env map[string]any
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if env["type"] != "excalidraw" || env["version"] != float64(2) || env["source"] != "octo-cli" {
		t.Fatalf("envelope header = %v", env)
	}
}

func TestDocsExcalidrawExportRejectsBadExtension(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[],"files":{},"baseVersion":"BV"}`))
	_, _, err := execRoot(t, cap.f, "docs", "save-excalidraw", "d1", "-o", "board.json")
	if err == nil {
		t.Fatal("expected rejection for non-.excalidraw extension")
	}
}

func TestDocsExcalidrawExportDryRunNoWriteNoDownload(t *testing.T) {
	body := `{"elements":[{"id":"a","type":"rectangle","isDeleted":false},{"id":"b","type":"rectangle","isDeleted":true},{"id":"img","type":"image","fileId":"f1"}],"files":{"f1":{"attachId":"att1"}},"appState":{"viewBackgroundColor":"#eee","theme":"dark"},"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	dir := t.TempDir()
	out := filepath.Join(dir, "board.excalidraw")
	stdout, _, err := execRoot(t, cap.f, "--dry-run", "docs", "save-excalidraw", "d1", "-o", out)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Fatal("dry-run must not write the file")
	}
	if cap.requests != 1 {
		// Only the scene GET; no attachment resolve/download during dry-run.
		t.Fatalf("dry-run made %d requests; want 1 (scene GET only)", cap.requests)
	}
	var env map[string]any
	_ = json.Unmarshal([]byte(stdout), &env)
	data, _ := env["data"].(map[string]any)
	if data["elements"] != float64(2) { // deleted 'b' excluded
		t.Fatalf("dry-run elements=%v, want 2", data["elements"])
	}
	if data["files"] != float64(1) { // 'f1' still referenced by live image
		t.Fatalf("dry-run files=%v, want 1", data["files"])
	}
	keys, _ := data["appState"].([]any)
	if len(keys) != 1 || keys[0] != "viewBackgroundColor" {
		t.Fatalf("dry-run appState keys=%v, want [viewBackgroundColor]", data["appState"])
	}
}

func TestValidatePortableAttachmentRefsFailsLoud(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]any
		want  string
	}{
		{name: "missing", files: map[string]any{}, want: `attachment "f1" is referenced`},
		{name: "invalid metadata", files: map[string]any{"f1": "bad"}, want: `attachment "f1" has invalid`},
		{name: "missing attachId", files: map[string]any{"f1": map[string]any{}}, want: `attachment "f1" is missing attachId`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validatePortableAttachmentRefs(tc.files, map[string]bool{"f1": true})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestValidatePortableAttachmentRefsEnforcesCountLimit(t *testing.T) {
	files := map[string]any{}
	referenced := map[string]bool{}
	for i := 0; i <= maxPortableExportAttachments; i++ {
		id := fmt.Sprintf("f%d", i)
		files[id] = map[string]any{"attachId": "att_" + id}
		referenced[id] = true
	}
	_, err := validatePortableAttachmentRefs(files, referenced)
	if err == nil || !strings.Contains(err.Error(), "limit is") {
		t.Fatalf("err=%v", err)
	}
}

func TestDocsExcalidrawExportFailsBeforeWriteOnMissingFile(t *testing.T) {
	body := `{"elements":[{"id":"img","type":"image","fileId":"f_missing"}],"files":{},"appState":{},"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out := filepath.Join(t.TempDir(), "board.excalidraw")
	_, _, err := execRoot(t, cap.f, "docs", "save-excalidraw", "d1", "-o", out)
	if err == nil || !strings.Contains(err.Error(), "f_missing") {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatalf("output unexpectedly exists: %v", statErr)
	}
}

func TestDocsExcalidrawExportWritesAtomicFile(t *testing.T) {
	body := `{"elements":[{"id":"a","type":"rectangle","isDeleted":false},{"id":"b","type":"rectangle","isDeleted":true}],"files":{},"appState":{"viewBackgroundColor":"#123456","zoom":2},"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	dir := t.TempDir()
	out := filepath.Join(dir, "board.excalidraw")
	_, _, err := execRoot(t, cap.f, "docs", "save-excalidraw", "d1", "-o", out)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("output file: %v", err)
	}
	var env struct {
		Type     string           `json:"type"`
		Version  int              `json:"version"`
		Elements []map[string]any `json:"elements"`
		AppState map[string]any   `json:"appState"`
		Files    map[string]any   `json:"files"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("written file is not valid JSON: %v\n%s", err, raw)
	}
	if env.Type != "excalidraw" || env.Version != 2 {
		t.Fatalf("header = %s/%d", env.Type, env.Version)
	}
	if len(env.Elements) != 1 || env.Elements[0]["id"] != "a" {
		t.Fatalf("deleted element not stripped: %v", env.Elements)
	}
	if len(env.AppState) != 1 || env.AppState["viewBackgroundColor"] != "#123456" {
		t.Fatalf("appState not filtered to allowlist: %v", env.AppState)
	}
	// No temp files should remain in the destination directory.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected exactly the output file, found %d entries", len(entries))
	}
}
