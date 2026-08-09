package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestSceneBindAndUnbindAreAtomicReciprocalCAS(t *testing.T) {
	for _, unbind := range []bool{false, true} {
		t.Run(map[bool]string{false: "bind", true: "unbind"}[unbind], func(t *testing.T) {
			binding := "null"
			bound := "null"
			if unbind {
				binding = `{"elementId":"r1","focus":0.25,"gap":8,"future":"keep"}`
				bound = `[{"id":"ar1","type":"arrow","future":"keep"}]`
			}
			body := `{"elements":[{"id":"r1","type":"rectangle","index":"a0","version":2,"versionNonce":3,"isDeleted":false,"boundElements":` + bound + `,"futureTarget":1},{"id":"ar1","type":"arrow","index":"a1","version":4,"versionNonce":5,"isDeleted":false,"points":[[0,0],[10,10]],"startBinding":` + binding + `,"endBinding":null,"futureArrow":2}],"baseVersion":"BV"}`
			var patch map[string]any
			_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					io.WriteString(w, body)
					return
				}
				if r.Method != http.MethodPatch || r.Header.Get("If-Match") != "BV" {
					t.Fatalf("request=%s if-match=%q", r.Method, r.Header.Get("If-Match"))
				}
				if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
					t.Fatal(err)
				}
				io.WriteString(w, `{}`)
			})
			args := []string{"docs", "scene", "element", "bind", "d", "ar1", "--endpoint", "start", "--element-id", "r1", "--focus", "0.25", "--gap", "8"}
			if unbind {
				args = []string{"docs", "scene", "element", "unbind", "d", "ar1", "--endpoint", "start"}
			}
			if _, _, err := execRoot(t, cap.f, args...); err != nil {
				t.Fatal(err)
			}
			if cap.requests != 2 || len(patch["elements"].([]any)) != 2 {
				t.Fatalf("requests=%d patch=%v", cap.requests, patch)
			}
			for _, raw := range patch["elements"].([]any) {
				e := raw.(map[string]any)
				if e["id"] == "ar1" && e["futureArrow"] != float64(2) || e["id"] == "r1" && e["futureTarget"] != float64(1) {
					t.Fatalf("unknown field lost: %v", e)
				}
			}
		})
	}
}

func TestSceneBindRejectsConflictAndWrongTypeWithoutPatch(t *testing.T) {
	cases := []struct{ body, target string }{
		{`{"elements":[{"id":"r1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false},{"id":"ar1","type":"arrow","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"points":[[0,0],[1,1]],"startBinding":{"elementId":"r1","focus":0,"gap":0}}],"baseVersion":"BV"}`, "r1"},
		{`{"elements":[{"id":"l1","type":"line","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"points":[[0,0],[1,1]]},{"id":"ar1","type":"arrow","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"points":[[0,0],[1,1]]}],"baseVersion":"BV"}`, "l1"},
	}
	for _, tc := range cases {
		_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Fatal("unexpected patch")
			}
			io.WriteString(w, tc.body)
		})
		if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "bind", "d", "ar1", "--endpoint", "start", "--element-id", tc.target, "--focus", "0", "--gap", "0"); err == nil {
			t.Fatal("expected rejection")
		}
		if cap.requests != 1 {
			t.Fatalf("requests=%d", cap.requests)
		}
	}
}

func TestSceneBindRejectsOneSidedOldOwnerWithoutPatch(t *testing.T) {
	body := `{"elements":[{"id":"old","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"id":"ar1","type":"arrow"}]},{"id":"new","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false},{"id":"ar1","type":"arrow","index":"a2","version":1,"versionNonce":1,"isDeleted":false,"points":[[0,0],[1,1]],"startBinding":null,"endBinding":null}],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatal("one-sided owner must not be patched")
		}
		io.WriteString(w, body)
	})
	_, _, err := execRoot(t, cap.f, "docs", "scene", "element", "bind", "d", "ar1", "--endpoint", "start", "--element-id", "new", "--focus", "0", "--gap", "0")
	if err == nil {
		t.Fatal("expected one-sided owner rejection")
	}
	if cap.requests != 1 {
		t.Fatalf("requests=%d", cap.requests)
	}
}
