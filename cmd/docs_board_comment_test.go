package cmd

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"testing"
)

func closeBoardNumber(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

func TestParseBoardPointStrict(t *testing.T) {
	for _, value := range []string{"1x,2", "1,2x", "1e2x,2", "NaN,2", "1,Inf"} {
		if _, _, err := parseBoardPoint(value); err == nil {
			t.Errorf("parseBoardPoint(%q) accepted invalid input", value)
		}
	}
	x, y, err := parseBoardPoint(" 1e2 , -2.5 ")
	if err != nil || x != 100 || y != -2.5 {
		t.Fatalf("valid point = %v,%v err=%v", x, y, err)
	}
}

func TestBoardElementAnchorRotatedRectangle(t *testing.T) {
	elements := []map[string]any{{"id": "r", "type": "rectangle", "x": float64(10), "y": float64(20), "width": float64(40), "height": float64(20), "angle": math.Pi / 2}}
	anchor, _, err := boardElementAnchor(elements, []string{"r"})
	if err != nil {
		t.Fatal(err)
	}
	if !closeBoardNumber(anchor["x"].(float64), 40) || !closeBoardNumber(anchor["y"].(float64), 10) {
		t.Fatalf("anchor=%v, want 40,10", anchor)
	}
}

func TestBoardElementAnchorFreedrawUsesPointsAndRotation(t *testing.T) {
	elements := []map[string]any{{
		"id": "f", "type": "freedraw", "x": float64(100), "y": float64(200),
		"width": float64(999), "height": float64(999), "angle": math.Pi / 2,
		"points": []any{[]any{float64(0), float64(0)}, []any{float64(20), float64(10)}, []any{float64(5), float64(30)}},
	}}
	anchor, _, err := boardElementAnchor(elements, []string{"f"})
	if err != nil {
		t.Fatal(err)
	}
	if !closeBoardNumber(anchor["x"].(float64), 125) || !closeBoardNumber(anchor["y"].(float64), 205) {
		t.Fatalf("anchor=%v, want 125,205", anchor)
	}
}

func TestBoardElementAnchorRotatedArrowMatchesWebRoughBounds(t *testing.T) {
	elements := []map[string]any{{
		"id": "a", "type": "arrow", "x": float64(100), "y": float64(100), "width": float64(40), "height": float64(20),
		"angle": math.Pi / 2, "points": []any{[]any{float64(.5), float64(.5)}, []any{float64(39.5), float64(19.5)}},
		"seed": float64(2072220693), "roughness": float64(1), "strokeStyle": "solid",
	}}
	anchor, _, err := boardElementAnchor(elements, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if !closeBoardNumber(anchor["x"].(float64), 129.5) || !closeBoardNumber(anchor["y"].(float64), 90.5) {
		t.Fatalf("anchor=%v, want 129.5,90.5", anchor)
	}
}

func TestBoardElementAnchorMixedSelectionUsesVisualUnion(t *testing.T) {
	elements := []map[string]any{
		{"id": "r", "type": "rectangle", "x": float64(10), "y": float64(20), "width": float64(40), "height": float64(20), "angle": math.Pi / 2},
		{"id": "f", "type": "freedraw", "x": float64(100), "y": float64(200), "angle": math.Pi / 2, "points": []any{[]any{float64(0), float64(0)}, []any{float64(20), float64(10)}, []any{float64(5), float64(30)}}},
	}
	anchor, _, err := boardElementAnchor(elements, []string{"r", "f"})
	if err != nil {
		t.Fatal(err)
	}
	if !closeBoardNumber(anchor["x"].(float64), 72.5) || !closeBoardNumber(anchor["y"].(float64), 117.5) {
		t.Fatalf("anchor=%v, want 72.5,117.5", anchor)
	}
}

func TestDocsBoardCommentDryRunPostsCanonicalAnchor(t *testing.T) {
	body := `{"elements":[{"id":"r","type":"rectangle","x":10,"y":20,"width":40,"height":20,"angle":1.5707963267948966,"isDeleted":false}],"files":{},"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	stdout, _, err := execRoot(t, cap.f, "--dry-run", "docs", "comments", "add-board", "d1", "--body", "review", "--element-id", "r")
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	data := envelope["data"].(map[string]any)
	if data["method"] != http.MethodPost || data["path"] != "/v1/bot/docs/d1/comments" {
		t.Fatalf("request=%v", data)
	}
	requestBody := data["body"].(map[string]any)
	decoded, err := base64.StdEncoding.DecodeString(requestBody["anchorStart"].(string))
	if err != nil {
		t.Fatal(err)
	}
	var anchor map[string]any
	if err := json.Unmarshal(decoded, &anchor); err != nil {
		t.Fatal(err)
	}
	if !closeBoardNumber(anchor["x"].(float64), 40) || !closeBoardNumber(anchor["y"].(float64), 10) || requestBody["anchorStart"] != requestBody["anchorEnd"] {
		t.Fatalf("anchor/body=%v", requestBody)
	}
}
