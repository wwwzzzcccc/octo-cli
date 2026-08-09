package cmd

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Mininglamp-OSS/octo-cli/internal/client"
	"github.com/Mininglamp-OSS/octo-cli/internal/cmdutil"
	"github.com/Mininglamp-OSS/octo-cli/internal/config"
	"github.com/Mininglamp-OSS/octo-cli/internal/credential"
)

func semanticFactory(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *testCapture) {
	t.Helper()
	cap := &testCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { cap.requests++; handler(w, r) }))
	t.Cleanup(srv.Close)
	f := newTestFactoryWithReg()
	cfg := &config.Config{APIBaseURL: srv.URL, BotToken: "app_test", Format: "json"}
	cred := &credential.BotCredential{Token: "app_test"}
	f.SetConfig(cfg)
	f.SetCredential(cred)
	f.SetClient(client.New(cfg, cred, client.Options{NoRetry: true, ErrOut: f.ErrOut}))
	cap.f = f
	return srv, cap
}

type testCapture struct {
	f        *cmdutil.TestFactory
	requests int
}

func TestSceneElementUpdatePreservesUnknownAndVersions(t *testing.T) {
	var patch map[string]any
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"elements":[{"id":"e1","type":"rectangle","x":1,"y":2,"version":7,"versionNonce":3,"index":"a0","futureField":{"x":1}}],"files":{},"baseVersion":"BV"}`)
			return
		}
		if r.Header.Get("If-Match") != "BV" {
			t.Fatalf("If-Match=%q", r.Header.Get("If-Match"))
		}
		_ = json.NewDecoder(r.Body).Decode(&patch)
		io.WriteString(w, `{}`)
	})
	f := cap.f
	_, _, err := execRoot(t, f, "docs", "scene", "element", "transform", "d1", "e1", "--x", "50")
	if err != nil {
		t.Fatal(err)
	}
	e := patch["elements"].([]any)[0].(map[string]any)
	if e["futureField"] == nil || e["version"] != float64(8) || e["x"] != float64(50) {
		t.Fatalf("patch=%v", patch)
	}
	if e["versionNonce"].(float64) < 0 || e["versionNonce"] == float64(3) {
		t.Fatalf("nonce=%v", e["versionNonce"])
	}
	if updated, ok := e["updated"].(float64); !ok || updated <= 0 {
		t.Fatalf("updated=%v", e["updated"])
	}
}

func TestSceneTransformScaleScalesTextTypographyButNotLineHeight(t *testing.T) {
	body := `{"elements":[{"id":"t1","type":"text","angle":0,"x":10,"y":20,"width":100,"height":40,"baseline":16,"fontSize":20,"lineHeight":1.25,"text":"hello","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"customData":{"textRuns":[{"start":0,"end":5,"fontSize":18,"bold":true}]}}],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "transform", "d1", "t1", "--scale", "1.5")
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	for key, want := range map[string]float64{"width": 150, "height": 60, "fontSize": 30, "baseline": 24, "lineHeight": 1.25} {
		if e[key] != want {
			t.Errorf("%s=%v, want %v", key, e[key], want)
		}
	}
	run := e["customData"].(map[string]any)["textRuns"].([]any)[0].(map[string]any)
	if run["fontSize"] != float64(27) || run["bold"] != true {
		t.Errorf("text run=%v", run)
	}
}

func TestSceneSemanticDryRunReadsButDoesNotPatch(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("dry-run sent %s", r.Method)
		}
		io.WriteString(w, `{"elements":[{"id":"e1","type":"rectangle","version":1,"versionNonce":1,"index":"a0"}],"baseVersion":"BV"}`)
	})
	f := cap.f
	out, _, err := execRoot(t, f, "--dry-run", "docs", "scene", "element", "style", "d1", "e1", "--stroke-color", "#fff")
	if err != nil {
		t.Fatal(err)
	}
	if cap.requests != 1 {
		t.Fatalf("requests=%d", cap.requests)
	}
	if !json.Valid([]byte(out)) {
		t.Fatalf("output=%s", out)
	}
}

// dryRunData parses the success envelope produced by --dry-run and asserts the
// PATCH envelope shape (method, path, If-Match) that every semantic mutation must
// emit. It returns the single element in the body so callers can assert content.
func dryRunData(t *testing.T, out string) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope: %v\n%s", err, out)
	}
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("no data object: %s", out)
	}
	if data["dry_run"] != true {
		t.Fatalf("dry_run marker missing: %v", data)
	}
	if data["method"] != http.MethodPatch {
		t.Fatalf("method=%v", data["method"])
	}
	if data["path"] != "/v1/bot/docs/d1/scene" {
		t.Fatalf("path=%v", data["path"])
	}
	headers, _ := data["headers"].(map[string]any)
	if headers["If-Match"] != "BV" {
		t.Fatalf("If-Match=%v", headers)
	}
	return data
}

func dryRunElement(t *testing.T, data map[string]any) map[string]any {
	t.Helper()
	body, ok := data["body"].(map[string]any)
	if !ok {
		t.Fatalf("body not object: %v", data["body"])
	}
	els, ok := body["elements"].([]any)
	if !ok || len(els) != 1 {
		t.Fatalf("elements not a single-element array: %v", body["elements"])
	}
	e, ok := els[0].(map[string]any)
	if !ok {
		t.Fatalf("element not object: %v", els[0])
	}
	return e
}

// serveScene builds a GET-only handler that returns body and fails the test on
// any PATCH — dry-run must never write.
func serveScene(t *testing.T, body string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("dry-run must not send %s", r.Method)
		}
		io.WriteString(w, body)
	}
}

func TestSceneDryRunEmitsCompletePatchCreate(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"top","index":"a0"}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "create", "d1",
		"--type", "rectangle", "--id", "new", "--x", "40", "--y", "50", "--width", "240", "--height", "120")
	if err != nil {
		t.Fatal(err)
	}
	if cap.requests != 1 {
		t.Fatalf("requests=%d", cap.requests)
	}
	e := dryRunElement(t, dryRunData(t, out))
	// Complete Excalidraw base-field set must be present in the patch body.
	for _, field := range []string{"id", "type", "x", "y", "width", "height", "angle", "strokeColor",
		"backgroundColor", "fillStyle", "strokeWidth", "strokeStyle", "roughness", "opacity", "groupIds",
		"frameId", "index", "roundness", "seed", "version", "versionNonce", "isDeleted", "boundElements",
		"updated", "link", "locked"} {
		if _, ok := e[field]; !ok {
			t.Fatalf("create body missing base field %q: %v", field, e)
		}
	}
	if e["id"] != "new" || e["type"] != "rectangle" || e["x"] != float64(40) || e["y"] != float64(50) ||
		e["width"] != float64(240) || e["height"] != float64(120) || e["version"] != float64(1) ||
		e["isDeleted"] != false {
		t.Fatalf("create body content=%v", e)
	}
	idx, _ := e["index"].(string)
	if idx <= "a0" {
		t.Fatalf("create index %q not above existing max a0", idx)
	}
}

func TestSceneDryRunEmitsCompletePatchUpdate(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"e1","type":"rectangle","x":1,"y":2,"width":3,"height":4,"index":"a0","version":5,"versionNonce":9,"isDeleted":false,"locked":false,"futureField":"keep"}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "update", "d1", "e1",
		"--data", `{"customData":{"owner":"agent"},"locked":true}`)
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	cd, _ := e["customData"].(map[string]any)
	if cd["owner"] != "agent" || e["locked"] != true || e["version"] != float64(6) ||
		e["isDeleted"] != false || e["futureField"] != "keep" || e["id"] != "e1" {
		t.Fatalf("update body=%v", e)
	}
	if e["versionNonce"] == float64(9) {
		t.Fatalf("versionNonce not replaced: %v", e["versionNonce"])
	}
}

func TestSceneDryRunEmitsCompletePatchTransform(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"e1","type":"rectangle","x":1,"y":2,"width":10,"height":20,"angle":0,"index":"a0","version":5,"versionNonce":9,"isDeleted":false}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "transform", "d1", "e1",
		"--x", "50", "--width", "300", "--angle", "0.1")
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	if e["x"] != float64(50) || e["width"] != float64(300) || e["angle"] != float64(0.1) ||
		e["y"] != float64(2) || e["version"] != float64(6) {
		t.Fatalf("transform body=%v", e)
	}
}

func TestSceneDryRunEmitsCompletePatchStyle(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"e1","type":"rectangle","strokeColor":"#000","opacity":100,"index":"a0","version":5,"versionNonce":9,"isDeleted":false}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "style", "d1", "e1",
		"--stroke-color", "#1971c2", "--opacity", "90")
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	if e["strokeColor"] != "#1971c2" || e["opacity"] != float64(90) || e["version"] != float64(6) {
		t.Fatalf("style body=%v", e)
	}
}

func TestSceneDryRunEmitsCompletePatchLayer(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"e1","type":"rectangle","index":"a0","version":5,"versionNonce":9,"isDeleted":false},{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "layer", "d1", "e1", "--position", "front")
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	if idx, _ := e["index"].(string); idx <= "a1" {
		t.Fatalf("layer front index %q not above a1", idx)
	}
	if e["version"] != float64(6) || e["id"] != "e1" {
		t.Fatalf("layer body=%v", e)
	}
}

func TestSceneDryRunEmitsCompletePatchDelete(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"e1","type":"rectangle","index":"a0","version":5,"versionNonce":9,"isDeleted":false}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "delete", "d1", "e1")
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	if e["isDeleted"] != true || e["version"] != float64(6) || e["id"] != "e1" {
		t.Fatalf("delete body=%v", e)
	}
}

func TestSceneElementMissingFailsWithoutPatch(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"elements":[],"baseVersion":"BV"}`) })
	f := cap.f
	_, _, err := execRoot(t, f, "docs", "scene", "element", "delete", "d1", "missing")
	if err == nil || cap.requests != 1 {
		t.Fatalf("err=%v requests=%d", err, cap.requests)
	}
}

func TestSceneElementUpdateRejectsStructuralFields(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected %s", r.Method)
		}
		io.WriteString(w, `{"elements":[{"id":"e1","type":"rectangle","version":1,"versionNonce":1,"index":"a0"}],"baseVersion":"BV"}`)
	})
	for _, data := range []string{`{"index":"garbage"}`, `{"type":"image"}`, `{"isDeleted":true}`, `{"id":"other"}`, `{"version":2}`} {
		_, _, err := execRoot(t, cap.f, "docs", "scene", "element", "update", "d1", "e1", "--data", data)
		if err == nil {
			t.Fatalf("data %s: expected rejection", data)
		}
	}
	if cap.requests != 0 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

func TestSceneInputValidationHappensBeforeGet(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("unexpected request") })
	cases := [][]string{
		{"docs", "scene", "element", "create", "d", "--type", "bad"},
		{"docs", "scene", "element", "create", "d", "--type", "text", "--text", "x"},
		{"docs", "scene", "element", "update", "d", "e", "--data", "null"},
		{"docs", "scene", "element", "update", "d", "e", "--data", "{}"},
		{"docs", "scene", "element", "transform", "d", "e"},
		{"docs", "scene", "element", "transform", "d", "e", "--width", "-1"},
		{"docs", "scene", "element", "style", "d", "e"},
		{"docs", "scene", "element", "style", "d", "e", "--opacity", "101"},
		{"docs", "scene", "element", "layer", "d", "e", "--position", "sideways"},
		{"docs", "scene", "element", "layer", "d", "e", "--position", "before"},
	}
	for _, args := range cases {
		if _, _, err := execRoot(t, cap.f, args...); err == nil {
			t.Fatalf("expected error: %v", args)
		}
	}
	if cap.requests != 0 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

func TestSceneUpdateNoOpDoesNotPatch(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected %s", r.Method)
		}
		io.WriteString(w, `{"elements":[{"id":"e","type":"rectangle","version":1,"versionNonce":1,"index":"a0","locked":false}],"baseVersion":"BV"}`)
	})
	_, _, err := execRoot(t, cap.f, "docs", "scene", "element", "update", "d", "e", "--data", `{"locked":false}`)
	if err == nil || cap.requests != 1 {
		t.Fatalf("err=%v requests=%d", err, cap.requests)
	}
}

func TestSceneMutationRejectsInvalidExistingVersion(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected %s", r.Method)
		}
		io.WriteString(w, `{"elements":[{"id":"e1","type":"rectangle","version":1.5,"versionNonce":1,"index":"a0"}],"baseVersion":"BV"}`)
	})
	_, _, err := execRoot(t, cap.f, "docs", "scene", "element", "transform", "d1", "e1", "--x", "5")
	if err == nil || cap.requests != 1 {
		t.Fatalf("err=%v requests=%d", err, cap.requests)
	}
}

func TestPatchSceneDryRunReturnsMarshalError(t *testing.T) {
	f := newTestFactoryWithReg()
	f.Globals.DryRun = true
	err := patchScene(&cobra.Command{}, f.Factory, "d1", "BV", map[string]any{"elements": []any{math.NaN()}})
	if err == nil {
		t.Fatal("expected marshal error")
	}
}

// --- boundary tests for read-only review P2 fixes ---

func TestSceneMutationRejectsDeletedTargetWithoutPatch(t *testing.T) {
	body := `{"elements":[{"id":"e1","type":"rectangle","index":"a0","version":5,"versionNonce":9,"isDeleted":true}],"baseVersion":"BV"}`
	for _, args := range [][]string{
		{"docs", "scene", "element", "transform", "d1", "e1", "--x", "5"},
		{"docs", "scene", "element", "style", "d1", "e1", "--opacity", "50"},
		{"docs", "scene", "element", "update", "d1", "e1", "--data", `{"locked":true}`},
		{"docs", "scene", "element", "layer", "d1", "e1", "--position", "front"},
	} {
		_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Fatalf("must not PATCH deleted target: %s", r.Method)
			}
			io.WriteString(w, body)
		})
		if _, _, err := execRoot(t, cap.f, args...); err == nil {
			t.Fatalf("expected deleted-target rejection: %v", args)
		} else if cap.requests != 1 {
			t.Fatalf("requests=%d for %v", cap.requests, args)
		}
	}
}

func TestSceneRepeatedDeleteDoesNotPatch(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("re-delete must not PATCH: %s", r.Method)
		}
		io.WriteString(w, `{"elements":[{"id":"e1","type":"rectangle","index":"a0","version":5,"versionNonce":9,"isDeleted":true}],"baseVersion":"BV"}`)
	})
	_, _, err := execRoot(t, cap.f, "docs", "scene", "element", "delete", "d1", "e1")
	if err == nil || cap.requests != 1 {
		t.Fatalf("err=%v requests=%d", err, cap.requests)
	}
}

func TestSceneCreateRejectsTextFlagsOnNonTextBeforeGet(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("must reject before GET") })
	for _, args := range [][]string{
		{"docs", "scene", "element", "create", "d1", "--type", "rectangle", "--text", "hi"},
		{"docs", "scene", "element", "create", "d1", "--type", "ellipse", "--baseline", "10"},
		{"docs", "scene", "element", "create", "d1", "--type", "arrow", "--font-size", "30"},
	} {
		if _, _, err := execRoot(t, cap.f, args...); err == nil {
			t.Fatalf("expected type-flag rejection: %v", args)
		}
	}
	if cap.requests != 0 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

func TestSceneRejectsDuplicateLiveElementIDs(t *testing.T) {
	// Two live elements share an id; a mutation must refuse rather than silently
	// target the first match. A deleted duplicate of the same id is tolerated.
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("must not PATCH a corrupt scene: %s", r.Method)
		}
		io.WriteString(w, `{"elements":[{"id":"e1","type":"rectangle","index":"a0","version":5,"versionNonce":9,"isDeleted":false},{"id":"e1","type":"ellipse","index":"a1","version":2,"versionNonce":3,"isDeleted":false}],"baseVersion":"BV"}`)
	})
	_, _, err := execRoot(t, cap.f, "docs", "scene", "element", "style", "d1", "e1", "--opacity", "50")
	if err == nil || cap.requests != 1 {
		t.Fatalf("err=%v requests=%d", err, cap.requests)
	}
}

func TestSceneLayerRejectsRelativeToSelfBeforeGet(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("must reject before GET") })
	for _, pos := range []string{"before", "after"} {
		if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "layer", "d1", "e1", "--position", pos, "--relative-to", "e1"); err == nil {
			t.Fatalf("expected self-reference rejection for %s", pos)
		}
	}
	if cap.requests != 0 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

// TestSceneCreateWhitelistIsNarrowerThanFinalValidation pins the intentional
// fail-closed boundary: `create` only mints the six shapes it can fully
// construct, even though validateFinalElement (which guards elements read back
// from an existing scene) accepts a broader Excalidraw type set. Types outside
// the create whitelist are rejected locally, before any GET.
func TestSceneCreateWhitelistIsNarrowerThanFinalValidation(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("must reject before GET") })
	for _, kind := range []string{"freedraw", "image", "frame", "embeddable"} {
		if err := validateFinalElement(map[string]any{"id": "x", "type": kind, "index": "a0", "version": 1, "versionNonce": 1}); err != nil {
			t.Fatalf("validateFinalElement should accept read-back type %q: %v", kind, err)
		}
		if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "create", "d1", "--type", kind); err == nil {
			t.Fatalf("create should reject non-whitelisted type %q", kind)
		}
	}
	if cap.requests != 0 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

func TestSceneCreateUsesMaximumIndexNotResponseOrder(t *testing.T) {
	var patch map[string]any
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"elements":[{"id":"top","index":"a2"},{"id":"bottom","index":"a0"}],"baseVersion":"BV"}`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&patch)
		io.WriteString(w, `{}`)
	})
	_, _, err := execRoot(t, cap.f, "docs", "scene", "element", "create", "d1", "--id", "new")
	if err != nil {
		t.Fatal(err)
	}
	e := patch["elements"].([]any)[0].(map[string]any)
	if e["index"].(string) <= "a2" {
		t.Fatalf("patch=%v", patch)
	}
}
