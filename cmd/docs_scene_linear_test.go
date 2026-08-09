package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestSceneCreateMultiPointArrowInline(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "create", "d1", "--type", "arrow", "--id", "a1", "--points", `[[0,0],[40,20],[100,0]]`, "--arrow-type", "round")
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	if len(e["points"].([]any)) != 3 || e["elbowed"] != false || e["roundness"].(map[string]any)["type"] != float64(2) {
		t.Fatalf("element=%v", e)
	}
}

func TestSceneLinearPointsFileAndStdin(t *testing.T) {
	for name, tc := range map[string]struct{ spec, stdin string }{
		"file": {"@points.json", ""}, "stdin": {"@-", `[[0,0],[25,10],[50,0]]`},
	} {
		t.Run(name, func(t *testing.T) {
			spec, stdin := tc.spec, tc.stdin
			dir := t.TempDir()
			old, _ := os.Getwd()
			_ = os.Chdir(dir)
			defer os.Chdir(old)
			if name == "file" {
				if err := os.WriteFile(filepath.Join(dir, "points.json"), []byte(`[[0,0],[25,10],[50,0]]`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, cap := semanticFactory(t, serveScene(t, `{"elements":[],"baseVersion":"BV"}`))
			cap.f.In.WriteString(stdin)
			out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "create", "d1", "--type", "line", "--id", "l1", "--points", spec)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(dryRunElement(t, dryRunData(t, out))["points"].([]any)); got != 3 {
				t.Fatalf("points=%d", got)
			}
		})
	}
}

func TestSceneLinearElbowFixedSegmentsPreservesUnknown(t *testing.T) {
	body := `{"elements":[{"id":"a1","type":"arrow","index":"a0","version":2,"versionNonce":3,"isDeleted":false,"width":100,"height":50,"points":[[0,0],[100,50]],"elbowed":false,"roundness":null,"customFuture":{"keep":true}}],"baseVersion":"BV"}`
	var patched map[string]any
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, body)
			return
		}
		if r.Method != http.MethodPatch {
			t.Fatalf("method=%s", r.Method)
		}
		if r.Header.Get("If-Match") != "BV" {
			t.Fatalf("if-match=%q", r.Header.Get("If-Match"))
		}
		if err := json.NewDecoder(r.Body).Decode(&patched); err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, `{}`)
	})
	_, _, err := execRoot(t, cap.f, "docs", "scene", "element", "linear", "d1", "a1", "--points", `[[0,0],[50,0],[50,50],[100,50]]`, "--arrow-type", "elbow", "--fixed-segments", `[{"start":[50,0],"end":[50,50],"index":2}]`)
	if err != nil {
		t.Fatal(err)
	}
	if cap.requests != 2 {
		t.Fatalf("requests=%d", cap.requests)
	}
	e := patched["elements"].([]any)[0].(map[string]any)
	if e["elbowed"] != true || e["customFuture"].(map[string]any)["keep"] != true || len(e["fixedSegments"].([]any)) != 1 {
		t.Fatalf("element=%v", e)
	}
}

func TestSceneCreateElbowPointsFromStdinAndFixedSegmentsFile(t *testing.T) {
	dir := t.TempDir()
	segments := filepath.Join(dir, "segments.json")
	if err := os.WriteFile(segments, []byte(`[{"start":[20,0],"end":[20,20],"index":2}]`), 0600); err != nil {
		t.Fatal(err)
	}
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[],"baseVersion":"BV"}`))
	cap.f.In.WriteString(`[[0,0],[20,0],[20,20]]`)
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "create", "d1", "--type", "arrow", "--id", "a1", "--points", "@-", "--arrow-type", "elbow", "--fixed-segments", "@"+segments)
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	if e["elbowed"] != true || len(e["fixedSegments"].([]any)) != 1 {
		t.Fatalf("element=%v", e)
	}
}

func TestSceneLinearRejectsUnsafeOrWrongTypeBeforePatch(t *testing.T) {
	cases := [][]string{
		{"docs", "scene", "element", "create", "d", "--type", "rectangle", "--points", "[[0,0],[1,1]]"},
		{"docs", "scene", "element", "create", "d", "--type", "line", "--arrow-type", "elbow"},
		{"docs", "scene", "element", "create", "d", "--type", "arrow", "--points", "[[0,0],[1,1]]", "--arrow-type", "elbow"},
		{"docs", "scene", "element", "create", "d", "--type", "arrow", "--points", "[[0,0],[1,0]] trailing"},
		{"docs", "scene", "element", "create", "d", "--type", "arrow", "--points", "[[0,0],[1e100,0]]"},
		{"docs", "scene", "element", "create", "d", "--type", "arrow", "--points", "[[0,0],[10,0],[10,10]]", "--fixed-segments", "[]"},
	}
	for _, args := range cases {
		_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("unexpected request") })
		if _, _, err := execRoot(t, cap.f, args...); err == nil {
			t.Fatalf("expected rejection: %v", args)
		}
		if cap.requests != 0 {
			t.Fatalf("requests=%d", cap.requests)
		}
	}
}

func TestSceneLinearBindingsRemainBlocked(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("unexpected request") })
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "linear", "d", "a", "--start-binding", "shape"); err == nil {
		t.Fatal("expected unknown binding flag")
	}
	if cap.requests != 0 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

func TestSceneLinearNormalizesFirstPointAndCompanionCoordinates(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "create", "d1", "--type", "arrow", "--id", "a1", "--x", "100", "--y", "200", "--points", `[[10,20],[40,20],[40,60]]`, "--arrow-type", "elbow", "--fixed-segments", `[{"start":[10,20],"end":[40,20],"index":1}]`)
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	if e["x"] != float64(110) || e["y"] != float64(220) || !samePoint(e["points"].([]any)[0], []any{0, 0}) {
		t.Fatalf("element not normalized: %v", e)
	}
	segment := e["fixedSegments"].([]any)[0].(map[string]any)
	if !samePoint(segment["start"], []any{0, 0}) || !samePoint(segment["end"], []any{30, 0}) {
		t.Fatalf("fixed segment not translated: %v", segment)
	}
}

func TestSceneFixedSegmentsRejectGeometryMismatch(t *testing.T) {
	for _, segments := range []string{
		`[{"start":[0,0],"end":[5,0],"index":1}]`,
		`[{"start":[0,0],"end":[0,0],"index":1}]`,
		`[{"start":[0,0],"end":[10,10],"index":1}]`,
	} {
		_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("unexpected request") })
		_, _, err := execRoot(t, cap.f, "docs", "scene", "element", "create", "d", "--type", "arrow", "--points", `[[0,0],[10,0]]`, "--arrow-type", "elbow", "--fixed-segments", segments)
		if err == nil {
			t.Fatalf("expected rejection for %s", segments)
		}
		if cap.requests != 0 {
			t.Fatalf("requests=%d", cap.requests)
		}
	}
}
