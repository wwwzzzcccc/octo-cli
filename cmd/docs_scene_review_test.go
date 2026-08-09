package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// ---- P1-A: text/container compatibility ----

// TestSceneMultiValidTextContainers pins that a bound text whose containerId
// resolves to any of the four supported Excalidraw text containers — rectangle,
// ellipse, diamond, or arrow (labelled arrow) — forms one valid structural unit:
// selecting the whole unit applies as a single PATCH.
func TestSceneMultiValidTextContainers(t *testing.T) {
	for _, kind := range []string{"rectangle", "ellipse", "diamond", "arrow"} {
		t.Run(kind, func(t *testing.T) {
			container := `{"id":"c1","type":"` + kind + `","x":1,"index":"a0","version":1,"versionNonce":1,"isDeleted":false`
			if kind == "arrow" {
				container += `,"points":[[0,0],[10,10]]`
			}
			container += `}`
			body := `{"elements":[
				` + container + `,
				{"id":"t1","type":"text","x":2,"index":"a1","version":1,"versionNonce":1,"isDeleted":false,"containerId":"c1","fontSize":20}
			],"baseVersion":"BV"}`
			_, cap := semanticFactory(t, serveScene(t, body))
			out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "transform-many", "d1",
				"--id", "c1", "--id", "t1", "--x", "99")
			if err != nil {
				t.Fatalf("%s container should be valid: %v", kind, err)
			}
			els := dryRunElements(t, dryRunData(t, out))
			if len(els) != 2 || els["c1"]["x"] != float64(99) || els["t1"]["x"] != float64(99) {
				t.Fatalf("%s bound-text unit not transformed atomically: %v", kind, els)
			}
		})
	}
}

// TestSceneMultiRejectsInvalidTextContainers pins that a text whose containerId
// resolves to a type Excalidraw cannot use as a text container — line, freedraw,
// image, frame, embeddable, or another text — is rejected before any PATCH.
func TestSceneMultiRejectsInvalidTextContainers(t *testing.T) {
	for _, kind := range []string{"line", "freedraw", "image", "frame", "embeddable", "text"} {
		t.Run(kind, func(t *testing.T) {
			container := `{"id":"c1","type":"` + kind + `","index":"a1","version":1,"versionNonce":1,"isDeleted":false`
			if kind == "text" {
				container += `,"fontSize":20`
			}
			container += `}`
			body := `{"elements":[
				{"id":"t1","type":"text","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"containerId":"c1","fontSize":20},
				` + container + `
			],"baseVersion":"BV"}`
			_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Fatalf("invalid container must not PATCH: %s", r.Method)
				}
				io.WriteString(w, body)
			})
			if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "transform-many", "d1", "--id", "t1", "--x", "99"); err == nil {
				t.Fatalf("expected rejection for text container of type %q", kind)
			}
			if cap.requests != 1 {
				t.Fatalf("requests=%d for %q", cap.requests, kind)
			}
		})
	}
}

// TestSceneMultiArrowEndpointToUnboundText pins that an UNBOUND text (no
// containerId) is a legitimate arrow endpoint target: the arrow is validated but
// never unioned, so the text stays mutable on its own.
func TestSceneMultiArrowEndpointToUnboundText(t *testing.T) {
	body := `{"elements":[
		{"id":"t1","type":"text","x":2,"index":"a0","version":1,"versionNonce":1,"isDeleted":false,"fontSize":20},
		{"id":"ar1","type":"arrow","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"points":[[0,0],[5,5]],"endBinding":{"elementId":"t1"}}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "transform-many", "d1", "--id", "t1", "--x", "99")
	if err != nil {
		t.Fatalf("unbound text must be a valid arrow endpoint and mutable alone: %v", err)
	}
	els := dryRunElements(t, dryRunData(t, out))
	if len(els) != 1 || els["t1"]["x"] != float64(99) {
		t.Fatalf("arrow must not be pulled into the batch: %v", els)
	}
}

// TestSceneMultiArrowEndpointToBoundTextRejected pins that a text bound inside a
// container (non-null containerId) may NOT be a direct arrow endpoint — the arrow
// must bind the container, not its label text. The whole batch fails before any
// PATCH.
func TestSceneMultiArrowEndpointToBoundTextRejected(t *testing.T) {
	body := `{"elements":[
		{"id":"c1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"t1","type":"text","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"containerId":"c1","fontSize":20},
		{"id":"ar1","type":"arrow","index":"a2","version":1,"versionNonce":1,"isDeleted":false,"points":[[0,0],[5,5]],"endBinding":{"elementId":"t1"}}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("arrow bound to container-bound text must not PATCH: %s", r.Method)
		}
		io.WriteString(w, body)
	})
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "transform-many", "d1", "--id", "ar1", "--x", "99"); err == nil {
		t.Fatal("expected rejection: arrow endpoint references container-bound text")
	}
	if cap.requests != 1 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

// ---- P1-B: strict live-id validation in buildStructuralGraph ----

// TestBuildStructuralGraphRejectsMalformedLiveIDs pins that buildStructuralGraph
// rejects (never silently skips) any live element with a missing, non-string, or
// empty id — including one carrying frameId/containerId/boundElements/arrow
// bindings — as well as a plain malformed live element, before building byID.
func TestBuildStructuralGraphRejectsMalformedLiveIDs(t *testing.T) {
	cases := []struct {
		name    string
		element map[string]any
	}{
		{"missing id with frameId", map[string]any{"type": "rectangle", "index": "a0", "frameId": "f1"}},
		{"empty id with containerId", map[string]any{"id": "", "type": "text", "index": "a0", "containerId": "c1"}},
		{"non-string id with boundElements", map[string]any{"id": 123, "type": "rectangle", "index": "a0", "boundElements": []any{map[string]any{"type": "text", "id": "t1"}}}},
		{"non-string id with arrow binding", map[string]any{"id": 123, "type": "arrow", "index": "a0", "startBinding": map[string]any{"elementId": "x"}}},
		{"plain malformed live element", map[string]any{"type": "rectangle", "index": "a0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildStructuralGraph([]map[string]any{tc.element}); err == nil {
				t.Fatalf("expected buildStructuralGraph to reject %q", tc.name)
			}
		})
	}
	// Tombstones remain exempt: a deleted element with no id must not trip the guard.
	if _, err := buildStructuralGraph([]map[string]any{
		{"id": "e1", "type": "rectangle", "index": "a0"},
		{"type": "rectangle", "index": "a1", "isDeleted": true},
	}); err != nil {
		t.Fatalf("tombstone without id must be tolerated: %v", err)
	}
}

// TestSceneMultiRejectsMalformedLiveIDNoPatch pins the same guard at the command
// boundary: a scene carrying a malformed live element fails the whole batch before
// any PATCH, even when the selected element itself is well-formed.
func TestSceneMultiRejectsMalformedLiveIDNoPatch(t *testing.T) {
	cases := []struct {
		name    string
		sibling string
	}{
		{"missing id with frameId", `{"type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"frameId":"f1"}`},
		{"empty id with containerId", `{"id":"","type":"text","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"containerId":"e1","fontSize":20}`},
		{"non-string id with boundElements", `{"id":123,"type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"text","id":"t1"}]}`},
		{"non-string id with arrow binding", `{"id":123,"type":"arrow","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"startBinding":{"elementId":"e1"}}`},
		{"plain malformed", `{"type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"elements":[
				{"id":"e1","type":"rectangle","x":1,"index":"a0","version":1,"versionNonce":1,"isDeleted":false},
				` + tc.sibling + `
			],"baseVersion":"BV"}`
			_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Fatalf("malformed live id must not PATCH: %s", r.Method)
				}
				io.WriteString(w, body)
			})
			if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "transform-many", "d1", "--id", "e1", "--x", "99"); err == nil {
				t.Fatalf("expected rejection for %q", tc.name)
			}
			if cap.requests != 1 {
				t.Fatalf("requests=%d for %q", cap.requests, tc.name)
			}
		})
	}
}

// ---- P2: duplicate boundElements on one owner ----

// TestSceneMultiRejectsDuplicateBoundElements pins that a single owner listing the
// same bound id twice — even an exactly identical duplicate entry — is a corrupt
// binding list and fails the whole batch before any PATCH.
func TestSceneMultiRejectsDuplicateBoundElements(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"identical duplicate text entry", `{"elements":[
			{"id":"c1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"text","id":"t1"},{"type":"text","id":"t1"}]},
			{"id":"t1","type":"text","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"fontSize":20}
		],"baseVersion":"BV"}`},
		{"duplicate arrow entry", `{"elements":[
			{"id":"c1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"arrow","id":"ar1"},{"type":"arrow","id":"ar1"}]},
			{"id":"ar1","type":"arrow","index":"a1","version":1,"versionNonce":1,"isDeleted":false}
		],"baseVersion":"BV"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.body
			_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Fatalf("duplicate boundElements must not PATCH: %s", r.Method)
				}
				io.WriteString(w, b)
			})
			if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "transform-many", "d1", "--id", "c1", "--x", "99"); err == nil {
				t.Fatalf("expected duplicate-boundElements rejection for %q", tc.name)
			}
			if cap.requests != 1 {
				t.Fatalf("requests=%d for %q", cap.requests, tc.name)
			}
		})
	}
}

// ---- P2: layer no-op output conventions ----

// TestSceneLayerManyDryRunNoOpCarriesDryRun pins that a semantic no-op under
// --dry-run emits dry_run:true (consistent with the dry-run PATCH envelope) while
// still marking noop:true and issuing no PATCH.
func TestSceneLayerManyDryRunNoOpCarriesDryRun(t *testing.T) {
	body := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "layer-many", "d1",
		"--id", "e2", "--position", "front")
	if err != nil {
		t.Fatalf("dry-run no-op must succeed: %v", err)
	}
	if cap.requests != 1 {
		t.Fatalf("dry-run no-op must not PATCH; requests=%d", cap.requests)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope: %v\n%s", err, out)
	}
	data, _ := env["data"].(map[string]any)
	if data["noop"] != true {
		t.Fatalf("expected noop:true, got %v", data)
	}
	if data["dry_run"] != true {
		t.Fatalf("dry-run no-op must carry dry_run:true, got %v", data)
	}
}

// TestSceneLayerManyNonDryRunNoOpOmitsDryRun guards the other side of the
// convention: without --dry-run the no-op envelope marks noop:true and does NOT
// spuriously carry a dry_run marker.
func TestSceneLayerManyNonDryRunNoOpOmitsDryRun(t *testing.T) {
	body := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "docs", "scene", "element", "layer-many", "d1",
		"--id", "e2", "--position", "front")
	if err != nil {
		t.Fatalf("no-op must succeed: %v", err)
	}
	if cap.requests != 1 {
		t.Fatalf("no-op must not PATCH; requests=%d", cap.requests)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope: %v\n%s", err, out)
	}
	data, _ := env["data"].(map[string]any)
	if data["noop"] != true {
		t.Fatalf("expected noop:true, got %v", data)
	}
	if _, ok := data["dry_run"]; ok {
		t.Fatalf("non-dry-run no-op must not carry dry_run, got %v", data)
	}
}

// TestSceneLayerManyNoOpJQFilter pins that the no-op success envelope is a normal
// envelope the universal --jq filter operates on: `.data.noop` extracts true.
func TestSceneLayerManyNoOpJQFilter(t *testing.T) {
	body := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--jq", ".data.noop", "docs", "scene", "element", "layer-many", "d1",
		"--id", "e2", "--position", "front")
	if err != nil {
		t.Fatalf("no-op with --jq must succeed: %v", err)
	}
	if cap.requests != 1 {
		t.Fatalf("no-op must not PATCH; requests=%d", cap.requests)
	}
	if strings.TrimSpace(out) != "true" {
		t.Fatalf("--jq .data.noop = %q, want true", strings.TrimSpace(out))
	}
}

// TestSceneLayerManyNoOpNonDefaultFormat pins that a non-default output format
// (ndjson) renders the no-op envelope without error and preserves the noop marker.
func TestSceneLayerManyNoOpNonDefaultFormat(t *testing.T) {
	body := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--format", "ndjson", "docs", "scene", "element", "layer-many", "d1",
		"--id", "e2", "--position", "front")
	if err != nil {
		t.Fatalf("no-op with --format ndjson must succeed: %v", err)
	}
	if cap.requests != 1 {
		t.Fatalf("no-op must not PATCH; requests=%d", cap.requests)
	}
	line := strings.TrimSpace(out)
	if !json.Valid([]byte(line)) {
		t.Fatalf("ndjson no-op output is not valid JSON: %q", line)
	}
	if !strings.Contains(line, `"noop":true`) {
		t.Fatalf("ndjson no-op output missing noop marker: %q", line)
	}
}

// ---- P1: boundElements owner compatibility & single-owner bound text ----

// TestSceneMultiRejectsMultiOwnerBoundText pins that a bound text referenced by
// boundElements(type=text) from more than one distinct owner is contradictory and
// fails the whole batch before any PATCH — even when the text carries no
// containerId, so neither the containerId side nor the reciprocity check would
// otherwise catch it. The reverse claim (textID -> owner) is tracked and the
// second distinct owner is rejected before any union/PATCH.
func TestSceneMultiRejectsMultiOwnerBoundText(t *testing.T) {
	// t1 has no containerId; two rectangles c1 and c2 each list it as bound text.
	body := `{"elements":[
		{"id":"c1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"text","id":"t1"}]},
		{"id":"c2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"text","id":"t1"}]},
		{"id":"t1","type":"text","index":"a2","version":1,"versionNonce":1,"isDeleted":false,"fontSize":20}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("multi-owner bound text must not PATCH: %s", r.Method)
		}
		io.WriteString(w, body)
	})
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "transform-many", "d1", "--id", "c1", "--x", "99"); err == nil {
		t.Fatal("expected rejection: two owners bind the same text")
	}
	if cap.requests != 1 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

// TestSceneMultiRejectsIncompatibleBoundElementsOwner pins boundElements owner
// compatibility, validated before any union/PATCH:
//   - a type=text entry is only allowed on a valid text container (rectangle,
//     ellipse, diamond, arrow) — a line owning a text is rejected;
//   - a type=arrow entry is only allowed on a valid arrow-binding target — a line
//     owning an arrow is rejected;
//   - a bound label text (non-null containerId) may not own an arrow ref.
func TestSceneMultiRejectsIncompatibleBoundElementsOwner(t *testing.T) {
	cases := []struct {
		name string
		body string
		sel  string
	}{
		{"line owns text", `{"elements":[
			{"id":"l1","type":"line","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"points":[[0,0],[5,5]],"boundElements":[{"type":"text","id":"t1"}]},
			{"id":"t1","type":"text","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"fontSize":20}
		],"baseVersion":"BV"}`, "t1"},
		{"line owns arrow", `{"elements":[
			{"id":"l1","type":"line","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"points":[[0,0],[5,5]],"boundElements":[{"type":"arrow","id":"ar1"}]},
			{"id":"ar1","type":"arrow","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"points":[[0,0],[5,5]]}
		],"baseVersion":"BV"}`, "ar1"},
		{"freedraw owns arrow", `{"elements":[
			{"id":"fd1","type":"freedraw","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"arrow","id":"ar1"}]},
			{"id":"ar1","type":"arrow","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"points":[[0,0],[5,5]]}
		],"baseVersion":"BV"}`, "ar1"},
		{"bound label text owns arrow", `{"elements":[
			{"id":"c1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false},
			{"id":"t1","type":"text","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"containerId":"c1","fontSize":20,"boundElements":[{"type":"arrow","id":"ar1"}]},
			{"id":"ar1","type":"arrow","index":"a2","version":1,"versionNonce":1,"isDeleted":false,"points":[[0,0],[5,5]]}
		],"baseVersion":"BV"}`, "ar1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.body
			_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Fatalf("incompatible boundElements owner must not PATCH: %s", r.Method)
				}
				io.WriteString(w, b)
			})
			if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "transform-many", "d1", "--id", tc.sel, "--x", "99"); err == nil {
				t.Fatalf("expected rejection for %q", tc.name)
			}
			if cap.requests != 1 {
				t.Fatalf("requests=%d for %q", cap.requests, tc.name)
			}
		})
	}
}

// TestSceneMultiValidBoundElementsOwner pins the legitimate one-sided owner cases
// the owner-compatibility gate must NOT break: a valid text container owning an
// arrow (owner->arrow) and an UNBOUND text owning an arrow are both accepted, and
// in neither case is the arrow unioned into the owner's structural unit — the
// owner stays mutable on its own.
func TestSceneMultiValidBoundElementsOwner(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		// A rectangle (valid text container / bindable target) owns an arrow that
		// carries no endpoint bindings back — a one-sided owner->arrow relation.
		{"rectangle owns arrow", `{"elements":[
			{"id":"r1","type":"rectangle","x":1,"index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"arrow","id":"ar1"}]},
			{"id":"ar1","type":"arrow","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"points":[[0,0],[5,5]]}
		],"baseVersion":"BV"}`},
		// An unbound text (no containerId) is a valid arrow-binding target and may
		// own an arrow ref.
		{"unbound text owns arrow", `{"elements":[
			{"id":"r1","type":"text","x":1,"index":"a0","version":1,"versionNonce":1,"isDeleted":false,"fontSize":20,"boundElements":[{"type":"arrow","id":"ar1"}]},
			{"id":"ar1","type":"arrow","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"points":[[0,0],[5,5]]}
		],"baseVersion":"BV"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, cap := semanticFactory(t, serveScene(t, tc.body))
			out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "transform-many", "d1", "--id", "r1", "--x", "99")
			if err != nil {
				t.Fatalf("valid owner->arrow must let the owner mutate alone: %v", err)
			}
			els := dryRunElements(t, dryRunData(t, out))
			if len(els) != 1 || els["r1"]["x"] != float64(99) {
				t.Fatalf("arrow must not be pulled into the batch (%s): %v", tc.name, els)
			}
		})
	}
}

// ---- P3: non-arrow endpoint bindings are corrupt ----

// TestSceneMultiRejectsNonArrowEndpointBindings pins that startBinding/endBinding
// are arrow-only: any non-arrow live element carrying a non-null endpoint binding
// — a well-formed object, a bare string, or an otherwise malformed value — is
// corrupt and fails the whole batch before any PATCH, even when the element being
// transformed is a well-formed sibling. Arrow owners are unaffected (they run
// through the existing strict endpoint validation).
func TestSceneMultiRejectsNonArrowEndpointBindings(t *testing.T) {
	cases := []struct {
		name    string
		carrier string
	}{
		{"rectangle with object endBinding", `{"id":"x1","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"endBinding":{"elementId":"e1"}}`},
		{"rectangle with object startBinding", `{"id":"x1","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"startBinding":{"elementId":"e1"}}`},
		{"line with object endBinding", `{"id":"x1","type":"line","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"points":[[0,0],[5,5]],"endBinding":{"elementId":"e1"}}`},
		{"line with object startBinding", `{"id":"x1","type":"line","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"points":[[0,0],[5,5]],"startBinding":{"elementId":"e1"}}`},
		{"text with object endBinding", `{"id":"x1","type":"text","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"fontSize":20,"endBinding":{"elementId":"e1"}}`},
		{"rectangle with malformed string endBinding", `{"id":"x1","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"endBinding":"e1"}`},
		{"rectangle with malformed number startBinding", `{"id":"x1","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"startBinding":7}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"elements":[
				{"id":"e1","type":"rectangle","x":1,"index":"a0","version":1,"versionNonce":1,"isDeleted":false},
				` + tc.carrier + `
			],"baseVersion":"BV"}`
			_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Fatalf("non-arrow endpoint binding must not PATCH: %s", r.Method)
				}
				io.WriteString(w, body)
			})
			if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "transform-many", "d1", "--id", "e1", "--x", "99"); err == nil {
				t.Fatalf("expected rejection for %q", tc.name)
			}
			if cap.requests != 1 {
				t.Fatalf("requests=%d for %q", cap.requests, tc.name)
			}
		})
	}
}

// TestSceneMultiAllowsNullNonArrowEndpointBindings pins the other side of the
// contract: an explicit null startBinding/endBinding on a non-arrow element is
// benign (absent-equivalent) and must NOT block a valid batch.
func TestSceneMultiAllowsNullNonArrowEndpointBindings(t *testing.T) {
	body := `{"elements":[
		{"id":"e1","type":"rectangle","x":1,"index":"a0","version":1,"versionNonce":1,"isDeleted":false,"startBinding":null,"endBinding":null}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "transform-many", "d1", "--id", "e1", "--x", "99")
	if err != nil {
		t.Fatalf("null endpoint bindings on a non-arrow must be tolerated: %v", err)
	}
	els := dryRunElements(t, dryRunData(t, out))
	if len(els) != 1 || els["e1"]["x"] != float64(99) {
		t.Fatalf("expected e1 transformed alone: %v", els)
	}
}

// ---- P3: at most two owners may claim an arrow ----

// TestSceneMultiRejectsThreeArrowOwners pins that a live arrow has two endpoints,
// so a third distinct owner listing the same arrow in boundElements is physically
// impossible and fails the whole batch before any PATCH — even when the arrow
// carries no endpoint bindings of its own (the claim set is rejected regardless of
// whether the arrow names anyone back).
func TestSceneMultiRejectsThreeArrowOwners(t *testing.T) {
	body := `{"elements":[
		{"id":"r1","type":"rectangle","x":1,"index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"arrow","id":"ar1"}]},
		{"id":"r2","type":"rectangle","x":2,"index":"a1","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"arrow","id":"ar1"}]},
		{"id":"r3","type":"rectangle","x":3,"index":"a2","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"arrow","id":"ar1"}]},
		{"id":"ar1","type":"arrow","index":"a3","version":1,"versionNonce":1,"isDeleted":false,"points":[[0,0],[5,5]]}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("three owners claiming one arrow must not PATCH: %s", r.Method)
		}
		io.WriteString(w, body)
	})
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "transform-many", "d1", "--id", "r1", "--x", "99"); err == nil {
		t.Fatal("expected rejection: three owners claim the same arrow")
	}
	if cap.requests != 1 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

// TestSceneMultiAllowsTwoArrowOwnersOneSided pins the valid boundary the two-owner
// cap must NOT break: two distinct owners each listing the same arrow (a
// one-sided claim — the arrow carries no endpoint bindings back) is accepted, and
// the arrow is never unioned into either owner's structural unit, so an owner
// stays mutable on its own.
func TestSceneMultiAllowsTwoArrowOwnersOneSided(t *testing.T) {
	body := `{"elements":[
		{"id":"r1","type":"rectangle","x":1,"index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"arrow","id":"ar1"}]},
		{"id":"r2","type":"rectangle","x":2,"index":"a1","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"arrow","id":"ar1"}]},
		{"id":"ar1","type":"arrow","index":"a2","version":1,"versionNonce":1,"isDeleted":false,"points":[[0,0],[5,5]]}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "transform-many", "d1", "--id", "r1", "--x", "99")
	if err != nil {
		t.Fatalf("two one-sided arrow owners must be valid and mutable alone: %v", err)
	}
	els := dryRunElements(t, dryRunData(t, out))
	if len(els) != 1 || els["r1"]["x"] != float64(99) {
		t.Fatalf("arrow must not be pulled into the batch: %v", els)
	}
}

// TestSceneMultiAllowsTwoArrowOwnersReciprocal pins that two owners claiming an
// arrow whose endpoints bind back to exactly those two owners (the fully
// reciprocal two-endpoint case) is accepted: neither owner is unioned with the
// arrow, and each may still mutate on its own.
func TestSceneMultiAllowsTwoArrowOwnersReciprocal(t *testing.T) {
	body := `{"elements":[
		{"id":"r1","type":"rectangle","x":1,"index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"arrow","id":"ar1"}]},
		{"id":"r2","type":"rectangle","x":2,"index":"a1","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"arrow","id":"ar1"}]},
		{"id":"ar1","type":"arrow","index":"a2","version":1,"versionNonce":1,"isDeleted":false,"points":[[0,0],[5,5]],"startBinding":{"elementId":"r1"},"endBinding":{"elementId":"r2"}}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "transform-many", "d1", "--id", "r1", "--x", "99")
	if err != nil {
		t.Fatalf("reciprocal two-owner arrow must be valid and mutable alone: %v", err)
	}
	els := dryRunElements(t, dryRunData(t, out))
	if len(els) != 1 || els["r1"]["x"] != float64(99) {
		t.Fatalf("arrow must not be pulled into the batch: %v", els)
	}
}
