package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ---- create: native shape, roundness, arrowheads (exact dry-run PATCH body) ----

func TestSceneCreateNativeShapeKind(t *testing.T) {
	kinds := []string{"square", "database", "notched-dovetail", "chevron", "parallelogram", "trapezoid", "speech-bubble", "speech-bubble-rounded", "triangle", "inverted-triangle", "circle", "right-triangle", "star", "hexagon", "pentagon", "octagon", "left-arrow", "right-arrow", "bidirectional-arrow"}
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"top","index":"a0"}],"baseVersion":"BV"}`))
			out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "create", "d1",
				"--type", "rectangle", "--id", "r", "--width", "100", "--height", "60", "--native-shape-kind", kind)
			if err != nil {
				t.Fatal(err)
			}
			e := dryRunElement(t, dryRunData(t, out))
			cd, ok := e["customData"].(map[string]any)
			if e["type"] != "rectangle" || e["roundness"] != nil || !ok || cd["nativeShapeKind"] != kind {
				t.Fatalf("element=%v", e)
			}
		})
	}
}

func TestSceneCreateNativeShapeRejectedOnNonRectangle(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("must reject before GET") })
	for _, kind := range []string{"ellipse", "diamond", "text", "arrow", "line"} {
		if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "create", "d1",
			"--type", kind, "--native-shape-kind", "triangle"); err == nil {
			t.Fatalf("expected native-shape-kind rejection for %s", kind)
		}
	}
	// Bad enum on a rectangle is also rejected locally.
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "create", "d1",
		"--type", "rectangle", "--native-shape-kind", "unknown-shape"); err == nil {
		t.Fatal("expected bad native-shape-kind enum rejection")
	}
	if cap.requests != 0 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

func TestSceneCreateAllWebShapePresets(t *testing.T) {
	tests := []struct {
		preset     string
		typeName   string
		customData any
		roundness  any
	}{
		{"rounded-rectangle", "rectangle", nil, map[string]any{"type": float64(3)}},
		{"ellipse", "ellipse", nil, nil},
		{"diamond", "diamond", nil, nil},
		{"square", "rectangle", map[string]any{"nativeShapeKind": "square"}, nil},
		{"circle", "rectangle", map[string]any{"nativeShapeKind": "circle"}, nil},
		{"triangle", "rectangle", map[string]any{"nativeShapeKind": "triangle"}, nil},
		{"parallelogram", "rectangle", map[string]any{"nativeShapeKind": "parallelogram"}, nil},
		{"database", "rectangle", map[string]any{"nativeShapeKind": "database"}, nil},
		{"notched-dovetail", "rectangle", map[string]any{"nativeShapeKind": "notched-dovetail"}, nil},
		{"chevron", "rectangle", map[string]any{"nativeShapeKind": "chevron"}, nil},
		{"trapezoid", "rectangle", map[string]any{"nativeShapeKind": "trapezoid"}, nil},
		{"speech-bubble", "rectangle", map[string]any{"nativeShapeKind": "speech-bubble"}, nil},
		{"speech-bubble-rounded", "rectangle", map[string]any{"nativeShapeKind": "speech-bubble-rounded"}, nil},
		{"right-triangle", "rectangle", map[string]any{"nativeShapeKind": "right-triangle"}, nil},
		{"star", "rectangle", map[string]any{"nativeShapeKind": "star"}, nil},
		{"hexagon", "rectangle", map[string]any{"nativeShapeKind": "hexagon"}, nil},
		{"pentagon", "rectangle", map[string]any{"nativeShapeKind": "pentagon"}, nil},
		{"octagon", "rectangle", map[string]any{"nativeShapeKind": "octagon"}, nil},
		{"left-arrow", "rectangle", map[string]any{"nativeShapeKind": "left-arrow"}, nil},
		{"right-arrow", "rectangle", map[string]any{"nativeShapeKind": "right-arrow"}, nil},
		{"bidirectional-arrow", "rectangle", map[string]any{"nativeShapeKind": "bidirectional-arrow"}, nil},
	}
	if len(tests) != 21 {
		t.Fatalf("shape preset contract has %d entries, want 21", len(tests))
	}
	for _, tt := range tests {
		t.Run(tt.preset, func(t *testing.T) {
			_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"top","index":"a0"}],"baseVersion":"BV"}`))
			out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "create", "d1", "--preset", tt.preset, "--id", "x")
			if err != nil {
				t.Fatal(err)
			}
			e := dryRunElement(t, dryRunData(t, out))
			for field, want := range map[string]any{
				"type": tt.typeName, "customData": tt.customData, "roundness": tt.roundness,
				"arrowType": nil, "points": nil,
			} {
				if !reflect.DeepEqual(e[field], want) {
					t.Errorf("%s=%#v want %#v; element=%v", field, e[field], want, e)
				}
			}
		})
	}
}

func TestSceneCreateAllWebLinePresets(t *testing.T) {
	tests := []struct {
		preset   string
		typeName string
		round    any
		elbowed  bool
		points   any
	}{
		{"curved-arrow", "arrow", map[string]any{"type": float64(2)}, false, []any{[]any{float64(0), float64(0)}, []any{float64(30), float64(70)}, []any{float64(100), float64(100)}}},
		{"elbow-arrow", "arrow", nil, true, []any{[]any{float64(0), float64(0)}, []any{float64(50), float64(0)}, []any{float64(50), float64(100)}, []any{float64(100), float64(100)}}},
		{"straight-arrow", "arrow", nil, false, []any{[]any{float64(0), float64(0)}, []any{float64(100), float64(100)}}},
		{"straight-line", "line", nil, false, []any{[]any{float64(0), float64(0)}, []any{float64(100), float64(100)}}},
	}
	for _, tt := range tests {
		t.Run(tt.preset, func(t *testing.T) {
			_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"top","index":"a0"}],"baseVersion":"BV"}`))
			args := []string{"--dry-run", "docs", "scene", "element", "create", "d1", "--preset", tt.preset, "--id", "x"}
			out, _, err := execRoot(t, cap.f, args...)
			if err != nil {
				t.Fatal(err)
			}
			e := dryRunElement(t, dryRunData(t, out))
			for field, want := range map[string]any{
				"type": tt.typeName, "customData": nil, "roundness": tt.round,
				"arrowType": nil, "points": tt.points, "elbowed": tt.elbowed,
			} {
				if !reflect.DeepEqual(e[field], want) {
					t.Errorf("%s=%#v want %#v; element=%v", field, e[field], want, e)
				}
			}
		})
	}
}

func TestSceneFriendlyFontFamilyMatchesWebCatalogue(t *testing.T) {
	cases := map[string]float64{
		"arial":       2001,
		"2005":        2005,
		"宋体":          2005,
		"pingfang-sc": 2013,
		"courier-new": 2022,
		"calibri":     2026,
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"top","index":"a0"}],"baseVersion":"BV"}`))
			out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "create", "d1", "--type", "text", "--text", "中文字体", "--width", "160", "--height", "40", "--baseline", "20", "--font-family", input)
			if err != nil {
				t.Fatal(err)
			}
			if got := dryRunElement(t, dryRunData(t, out))["fontFamily"]; got != want {
				t.Fatalf("fontFamily=%v want %v", got, want)
			}
		})
	}
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("must reject before GET") })
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "create", "d1", "--type", "text", "--text", "x", "--width", "80", "--height", "40", "--baseline", "20", "--font-family", "2"); err == nil {
		t.Fatal("legacy numeric font id accepted for a new friendly write")
	}
}

func TestSceneLinePresetExplicitPointsRemainAuthoritative(t *testing.T) {
	for _, preset := range []string{"curved-arrow", "elbow-arrow"} {
		_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"top","index":"a0"}],"baseVersion":"BV"}`))
		points := `[[0,0],[40,0],[40,60]]`
		if preset == "curved-arrow" {
			points = `[[0,0],[30,80],[100,40]]`
		}
		out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "create", "d1", "--preset", preset, "--points", points)
		if err != nil {
			t.Fatal(err)
		}
		got := dryRunElement(t, dryRunData(t, out))["points"]
		var want any
		if err := json.Unmarshal([]byte(points), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s points=%v want %v", preset, got, want)
		}
	}
}

func TestSceneDatabaseRimRatioCreateAndUpdate(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"top","index":"a0"}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "create", "d1", "--preset", "database", "--database-rim-ratio", "0.35")
	if err != nil {
		t.Fatal(err)
	}
	cd := dryRunElement(t, dryRunData(t, out))["customData"].(map[string]any)
	if cd["nativeShapeKind"] != "database" || cd["databaseRimRatio"] != float64(0.35) {
		t.Fatalf("customData=%v", cd)
	}

	_, cap = semanticFactory(t, serveScene(t, `{"elements":[{"id":"db","type":"rectangle","customData":{"nativeShapeKind":"database","keep":true},"index":"a0","version":1,"versionNonce":1}],"baseVersion":"BV"}`))
	out, _, err = execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "update", "d1", "db", "--database-rim-ratio", "0.06")
	if err != nil {
		t.Fatal(err)
	}
	cd = dryRunElement(t, dryRunData(t, out))["customData"].(map[string]any)
	if cd["nativeShapeKind"] != "database" || cd["keep"] != true || cd["databaseRimRatio"] != float64(0.06) {
		t.Fatalf("customData=%v", cd)
	}

	for _, ratio := range []string{"0.059", "0.401", "NaN"} {
		_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("must reject before GET") })
		if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "create", "d1", "--preset", "database", "--database-rim-ratio", ratio); err == nil {
			t.Fatalf("ratio %s accepted", ratio)
		}
	}

	// A valid ratio is still illegal on every non-database create/update path.
	_, cap = semanticFactory(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("must reject before GET") })
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "create", "d1", "--preset", "triangle", "--database-rim-ratio", "0.2"); err == nil {
		t.Fatal("database rim ratio accepted for a triangle")
	}
	_, cap = semanticFactory(t, serveScene(t, `{"elements":[{"id":"shape","type":"rectangle","customData":{"nativeShapeKind":"triangle"},"index":"a0","version":1,"versionNonce":1}],"baseVersion":"BV"}`))
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "update", "d1", "shape", "--database-rim-ratio", "0.2"); err == nil {
		t.Fatal("database rim ratio update accepted for a triangle")
	}
}

func TestSceneCreateRoundnessMapping(t *testing.T) {
	cases := map[string]float64{"rectangle": 3, "diamond": 3, "line": 2, "arrow": 2}
	for kind, wantType := range cases {
		_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"top","index":"a0"}],"baseVersion":"BV"}`))
		args := []string{"--dry-run", "docs", "scene", "element", "create", "d1", "--type", kind, "--id", "x", "--roundness", "round"}
		if kind == "arrow" || kind == "line" {
			args = append(args, "--width", "100", "--height", "0")
		}
		out, _, err := execRoot(t, cap.f, args...)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		e := dryRunElement(t, dryRunData(t, out))
		r, ok := e["roundness"].(map[string]any)
		if !ok || r["type"] != wantType {
			t.Fatalf("%s roundness=%v want type %v", kind, e["roundness"], wantType)
		}
	}
}

func TestSceneCreateRoundnessRejectedOnEllipse(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("must reject before GET") })
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "create", "d1",
		"--type", "ellipse", "--roundness", "round"); err == nil {
		t.Fatal("expected roundness rejection for ellipse")
	}
	if cap.requests != 0 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

func TestSceneCreateArrowheads(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"top","index":"a0"}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "create", "d1",
		"--type", "arrow", "--id", "a", "--width", "120", "--height", "0",
		"--start-arrowhead", "none", "--end-arrowhead", "triangle")
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	if e["startArrowhead"] != nil || e["endArrowhead"] != "triangle" {
		t.Fatalf("arrowheads start=%v end=%v", e["startArrowhead"], e["endArrowhead"])
	}
}

func TestSceneCreateArrowheadRejectedOnNonLinear(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("must reject before GET") })
	for _, kind := range []string{"rectangle", "ellipse", "diamond"} {
		if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "create", "d1",
			"--type", kind, "--end-arrowhead", "arrow"); err == nil {
			t.Fatalf("expected arrowhead rejection for %s", kind)
		}
	}
	if cap.requests != 0 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

func TestSceneCreateTextTypography(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"top","index":"a0"}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "create", "d1",
		"--type", "text", "--text", "hi", "--width", "40", "--height", "25", "--baseline", "20",
		"--font-family", "courier-new", "--text-align", "center", "--vertical-align", "middle", "--line-height", "1.5",
		"--bold", "true", "--italic", "false")
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	if e["fontFamily"] != float64(2022) || e["textAlign"] != "center" || e["verticalAlign"] != "middle" || e["lineHeight"] != 1.5 {
		t.Fatalf("typography=%v", e)
	}
	cd, _ := e["customData"].(map[string]any)
	if cd["bold"] != true || cd["italic"] != false {
		t.Fatalf("customData=%v", e["customData"])
	}
}

func TestSceneCreateTextTypographyRejectedOnNonText(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("must reject before GET") })
	for _, flag := range [][]string{{"--font-family", "times-new-roman"}, {"--text-align", "center"}, {"--vertical-align", "middle"}, {"--line-height", "1.5"}, {"--bold", "true"}} {
		args := append([]string{"docs", "scene", "element", "create", "d1", "--type", "rectangle"}, flag...)
		if _, _, err := execRoot(t, cap.f, args...); err == nil {
			t.Fatalf("expected rejection for %v on rectangle", flag)
		}
	}
	if cap.requests != 0 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

func TestSceneCreateTextAlignRejectsJustify(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("must reject before GET") })
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "create", "d1",
		"--type", "text", "--text", "x", "--width", "40", "--height", "25", "--baseline", "20",
		"--text-align", "justify"); err == nil {
		t.Fatal("expected justify rejection")
	}
}

// ---- style: stroke-style, deep-merge customData, explicit false, type gating ----

func TestSceneStyleStrokeStyleAndRoundness(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"e1","type":"rectangle","index":"a0","version":5,"versionNonce":9,"isDeleted":false,"roundness":null}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "style", "d1", "e1",
		"--stroke-style", "dashed", "--roundness", "round")
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	if e["strokeStyle"] != "dashed" {
		t.Fatalf("strokeStyle=%v", e["strokeStyle"])
	}
	r, ok := e["roundness"].(map[string]any)
	if !ok || r["type"] != float64(3) {
		t.Fatalf("roundness=%v", e["roundness"])
	}
}

func TestSceneStyleRoundnessSharpNullifies(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"e1","type":"rectangle","index":"a0","version":5,"versionNonce":9,"isDeleted":false,"roundness":{"type":3}}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "style", "d1", "e1", "--roundness", "sharp")
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	if v, ok := e["roundness"]; !ok || v != nil {
		t.Fatalf("roundness not nulled: %v (present=%v)", v, ok)
	}
}

func TestSceneStyleDeepMergesCustomDataPreservingKeys(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"e1","type":"text","text":"hi","index":"a0","version":5,"versionNonce":9,"isDeleted":false,"fontSize":20,"customData":{"owner":"agent","nested":{"keep":1}}}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "style", "d1", "e1", "--bold", "true")
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	cd, _ := e["customData"].(map[string]any)
	if cd["owner"] != "agent" || cd["bold"] != true {
		t.Fatalf("customData=%v", e["customData"])
	}
	nested, _ := cd["nested"].(map[string]any)
	if nested["keep"] != float64(1) {
		t.Fatalf("nested customData not preserved: %v", cd["nested"])
	}
}

func TestSceneStyleExplicitFalseIsRepresentable(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"e1","type":"text","text":"hi","index":"a0","version":5,"versionNonce":9,"isDeleted":false,"fontSize":20,"customData":{"bold":true}}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "style", "d1", "e1", "--bold", "false")
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	cd, _ := e["customData"].(map[string]any)
	if v, ok := cd["bold"]; !ok || v != false {
		t.Fatalf("explicit false not stored: %v", cd)
	}
}

func TestSceneStyleTriStateRejectsGarbage(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("must reject before GET") })
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "style", "d1", "e1", "--bold", "yes"); err == nil {
		t.Fatal("expected tri-state rejection")
	}
	if cap.requests != 0 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

func TestSceneStyleTypographyGatedToText(t *testing.T) {
	body := `{"elements":[{"id":"e1","type":"rectangle","index":"a0","version":5,"versionNonce":9,"isDeleted":false}],"baseVersion":"BV"}`
	for _, args := range [][]string{
		{"docs", "scene", "element", "style", "d1", "e1", "--font-family", "times-new-roman"},
		{"docs", "scene", "element", "style", "d1", "e1", "--bold", "true"},
	} {
		_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Fatalf("must not PATCH: %s", r.Method)
			}
			io.WriteString(w, body)
		})
		if _, _, err := execRoot(t, cap.f, args...); err == nil {
			t.Fatalf("expected text-only rejection: %v", args)
		} else if cap.requests != 1 {
			t.Fatalf("requests=%d for %v", cap.requests, args)
		}
	}
}

func TestSceneStyleArrowheadGatedToLinear(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("must not PATCH: %s", r.Method)
		}
		io.WriteString(w, `{"elements":[{"id":"e1","type":"rectangle","index":"a0","version":5,"versionNonce":9,"isDeleted":false}],"baseVersion":"BV"}`)
	})
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "style", "d1", "e1", "--end-arrowhead", "dot"); err == nil {
		t.Fatal("expected arrowhead rejection on rectangle")
	}
	if cap.requests != 1 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

func TestSceneStyleFillStylePreserved(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"e1","type":"rectangle","index":"a0","version":5,"versionNonce":9,"isDeleted":false,"fillStyle":"solid"}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "style", "d1", "e1", "--fill-style", "hachure")
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	if e["fillStyle"] != "hachure" {
		t.Fatalf("fillStyle=%v", e["fillStyle"])
	}
}

// ---- element text: content, typography, textRuns ----

// customDataRuns extracts customData.textRuns and asserts the element never
// carries a top-level textRuns field — schema v3 stores rich-text runs under
// customData (packages/whiteboard-schema/src/customData.ts) and the canonical
// PATCH must never emit a top-level textRuns.
func customDataRuns(t *testing.T, e map[string]any) []any {
	t.Helper()
	if _, ok := e["textRuns"]; ok {
		t.Fatalf("element must not carry top-level textRuns: %v", e)
	}
	cd, ok := e["customData"].(map[string]any)
	if !ok {
		t.Fatalf("customData missing or not an object: %v", e["customData"])
	}
	runs, ok := cd["textRuns"].([]any)
	if !ok {
		t.Fatalf("customData.textRuns not an array: %v", cd["textRuns"])
	}
	return runs
}

func TestSceneTextUpdatesContentAndTypography(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"e1","type":"text","text":"old","originalText":"old","index":"a0","version":5,"versionNonce":9,"isDeleted":false,"fontSize":20,"fontFamily":1,"textAlign":"left","verticalAlign":"top","lineHeight":1.25,"customData":{"owner":"agent"}}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "text", "d1", "e1",
		"--text", "new", "--font-family", "simsun", "--text-align", "right")
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	if e["text"] != "new" || e["originalText"] != "new" || e["fontFamily"] != float64(2005) || e["textAlign"] != "right" {
		t.Fatalf("text update=%v", e)
	}
	if cd, _ := e["customData"].(map[string]any); cd["owner"] != "agent" {
		t.Fatalf("customData not preserved: %v", e["customData"])
	}
}

func TestSceneTextRejectedOnNonTextTarget(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("must not PATCH: %s", r.Method)
		}
		io.WriteString(w, `{"elements":[{"id":"e1","type":"rectangle","index":"a0","version":5,"versionNonce":9,"isDeleted":false}],"baseVersion":"BV"}`)
	})
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "text", "d1", "e1", "--runs", `[{"start":0,"end":1}]`); err == nil {
		t.Fatal("expected textRuns rejection on rectangle")
	}
	if cap.requests != 1 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

func TestSceneTextRunsEmojiUTF16Clamp(t *testing.T) {
	// "a😀b" is 4 UTF-16 units: a=1, 😀=2 (surrogate pair), b=1.
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"e1","type":"text","text":"a😀b","index":"a0","version":5,"versionNonce":9,"isDeleted":false,"fontSize":20}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "text", "d1", "e1",
		"--runs", `[{"start":1,"end":100,"bold":true}]`)
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	runs := customDataRuns(t, e)
	if len(runs) != 1 {
		t.Fatalf("textRuns=%v", runs)
	}
	run := runs[0].(map[string]any)
	if run["start"] != float64(1) || run["end"] != float64(4) || run["bold"] != true {
		t.Fatalf("run=%v", run)
	}
}

func TestSceneTextRunsClampAgainstNewContent(t *testing.T) {
	// --text shortens content; runs must clamp to the NEW length, not the old.
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"e1","type":"text","text":"hello world","index":"a0","version":5,"versionNonce":9,"isDeleted":false,"fontSize":20}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "text", "d1", "e1",
		"--text", "hi", "--runs", `[{"start":0,"end":11,"italic":true}]`)
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	run := customDataRuns(t, e)[0].(map[string]any)
	if run["end"] != float64(2) {
		t.Fatalf("run clamped to old length: %v", run)
	}
}

func TestSceneTextRunsFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runs.json")
	if err := os.WriteFile(path, []byte(`[{"start":0,"end":3,"color":"#ff0000"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"e1","type":"text","text":"abcdef","index":"a0","version":5,"versionNonce":9,"isDeleted":false,"fontSize":20}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "text", "d1", "e1", "--runs", "@"+path)
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	run := customDataRuns(t, e)[0].(map[string]any)
	if run["color"] != "#ff0000" || run["end"] != float64(3) {
		t.Fatalf("run=%v", run)
	}
}

func TestSceneTextRunsClearWithEmptyArray(t *testing.T) {
	// customData holds only textRuns → clearing empties customData, which is
	// dropped entirely (matching the backend's normalizeCustomData).
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"e1","type":"text","text":"abc","index":"a0","version":5,"versionNonce":9,"isDeleted":false,"fontSize":20,"customData":{"textRuns":[{"start":0,"end":3,"bold":true}]}}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "text", "d1", "e1", "--runs", `[]`)
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	if _, ok := e["textRuns"]; ok {
		t.Fatalf("must not emit top-level textRuns: %v", e)
	}
	if cd, ok := e["customData"]; ok {
		t.Fatalf("emptied customData must be dropped entirely, got %v", cd)
	}
}

func TestSceneTextRunsClearPreservesOtherCustomData(t *testing.T) {
	// Clearing textRuns removes ONLY customData.textRuns; unrelated keys survive.
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"e1","type":"text","text":"abc","index":"a0","version":5,"versionNonce":9,"isDeleted":false,"fontSize":20,"customData":{"owner":"agent","textRuns":[{"start":0,"end":3,"italic":true}]}}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "text", "d1", "e1", "--runs", `[]`)
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	if _, ok := e["textRuns"]; ok {
		t.Fatalf("must not emit top-level textRuns: %v", e)
	}
	cd, ok := e["customData"].(map[string]any)
	if !ok || cd["owner"] != "agent" {
		t.Fatalf("unrelated customData not preserved: %v", e["customData"])
	}
	if _, ok := cd["textRuns"]; ok {
		t.Fatalf("customData.textRuns not cleared: %v", cd)
	}
}

// TestSceneTextRunsWrittenToCustomData pins the schema-v3 location: normalized
// runs land in customData.textRuns (never top-level) and coexist with unrelated
// customData keys.
func TestSceneTextRunsWrittenToCustomData(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"e1","type":"text","text":"hello","index":"a0","version":5,"versionNonce":9,"isDeleted":false,"fontSize":20,"customData":{"owner":"agent"}}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "text", "d1", "e1",
		"--runs", `[{"start":0,"end":5,"bold":true}]`)
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	runs := customDataRuns(t, e)
	if len(runs) != 1 {
		t.Fatalf("textRuns=%v", runs)
	}
	if cd := e["customData"].(map[string]any); cd["owner"] != "agent" {
		t.Fatalf("unrelated customData not preserved: %v", cd)
	}
}

// TestSceneTextRejectsLegacyTopLevelTextRuns pins the fail-loud guard: an element
// read back with a legacy top-level textRuns field must not be re-emitted as a
// malformed dual-location state; the mutation fails after the GET, without a PATCH.
func TestSceneTextRejectsLegacyTopLevelTextRuns(t *testing.T) {
	body := `{"elements":[{"id":"e1","type":"text","text":"abc","index":"a0","version":5,"versionNonce":9,"isDeleted":false,"fontSize":20,"textRuns":[{"start":0,"end":3}]}],"baseVersion":"BV"}`
	for _, args := range [][]string{
		{"docs", "scene", "element", "text", "d1", "e1", "--text", "abcd"},
		{"docs", "scene", "element", "text", "d1", "e1", "--runs", `[{"start":0,"end":3,"bold":true}]`},
		{"docs", "scene", "element", "style", "d1", "e1", "--opacity", "50"},
	} {
		_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Fatalf("must not PATCH an element with legacy top-level textRuns: %s", r.Method)
			}
			io.WriteString(w, body)
		})
		if _, _, err := execRoot(t, cap.f, args...); err == nil {
			t.Fatalf("expected legacy top-level textRuns rejection: %v", args)
		} else if cap.requests != 1 {
			t.Fatalf("requests=%d for %v", cap.requests, args)
		}
	}
}

func TestSceneTextRunsAcceptsFunctionalColor(t *testing.T) {
	// A functional rgb() color is valid per the shared schema and must round-trip.
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"e1","type":"text","text":"abc","index":"a0","version":5,"versionNonce":9,"isDeleted":false,"fontSize":20}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "text", "d1", "e1",
		"--runs", `[{"start":0,"end":3,"color":"rgb(255, 0, 0)"}]`)
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	run := customDataRuns(t, e)[0].(map[string]any)
	if run["color"] != "rgb(255, 0, 0)" {
		t.Fatalf("functional color not preserved: %v", run)
	}
}

func TestSceneTextRunsRejectInvalidContracts(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("must reject before GET") })
	for _, runs := range []string{
		`null`,
		`{}`,
		`[{"start":-1,"end":2}]`,
		`[{"start":2,"end":2}]`,
		`[{"start":3,"end":1}]`,
		`[{"start":0,"end":1,"fontFamily":0}]`,
		`[{"start":0,"end":1,"fontSize":0}]`,
		`[{"start":0,"end":1,"fontSize":-3}]`,
		`[{"start":0,"end":1,"color":"#zzzzzz"}]`,
		`[{"start":0,"end":1,"color":"not a color"}]`,
		`[{"start":0,"end":1,"bold":"true"}]`,
		`[{"start":0,"end":1,"unknown":1}]`,
	} {
		if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "text", "d1", "e1", "--runs", runs); err == nil {
			t.Fatalf("expected rejection for runs %s", runs)
		}
	}
	if cap.requests != 0 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

func TestSceneTextNoOpDoesNotPatch(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("no-op must not PATCH: %s", r.Method)
		}
		io.WriteString(w, `{"elements":[{"id":"e1","type":"text","text":"hi","index":"a0","version":5,"versionNonce":9,"isDeleted":false,"fontSize":20,"textAlign":"left"}],"baseVersion":"BV"}`)
	})
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "text", "d1", "e1", "--text-align", "left"); err == nil || cap.requests != 1 {
		t.Fatalf("err=%v requests=%d", err, cap.requests)
	}
}

func TestSceneTextRejectsDeletedTarget(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("must not PATCH deleted target: %s", r.Method)
		}
		io.WriteString(w, `{"elements":[{"id":"e1","type":"text","text":"hi","index":"a0","version":5,"versionNonce":9,"isDeleted":true,"fontSize":20}],"baseVersion":"BV"}`)
	})
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "text", "d1", "e1", "--text", "x"); err == nil || cap.requests != 1 {
		t.Fatalf("err=%v requests=%d", err, cap.requests)
	}
}

// ---- normalizeTextRuns unit tests: clamp/sort/overlap/merge ----

func run(start, end int, kv ...any) map[string]any {
	m := map[string]any{"start": start, "end": end}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func TestNormalizeTextRunsSortAndClamp(t *testing.T) {
	// Distinct styles so the two adjacent runs are not merged; this isolates the
	// sort-by-start behavior (input is given out of order).
	out, err := normalizeTextRuns([]any{run(3, 5, "italic", true), run(0, 3, "bold", true)}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("out=%v", out)
	}
	a, b := out[0].(map[string]any), out[1].(map[string]any)
	if a["start"] != 0 || a["end"] != 3 || b["start"] != 3 || b["end"] != 5 {
		t.Fatalf("not sorted: %v", out)
	}
}

func TestNormalizeTextRunsLaterWinsOverlap(t *testing.T) {
	// Partial overlap: the second run composes onto the first over [2,6) rather
	// than replacing it, so the overlap carries BOTH properties and the boundary
	// sweep yields three spans — [0,2) bold, [2,6) bold+italic, [6,8) italic.
	out, err := normalizeTextRuns([]any{run(0, 6, "bold", true), run(2, 8, "italic", true)}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("out=%v", out)
	}
	a := out[0].(map[string]any)
	if a["start"] != 0 || a["end"] != 2 || a["bold"] != true || a["italic"] != nil {
		t.Fatalf("a=%v", a)
	}
	b := out[1].(map[string]any)
	if b["start"] != 2 || b["end"] != 6 || b["bold"] != true || b["italic"] != true {
		t.Fatalf("b=%v", b)
	}
	c := out[2].(map[string]any)
	if c["start"] != 6 || c["end"] != 8 || c["italic"] != true || c["bold"] != nil {
		t.Fatalf("c=%v", c)
	}
}

// TestNormalizeTextRunsPartialOverlapComposition covers the canonical property-by-
// property composition: two runs sharing no properties overlap, and the shared
// span carries the union of both, never a wholesale replacement of one by the other.
func TestNormalizeTextRunsPartialOverlapComposition(t *testing.T) {
	out, err := normalizeTextRuns([]any{run(0, 4, "bold", true), run(2, 6, "italic", true)}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("out=%v", out)
	}
	mid := out[1].(map[string]any)
	if mid["start"] != 2 || mid["end"] != 4 || mid["bold"] != true || mid["italic"] != true {
		t.Fatalf("overlap span did not compose both properties: %v", mid)
	}
}

// TestNormalizeTextRunsLaterOverridesOneProperty pins that a later run overriding a
// single property leaves the earlier run's OTHER properties intact on the overlap.
func TestNormalizeTextRunsLaterOverridesOneProperty(t *testing.T) {
	// First run sets bold+italic across [0,4); second flips bold to false on [1,3).
	out, err := normalizeTextRuns([]any{run(0, 4, "bold", true, "italic", true), run(1, 3, "bold", false)}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("out=%v", out)
	}
	mid := out[1].(map[string]any)
	if mid["start"] != 1 || mid["end"] != 3 || mid["bold"] != false || mid["italic"] != true {
		t.Fatalf("override did not preserve the other property: %v", mid)
	}
	// The flanking spans keep the original bold=true, italic=true.
	for _, i := range []int{0, 2} {
		s := out[i].(map[string]any)
		if s["bold"] != true || s["italic"] != true {
			t.Fatalf("flank span %d altered: %v", i, s)
		}
	}
}

// TestNormalizeTextRunsDropsStyleless pins that a run carrying no style property is
// dropped (never persisted), while a styled run in the same input survives.
func TestNormalizeTextRunsDropsStyleless(t *testing.T) {
	out, err := normalizeTextRuns([]any{run(0, 3, "bold", true), run(3, 6)}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("styleless run not dropped: %v", out)
	}
	m := out[0].(map[string]any)
	if m["start"] != 0 || m["end"] != 3 || m["bold"] != true {
		t.Fatalf("surviving run=%v", m)
	}
}

// TestNormalizeTextRunsExplicitFalse pins that an explicit false is MEANINGFUL and
// preserved — it is a real style value, not a default to be dropped.
func TestNormalizeTextRunsExplicitFalse(t *testing.T) {
	out, err := normalizeTextRuns([]any{run(0, 3, "bold", false)}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("explicit false dropped: %v", out)
	}
	m := out[0].(map[string]any)
	if v, ok := m["bold"]; !ok || v != false {
		t.Fatalf("explicit bold=false not preserved: %v", m)
	}
}

// TestNormalizeTextRunsNonIntegerFontFamily pins that a canonical positive
// non-integer fontFamily survives normalization as its numeric value.
func TestNormalizeTextRunsNonIntegerFontFamily(t *testing.T) {
	out, err := normalizeTextRuns([]any{run(0, 3, "fontFamily", 1.5)}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("out=%v", out)
	}
	m := out[0].(map[string]any)
	if m["fontFamily"] != float64(1.5) {
		t.Fatalf("non-integer fontFamily not preserved: %v", m)
	}
}

// TestNormalizeTextRunsIdempotent pins that feeding the normalized output back
// through normalizeTextRuns is a no-op.
func TestNormalizeTextRunsIdempotent(t *testing.T) {
	first, err := normalizeTextRuns([]any{run(0, 6, "bold", true), run(2, 8, "italic", true), run(1, 3, "bold", false)}, 10)
	if err != nil {
		t.Fatal(err)
	}
	second, err := normalizeTextRuns(first, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("not idempotent:\n first=%v\nsecond=%v", first, second)
	}
}

func TestNormalizeTextRunsAdjacentMerge(t *testing.T) {
	out, err := normalizeTextRuns([]any{run(0, 3, "bold", true), run(3, 6, "bold", true)}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("adjacent identical runs not merged: %v", out)
	}
	m := out[0].(map[string]any)
	if m["start"] != 0 || m["end"] != 6 || m["bold"] != true {
		t.Fatalf("merged=%v", m)
	}
}

func TestNormalizeTextRunsAdjacentDifferentStyleNotMerged(t *testing.T) {
	out, err := normalizeTextRuns([]any{run(0, 3, "bold", true), run(3, 6, "italic", true)}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("distinct styles wrongly merged: %v", out)
	}
}

func TestNormalizeTextRunsEmptyClears(t *testing.T) {
	out, err := normalizeTextRuns(nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("out=%v", out)
	}
}

func TestNormalizeTextRunsAllOutOfRangeErrors(t *testing.T) {
	if _, err := normalizeTextRuns([]any{run(20, 30)}, 5); err == nil {
		t.Fatal("expected out-of-range error")
	}
}

func TestUTF16Len(t *testing.T) {
	cases := map[string]int{"": 0, "abc": 3, "a😀b": 4, "😀😀": 4, "café": 4}
	for s, want := range cases {
		if got := utf16Len(s); got != want {
			t.Fatalf("utf16Len(%q)=%d want %d", s, got, want)
		}
	}
}

// TestSceneTextRunsRoundTripJSON pins that normalized runs marshal to the exact
// integer offsets and typed style attributes the backend contract expects.
func TestSceneTextRunsRoundTripJSON(t *testing.T) {
	out, err := normalizeTextRuns([]any{run(0, 2, "fontFamily", 2, "fontSize", 24.0, "color", "#123456", "underline", false)}, 5)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	var back []map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"start": float64(0), "end": float64(2), "fontFamily": float64(2), "fontSize": float64(24), "color": "#123456", "underline": false}
	if !reflect.DeepEqual(back[0], want) {
		t.Fatalf("round-trip=%v want %v", back[0], want)
	}
}

// TestValidColorMatchesSchemaContract pins validColor to isValidTextColor in
// packages/whiteboard-schema/src/customData.ts: hex (3/4/6/8), functional
// rgb/rgba/hsl/hsla, and bare keywords (incl. "transparent"), bounded to 32 chars.
func TestValidColorMatchesSchemaContract(t *testing.T) {
	for _, c := range []string{"#fff", "#ffff", "#1e1e1e", "#3F83F8ff", "transparent", "red", "rgb(1,2,3)", "rgba(1, 2, 3, 0.5)", "hsl(120, 50%, 50%)"} {
		if !validColor(c) {
			t.Errorf("expected %q to be a valid color", c)
		}
	}
	for _, c := range []string{"", "#zz", "#12345", "not a color", "#1e1e1e1e1e", "1e1e1e", "rgb()", strings.Repeat("a", 33)} {
		if validColor(c) {
			t.Errorf("expected %q to be rejected", c)
		}
	}
}

func TestValidateFinalElementRejectsTopLevelTextRuns(t *testing.T) {
	// Even a well-formed array is rejected at the top level: schema v3 keeps runs
	// under customData, and the wire must never carry a dual location.
	e := map[string]any{"id": "e1", "type": "text", "index": "a0", "version": 1, "versionNonce": 1, "fontSize": 20, "textRuns": []any{map[string]any{"start": 0, "end": 2, "bold": true}}}
	if err := validateFinalElement(e); err == nil {
		t.Fatal("expected top-level textRuns rejection")
	}
}

func TestValidateFinalElementValidatesCustomDataTextRuns(t *testing.T) {
	base := map[string]any{"id": "e1", "type": "text", "index": "a0", "version": 1, "versionNonce": 1, "fontSize": 20}
	good := cloneMap(base)
	good["customData"] = map[string]any{"textRuns": []any{map[string]any{"start": 0, "end": 2, "bold": true}}}
	if err := validateFinalElement(good); err != nil {
		t.Fatalf("valid customData.textRuns rejected: %v", err)
	}
	bad := cloneMap(base)
	bad["customData"] = map[string]any{"textRuns": []any{map[string]any{"start": 2, "end": 1}}}
	if err := validateFinalElement(bad); err == nil {
		t.Fatal("expected malformed customData.textRuns rejection")
	}
	badType := cloneMap(base)
	badType["customData"] = map[string]any{"textRuns": "nope"}
	if err := validateFinalElement(badType); err == nil {
		t.Fatal("expected non-array customData.textRuns rejection")
	}
}

// TestValidateFinalElementAcceptsNonIntegerFontFamily pins that wire/read-back
// customData.textRuns validation accepts a canonical positive finite non-integer
// fontFamily (aligning with isStyleValue in the shared schema — the integer-only
// rule stays confined to the friendly --font-family flag).
func TestValidateFinalElementAcceptsNonIntegerFontFamily(t *testing.T) {
	base := map[string]any{"id": "e1", "type": "text", "index": "a0", "version": 1, "versionNonce": 1, "fontSize": 20}
	good := cloneMap(base)
	good["customData"] = map[string]any{"textRuns": []any{map[string]any{"start": 0, "end": 2, "fontFamily": 1.5}}}
	if err := validateFinalElement(good); err != nil {
		t.Fatalf("positive non-integer fontFamily rejected: %v", err)
	}
	bad := cloneMap(base)
	bad["customData"] = map[string]any{"textRuns": []any{map[string]any{"start": 0, "end": 2, "fontFamily": 0}}}
	if err := validateFinalElement(bad); err == nil {
		t.Fatal("expected non-positive fontFamily rejection")
	}
}
