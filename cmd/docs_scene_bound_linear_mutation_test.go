package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestSingleLinearMutationDoesNotRewriteBrowserOwnedBoundText(t *testing.T) {
	body := `{"elements":[{"id":"a1","type":"arrow","x":100,"y":200,"width":100,"height":0,"angle":0,"points":[[0,0],[100,0]],"boundElements":[{"id":"t1","type":"text"}],"index":"a0","version":2,"versionNonce":3,"isDeleted":false,"elbowed":false},{"id":"t1","type":"text","x":130,"y":190,"width":40,"height":20,"angle":0,"containerId":"a1","autoResize":true,"fontSize":20,"baseline":16,"lineHeight":1.25,"text":"label","index":"a1","version":4,"versionNonce":5,"isDeleted":false}],"baseVersion":"BV"}`
	var patch map[string]any
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, body)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, `{}`)
	})
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "linear", "d", "a1", "--points", `[[0,0],[100,100]]`); err != nil {
		t.Fatal(err)
	}
	if cap.requests != 2 {
		t.Fatalf("requests=%d", cap.requests)
	}
	got := elementsByID(t, patch)
	if len(got) != 1 || got["a1"] == nil || got["t1"] != nil {
		t.Fatalf("linear mutation must patch only the container: %v", patch)
	}
}

func TestSingleTransformDoesNotRewriteBrowserOwnedBoundText(t *testing.T) {
	body := `{"elements":[{"id":"a1","type":"arrow","x":100,"y":200,"width":100,"height":0,"angle":0,"points":[[0,0],[100,0]],"boundElements":[{"id":"t1","type":"text"}],"index":"a0","version":2,"versionNonce":3,"isDeleted":false,"elbowed":false},{"id":"t1","type":"text","x":130,"y":190,"width":40,"height":20,"angle":0,"containerId":"a1","autoResize":true,"fontSize":20,"baseline":16,"lineHeight":1.25,"text":"label","index":"a1","version":4,"versionNonce":5,"isDeleted":false}],"baseVersion":"BV"}`
	var patch map[string]any
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, body)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, `{}`)
	})
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "transform", "d", "a1", "--rotate-deg", "90"); err != nil {
		t.Fatal(err)
	}
	got := elementsByID(t, patch)
	if len(got) != 1 || got["a1"] == nil || got["t1"] != nil {
		t.Fatalf("transform must patch only the container: %v", patch)
	}
}

func TestLinearUnbindFailsClosedWithoutBrowserResolvedGeometry(t *testing.T) {
	body := `{"elements":[{"id":"a1","type":"arrow","x":100,"y":200,"width":100,"height":0,"angle":0,"points":[[0,0],[100,0]],"boundElements":[{"id":"t1","type":"text"}],"index":"a0","version":2,"versionNonce":3,"isDeleted":false,"elbowed":false},{"id":"t1","type":"text","x":130,"y":190,"width":40,"height":20,"angle":0,"containerId":"a1","autoResize":true,"fontSize":20,"baseline":16,"lineHeight":1.25,"text":"label","index":"a1","version":4,"versionNonce":5,"isDeleted":false}],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unsafe request %s: rejected unbind must PATCH=0", r.Method)
		}
		io.WriteString(w, body)
	})
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "unbind-text", "d", "t1"); err == nil {
		t.Fatal("expected fail-closed linear unbind rejection")
	}
	if cap.requests != 1 {
		t.Fatalf("requests=%d, want GET only", cap.requests)
	}
}

func elementsByID(t *testing.T, patch map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, raw := range patch["elements"].([]any) {
		e := raw.(map[string]any)
		out[e["id"].(string)] = e
	}
	return out
}
