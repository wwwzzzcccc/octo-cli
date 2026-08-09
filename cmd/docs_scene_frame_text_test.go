package cmd

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"testing"
)

func TestSceneFrameCommandsSendPatch(t *testing.T) {
	cases := []struct {
		name string
		body string
		args []string
		want func(*testing.T, map[string]any)
	}{
		{
			name: "create",
			body: `{"elements":[],"baseVersion":"BV"}`,
			args: []string{"docs", "scene", "element", "frame-create", "d", "--id", "f1", "--x", "10", "--y", "20", "--width", "300", "--height", "200"},
			want: func(t *testing.T, e map[string]any) {
				if e["id"] != "f1" || e["type"] != "frame" || e["width"] != float64(300) {
					t.Fatalf("frame=%v", e)
				}
			},
		},
		{
			name: "add",
			body: `{"elements":[{"id":"f1","type":"frame","index":"a0","version":1,"versionNonce":1,"isDeleted":false},{"id":"r1","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false}],"baseVersion":"BV"}`,
			args: []string{"docs", "scene", "element", "frame-add", "d", "f1", "--id", "r1"},
			want: func(t *testing.T, e map[string]any) {
				if e["frameId"] != "f1" {
					t.Fatalf("element=%v", e)
				}
			},
		},
		{
			name: "remove",
			body: `{"elements":[{"id":"f1","type":"frame","index":"a0","version":1,"versionNonce":1,"isDeleted":false},{"id":"r1","type":"rectangle","frameId":"f1","index":"a1","version":1,"versionNonce":1,"isDeleted":false}],"baseVersion":"BV"}`,
			args: []string{"docs", "scene", "element", "frame-remove", "d", "f1", "--id", "r1"},
			want: func(t *testing.T, e map[string]any) {
				if e["frameId"] != nil {
					t.Fatalf("element=%v", e)
				}
			},
		},
		{
			name: "unframe",
			body: `{"elements":[{"id":"f1","type":"frame","index":"a0","version":1,"versionNonce":1,"isDeleted":false},{"id":"r1","type":"rectangle","frameId":"f1","index":"a1","version":1,"versionNonce":1,"isDeleted":false}],"baseVersion":"BV"}`,
			args: []string{"docs", "scene", "element", "unframe", "d", "--id", "r1"},
			want: func(t *testing.T, e map[string]any) {
				if e["frameId"] != nil {
					t.Fatalf("element=%v", e)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var patch map[string]any
			_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					io.WriteString(w, tc.body)
					return
				}
				if r.Method != http.MethodPatch || r.Header.Get("If-Match") != "BV" {
					t.Fatalf("request=%s If-Match=%q", r.Method, r.Header.Get("If-Match"))
				}
				if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
					t.Fatal(err)
				}
				io.WriteString(w, `{}`)
			})
			if _, _, err := execRoot(t, cap.f, tc.args...); err != nil {
				t.Fatal(err)
			}
			if cap.requests != 2 {
				t.Fatalf("requests=%d", cap.requests)
			}
			elements := patch["elements"].([]any)
			if len(elements) != 1 {
				t.Fatalf("patch=%v", patch)
			}
			tc.want(t, elements[0].(map[string]any))
		})
	}
}

func TestSceneBindTextAndUnbindTextSendAtomicPatch(t *testing.T) {
	for _, unbind := range []bool{false, true} {
		t.Run(map[bool]string{false: "bind-text", true: "unbind-text"}[unbind], func(t *testing.T) {
			containerID := "null"
			bound := "null"
			autoResize := "true"
			if unbind {
				containerID = `"c1"`
				bound = `[{"id":"t1","type":"text"}]`
				autoResize = "false"
			}
			body := `{"elements":[{"id":"c1","type":"rectangle","x":10,"y":20,"width":200,"height":100,"angle":0,"boundElements":` + bound + `,"index":"a0","version":1,"versionNonce":1,"isDeleted":false},{"id":"t1","type":"text","x":0,"y":0,"width":80,"height":30,"containerId":` + containerID + `,"autoResize":` + autoResize + `,"fontSize":20,"baseline":16,"index":"a1","version":1,"versionNonce":1,"isDeleted":false}],"baseVersion":"BV"}`
			var patch map[string]any
			_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					io.WriteString(w, body)
					return
				}
				if r.Method != http.MethodPatch || r.Header.Get("If-Match") != "BV" {
					t.Fatalf("request=%s If-Match=%q", r.Method, r.Header.Get("If-Match"))
				}
				if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
					t.Fatal(err)
				}
				io.WriteString(w, `{}`)
			})
			args := []string{"docs", "scene", "element", "bind-text", "d", "t1", "c1"}
			if unbind {
				args = []string{"docs", "scene", "element", "unbind-text", "d", "t1"}
			}
			if _, _, err := execRoot(t, cap.f, args...); err != nil {
				t.Fatal(err)
			}
			if cap.requests != 2 || len(patch["elements"].([]any)) != 2 {
				t.Fatalf("requests=%d patch=%v", cap.requests, patch)
			}
			if !unbind {
				var text, container map[string]any
				for _, raw := range patch["elements"].([]any) {
					e := raw.(map[string]any)
					if e["id"] == "t1" {
						text = e
					} else if e["id"] == "c1" {
						container = e
					}
				}
				if text["containerId"] != "c1" || text["autoResize"] != true || text["textAlign"] != "center" || text["verticalAlign"] != "middle" {
					t.Fatalf("bound text contract=%v", text)
				}
				if text["x"] != float64(70) || text["y"] != float64(55) {
					t.Fatalf("bound text must be centered like Web double-click input: %v", text)
				}
				if index, _ := text["index"].(string); index <= "a0" {
					t.Fatalf("bound text must be painted above its container: %v", text)
				}
				bound := container["boundElements"].([]any)[0].(map[string]any)
				if bound["id"] != "t1" || bound["type"] != "text" {
					t.Fatalf("missing reciprocal binding: %v", container)
				}
			}
		})
	}
}

func TestSceneBindTextOnLineConvertsItToHeadlessArrowAndBinds(t *testing.T) {
	body := `{"elements":[{"id":"l1","type":"line","x":100,"y":200,"width":300,"height":100,"angle":0.25,"points":[[0,100],[150,0],[300,100]],"boundElements":null,"index":"a0","version":1,"versionNonce":1,"isDeleted":false,"startArrowhead":null,"endArrowhead":null,"elbowed":false,"polygon":false,"customFuture":{"keep":true}},{"id":"t1","type":"text","x":0,"y":0,"width":80,"height":30,"angle":0,"containerId":null,"autoResize":false,"fontSize":20,"baseline":16,"index":"a1","version":1,"versionNonce":1,"isDeleted":false}],"baseVersion":"BV"}`
	var patch map[string]any
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, body)
			return
		}
		if r.Method != http.MethodPatch || r.Header.Get("If-Match") != "BV" {
			t.Fatalf("request=%s If-Match=%q", r.Method, r.Header.Get("If-Match"))
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, `{}`)
	})
	_, _, err := execRoot(t, cap.f, "docs", "scene", "element", "bind-text", "d", "t1", "l1")
	if err != nil {
		t.Fatal(err)
	}
	if cap.requests != 2 {
		t.Fatalf("requests=%d", cap.requests)
	}
	elements := patch["elements"].([]any)
	if len(elements) != 2 {
		t.Fatalf("line conversion and binding must patch both elements, patch=%v", patch)
	}
	var text, line map[string]any
	for _, raw := range elements {
		element := raw.(map[string]any)
		switch element["id"] {
		case "t1":
			text = element
		case "l1":
			line = element
		}
	}
	if text == nil || line == nil {
		t.Fatalf("missing converted pair: %v", patch)
	}
	if line["type"] != "arrow" || line["startArrowhead"] != nil || line["endArrowhead"] != nil || line["elbowed"] != false {
		t.Fatalf("line must become a visually identical headless arrow: %v", line)
	}
	if _, exists := line["polygon"]; exists {
		t.Fatalf("line-only polygon field must not survive conversion: %v", line)
	}
	if future, ok := line["customFuture"].(map[string]any); !ok || future["keep"] != true {
		t.Fatalf("unknown legal fields must survive conversion: %v", line)
	}
	bound := line["boundElements"].([]any)
	if len(bound) != 1 || bound[0].(map[string]any)["id"] != "t1" || bound[0].(map[string]any)["type"] != "text" {
		t.Fatalf("missing reciprocal text binding: %v", line)
	}
	if text["containerId"] != "l1" || text["autoResize"] != true || text["angle"] != float64(0.25) {
		t.Fatalf("text must be structurally bound to the converted element: %v", text)
	}
	if text["x"] != float64(0) || text["y"] != float64(0) {
		t.Fatalf("linear binding must not invent browser-owned x/y geometry: %v", text)
	}
	if text["textAlign"] != "center" || text["verticalAlign"] != "middle" {
		t.Fatalf("bound text alignment=%v", text)
	}
}

func TestSceneBindTextOnLineRejectsArrowRoutingStateWithoutPatch(t *testing.T) {
	cases := []struct {
		name, extra string
	}{
		{"start arrowhead", `,"startArrowhead":"dot"`},
		{"end arrowhead", `,"endArrowhead":"arrow"`},
		{"elbowed", `,"elbowed":true`},
		{"elbowed string", `,"elbowed":"false"`},
		{"elbowed number", `,"elbowed":0`},
		{"elbowed object", `,"elbowed":{}`},
		{"fixed segments", `,"fixedSegments":null`},
		{"fixed point", `,"fixedPoint":[0.5,0.5]`},
		{"start special", `,"startIsSpecial":false`},
		{"end special", `,"endIsSpecial":true`},
		{"polygon true", `,"polygon":true`},
		{"polygon malformed", `,"polygon":"false"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"elements":[{"id":"l1","type":"line","x":0,"y":0,"width":100,"height":0,"angle":0,"points":[[0,0],[100,0]],"boundElements":null,"index":"a0","version":1,"versionNonce":1,"isDeleted":false` + tc.extra + `},{"id":"t1","type":"text","x":0,"y":0,"width":40,"height":20,"angle":0,"containerId":null,"index":"a1","version":1,"versionNonce":1,"isDeleted":false}],"baseVersion":"BV"}`
			_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Fatalf("unsafe request %s: rejected conversion must PATCH=0", r.Method)
				}
				io.WriteString(w, body)
			})
			if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "bind-text", "d", "t1", "l1"); err == nil {
				t.Fatal("expected fail-closed conversion rejection")
			}
			if cap.requests != 1 {
				t.Fatalf("requests=%d, want GET only", cap.requests)
			}
		})
	}
}

func TestArrowBoundTextPositionRequiresBrowserResolvedGeometry(t *testing.T) {
	container := map[string]any{
		"type": "arrow", "x": 100.0, "y": 200.0, "width": 300.0, "height": 100.0,
		"angle": math.Pi / 2, "points": []any{[]any{0.0, 0.0}, []any{300.0, 100.0}},
	}
	text := map[string]any{"width": 80.0, "height": 20.0, "x": 7.0, "y": 9.0}
	if err := positionBoundText(text, container); err == nil {
		t.Fatal("expected browser-resolved geometry error")
	}
	if text["x"] != 7.0 || text["y"] != 9.0 {
		t.Fatalf("rejected positioning must not mutate text: %v", text)
	}
}
