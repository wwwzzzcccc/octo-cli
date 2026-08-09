package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestSceneFreedrawCreateAndUpdate(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "create", "d1", "--type", "freedraw", "--id", "f1", "--points", `[[0,0],[2,3]]`, "--pressures", `[0.2,1]`, "--simulate-pressure=false")
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	if e["type"] != "freedraw" || len(e["pressures"].([]any)) != 2 || e["simulatePressure"] != false || e["width"] != float64(2) || e["height"] != float64(3) {
		t.Fatalf("element=%v", e)
	}

	body := `{"elements":[{"id":"f1","type":"freedraw","index":"a0","version":2,"versionNonce":3,"isDeleted":false,"points":[[0,0]],"pressures":[],"simulatePressure":true,"lastCommittedPoint":null,"future":{"keep":true}}],"baseVersion":"BV"}`
	var patch map[string]any
	_, cap = semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, body)
			return
		}
		json.NewDecoder(r.Body).Decode(&patch)
		io.WriteString(w, `{}`)
	})
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "freedraw", "d", "f1", "--points", `[[0,0],[4,5]]`, "--pressures", `[]`, "--simulate-pressure=false", "--last-committed-point", `null`); err != nil {
		t.Fatal(err)
	}
	e = patch["elements"].([]any)[0].(map[string]any)
	if e["future"].(map[string]any)["keep"] != true || e["simulatePressure"] != false || e["version"] != float64(3) {
		t.Fatalf("element=%v", e)
	}
}

func TestSceneFreedrawRejectsMalformedInputBeforeRequest(t *testing.T) {
	for _, args := range [][]string{
		{"docs", "scene", "element", "create", "d1", "--type", "freedraw", "--points", `[]`},
		{"docs", "scene", "element", "create", "d1", "--type", "freedraw", "--points", `[[0,0],[1,1]]`, "--pressures", `[0.5]`},
		{"docs", "scene", "element", "create", "d1", "--type", "freedraw", "--points", `[[0,0]]`, "--pressures", `[2]`},
		{"docs", "scene", "element", "create", "d1", "--type", "freedraw", "--points", `[[0,0]]`, "--start-arrowhead", "arrow"},
		{"docs", "scene", "element", "create", "d1", "--type", "freedraw", "--points", `[[0,0]]`, "--end-arrowhead", "dot"},
		{"docs", "scene", "element", "create", "d1", "--type", "rectangle", "--pressures", `[]`},
	} {
		_, cap := semanticFactory(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected request") })
		if _, _, err := execRoot(t, cap.f, args...); err == nil {
			t.Fatalf("expected rejection: %v", args)
		}
	}
}

func TestSceneFreedrawNormalizesPointsAndLastCommittedPoint(t *testing.T) {
	body := `{"elements":[{"id":"f1","type":"freedraw","x":100,"y":200,"index":"a0","version":2,"versionNonce":3,"isDeleted":false,"points":[[0,0]],"pressures":[],"simulatePressure":true,"lastCommittedPoint":[1,1]}],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "freedraw", "d1", "f1", "--points", `[[10,20],[15,30]]`, "--last-committed-point", `[20,40]`)
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	if e["x"] != float64(110) || e["y"] != float64(220) || !samePoint(e["points"].([]any)[0], []any{0, 0}) || !samePoint(e["lastCommittedPoint"], []any{10, 20}) {
		t.Fatalf("element not normalized: %v", e)
	}
}
