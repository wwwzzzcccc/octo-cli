package cmd

import (
	"math"
	"testing"

	"github.com/spf13/cobra"
)

func TestBaseElementSupportedTypes(t *testing.T) {
	for _, kind := range []string{"rectangle", "ellipse", "diamond", "text", "arrow", "line"} {
		t.Run(kind, func(t *testing.T) {
			o := &sceneFlags{typeName: kind, id: "e1", text: "hello", width: 120, height: 40, strokeColor: "#000", backgroundColor: "transparent", fillStyle: "solid", strokeWidth: 1, roughness: 1, opacity: 100, fontSize: 20, strokeStyle: "solid", fontFamily: 1, textAlign: "left", verticalAlign: "top", lineHeight: 1.25}
			e, err := baseElement(nil, o, "a0")
			if err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"id", "type", "x", "y", "width", "height", "angle", "strokeColor", "backgroundColor", "fillStyle", "strokeWidth", "strokeStyle", "roughness", "opacity", "groupIds", "frameId", "index", "seed", "version", "versionNonce", "isDeleted", "boundElements", "updated", "link", "locked"} {
				if _, ok := e[field]; !ok {
					t.Errorf("missing base field %s: %v", field, e)
				}
			}
			if kind == "text" && e["originalText"] != "hello" {
				t.Errorf("text fields=%v", e)
			}
			if (kind == "arrow" || kind == "line") && e["points"] == nil {
				t.Errorf("linear fields=%v", e)
			}
		})
	}
}

func TestApplyLayerGeneratesBoundedIndex(t *testing.T) {
	elements := []map[string]any{{"id": "a", "index": "a0"}, {"id": "b", "index": "a1"}, {"id": "c", "index": "a2"}}
	target := cloneMap(elements[2])
	if err := applyLayer(target, elements, "after", "a"); err != nil {
		t.Fatal(err)
	}
	index := target["index"].(string)
	if index <= "a0" || index >= "a1" {
		t.Fatalf("index=%q", index)
	}
}

func TestApplyChangedNumbersResizesTwoPointLinearElement(t *testing.T) {
	cmd := &cobra.Command{}
	o := &sceneFlags{}
	bindTransformFlags(cmd, o)
	if err := cmd.Flags().Set("width", "240"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("height", "80"); err != nil {
		t.Fatal(err)
	}
	e := map[string]any{"type": "line", "width": 120.0, "height": 40.0, "points": []any{[]any{0.0, 0.0}, []any{120.0, 40.0}}}
	if err := applyChangedNumbers(cmd, e, o); err != nil {
		t.Fatal(err)
	}
	points := e["points"].([]any)
	last := points[1].([]any)
	if last[0] != float64(240) || last[1] != float64(80) {
		t.Fatalf("element=%v", e)
	}
}

func TestApplyChangedNumbersRejectsComplexLinearResize(t *testing.T) {
	cmd := &cobra.Command{}
	o := &sceneFlags{}
	bindTransformFlags(cmd, o)
	_ = cmd.Flags().Set("width", "240")
	e := map[string]any{"type": "arrow", "width": 120.0, "height": 40.0, "points": []any{[]any{0.0, 0.0}, []any{40.0, 20.0}, []any{120.0, 40.0}}}
	if err := applyChangedNumbers(cmd, e, o); err == nil {
		t.Fatal("expected complex linear resize rejection")
	}
}

func TestApplyChangedStyleScalesTextDerivedGeometry(t *testing.T) {
	cmd := &cobra.Command{}
	o := &sceneFlags{}
	bindStyleFlags(cmd, o)
	_ = cmd.Flags().Set("font-size", "40")
	e := map[string]any{"type": "text", "text": "hello", "autoResize": true, "containerId": nil, "boundElements": nil, "fontSize": 20.0, "width": 100.0, "height": 50.0, "baseline": 20.0}
	if err := applyChangedStyle(cmd, e, o); err != nil {
		t.Fatal(err)
	}
	if e["width"] != float64(200) || e["height"] != float64(100) || e["baseline"] != float64(40) {
		t.Fatalf("element=%v", e)
	}
}

func TestFontSizeRejectsNonSimpleText(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"multiline":   func(e map[string]any) { e["text"] = "a\nb" },
		"fixed width": func(e map[string]any) { e["autoResize"] = false },
		"container":   func(e map[string]any) { e["containerId"] = "box" },
		"bindings":    func(e map[string]any) { e["boundElements"] = []any{map[string]any{"id": "x"}} },
	} {
		t.Run(name, func(t *testing.T) {
			cmd := &cobra.Command{}
			o := &sceneFlags{}
			bindStyleFlags(cmd, o)
			_ = cmd.Flags().Set("font-size", "40")
			e := map[string]any{"type": "text", "text": "hello", "autoResize": true, "containerId": nil, "boundElements": nil, "fontSize": 20.0, "width": 100.0, "height": 50.0, "baseline": 20.0}
			change(e)
			if err := applyChangedStyle(cmd, e, o); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestLinearResizeRejectsBindingAndComplexState(t *testing.T) {
	for _, field := range []string{"startBinding", "endBinding", "boundElements", "fixedPoint", "fixedSegments", "elbowed"} {
		t.Run(field, func(t *testing.T) {
			cmd := &cobra.Command{}
			o := &sceneFlags{}
			bindTransformFlags(cmd, o)
			_ = cmd.Flags().Set("width", "240")
			e := map[string]any{"type": "arrow", "width": 120.0, "height": 40.0, "points": []any{[]any{0.0, 0.0}, []any{120.0, 40.0}}}
			if field == "elbowed" {
				e[field] = true
			} else {
				e[field] = map[string]any{"id": "x"}
			}
			if err := applyChangedNumbers(cmd, e, o); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestFinalValidationRejectsMalformedLinearPoints(t *testing.T) {
	base := map[string]any{"id": "l", "type": "line", "index": "a0", "version": 1, "versionNonce": 1, "width": 1, "height": 1}
	for _, points := range []any{
		[]any{[]any{0.0, 0.0}, []any{1.0}},
		[]any{[]any{0.0, 0.0}, []any{math.NaN(), 1.0}},
		[]any{[]any{0.0, 0.0}, []any{"1", 1.0}},
	} {
		e := cloneMap(base)
		e["points"] = points
		if err := validateFinalElement(e); err == nil {
			t.Fatalf("accepted points %v", points)
		}
	}
}

func TestApplyLayerSortsInputAndRejectsDuplicateIndex(t *testing.T) {
	elements := []map[string]any{{"id": "c", "index": "a2"}, {"id": "a", "index": "a0"}, {"id": "b", "index": "a1"}}
	target := cloneMap(elements[0])
	if err := applyLayer(target, elements, "after", "a"); err != nil {
		t.Fatal(err)
	}
	if got := target["index"].(string); got <= "a0" || got >= "a1" {
		t.Fatalf("index=%q", got)
	}
	duplicates := []map[string]any{{"id": "a", "index": "a0"}, {"id": "b", "index": "a0"}, {"id": "c", "index": "a2"}}
	if err := applyLayer(cloneMap(duplicates[2]), duplicates, "front", ""); err == nil {
		t.Fatal("expected duplicate index rejection")
	}
}

func TestValidationRejectsNonFiniteAndStyleRanges(t *testing.T) {
	if err := validateCreateFlags(&sceneFlags{x: math.NaN(), width: 1, height: 1, strokeColor: "#000", backgroundColor: "transparent", fillStyle: "solid", strokeWidth: 1, roughness: 1, opacity: 100, fontSize: 20}); err == nil {
		t.Fatal("expected NaN rejection")
	}
	for _, o := range []*sceneFlags{
		{width: 1, height: 1, strokeColor: "#000", backgroundColor: "transparent", fillStyle: "bad", strokeWidth: 1, roughness: 1, opacity: 100, fontSize: 20},
		{width: 1, height: 1, strokeColor: "#000", backgroundColor: "transparent", fillStyle: "solid", strokeWidth: 1, roughness: 1, opacity: 101, fontSize: 20},
		{width: 1, height: 1, strokeColor: "#000", backgroundColor: "transparent", fillStyle: "solid", strokeWidth: 1, roughness: 3, opacity: 100, fontSize: 20},
		{width: 1, height: 1, strokeColor: "#000", backgroundColor: "transparent", fillStyle: "solid", strokeWidth: 1, roughness: 1, opacity: 100, fontSize: 0},
	} {
		if err := validateCreateFlags(o); err == nil {
			t.Fatalf("expected invalid style rejection: %+v", o)
		}
	}
}

func TestApplyChangedNumbersResizesNewTwoPointLinearZeroAxis(t *testing.T) {
	cmd := &cobra.Command{}
	o := &sceneFlags{}
	bindTransformFlags(cmd, o)
	_ = cmd.Flags().Set("height", "30")
	e := map[string]any{"type": "arrow", "width": 120.0, "height": 0.0, "points": []any{[]any{0.0, 0.0}, []any{120.0, 0.0}}}
	if err := applyChangedNumbers(cmd, e, o); err != nil {
		t.Fatal(err)
	}
	last := e["points"].([]any)[1].([]any)
	if last[1] != float64(30) {
		t.Fatalf("element=%v", e)
	}
}
