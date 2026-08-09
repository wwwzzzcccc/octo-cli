package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/Mininglamp-OSS/octo-cli/internal/fracindex"
)

// dryRunElements returns every element in a dry-run PATCH body, keyed by id, and
// asserts the batch is a single PATCH under If-Match (via dryRunData).
func dryRunElements(t *testing.T, data map[string]any) map[string]map[string]any {
	t.Helper()
	body, ok := data["body"].(map[string]any)
	if !ok {
		t.Fatalf("body not object: %v", data["body"])
	}
	els, ok := body["elements"].([]any)
	if !ok {
		t.Fatalf("elements not an array: %v", body["elements"])
	}
	out := make(map[string]map[string]any, len(els))
	for _, raw := range els {
		e, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("element not object: %v", raw)
		}
		id, _ := e["id"].(string)
		out[id] = e
	}
	return out
}

func groupIDsOf(t *testing.T, e map[string]any) []string {
	t.Helper()
	raw, ok := e["groupIds"].([]any)
	if !ok {
		t.Fatalf("groupIds not an array: %v", e["groupIds"])
	}
	out := make([]string, len(raw))
	for i, v := range raw {
		out[i] = v.(string)
	}
	return out
}

// ---- group: atomic body, nested groups, generated + explicit id ----

// TestSceneGroupAtomicBodyPreservesNestedAndUnknown pins the whole contract of
// `group`: one PATCH carrying every selected element, the new group id appended
// (outermost) without deleting a pre-existing nested group, unknown fields
// preserved, and every element's version bumped.
func TestSceneGroupAtomicBodyPreservesNestedAndUnknown(t *testing.T) {
	body := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a0","version":5,"versionNonce":9,"isDeleted":false,"groupIds":["inner"],"futureField":{"x":1}},
		{"id":"e2","type":"ellipse","index":"a1","version":2,"versionNonce":3,"isDeleted":false}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "group", "d1",
		"--id", "e1", "--id", "e2", "--group-id", "G")
	if err != nil {
		t.Fatal(err)
	}
	els := dryRunElements(t, dryRunData(t, out))
	if len(els) != 2 {
		t.Fatalf("expected 2 elements in body, got %d: %v", len(els), els)
	}
	if got := groupIDsOf(t, els["e1"]); len(got) != 2 || got[0] != "inner" || got[1] != "G" {
		t.Fatalf("e1 groupIds=%v (nested group must be preserved, G appended outermost)", got)
	}
	if got := groupIDsOf(t, els["e2"]); len(got) != 1 || got[0] != "G" {
		t.Fatalf("e2 groupIds=%v", got)
	}
	if els["e1"]["futureField"] == nil {
		t.Fatalf("unknown field dropped: %v", els["e1"])
	}
	if els["e1"]["version"] != float64(6) || els["e2"]["version"] != float64(3) {
		t.Fatalf("versions not bumped: e1=%v e2=%v", els["e1"]["version"], els["e2"]["version"])
	}
	if els["e1"]["versionNonce"] == float64(9) || els["e2"]["versionNonce"] == float64(3) {
		t.Fatalf("versionNonce not replaced")
	}
}

func TestSceneGroupGeneratesUniqueGroupID(t *testing.T) {
	// No --group-id: the CLI mints one that collides with no element id nor any
	// existing group id in the scene.
	body := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"groupIds":["existing"]},
		{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "group", "d1", "--id", "e1", "--id", "e2")
	if err != nil {
		t.Fatal(err)
	}
	els := dryRunElements(t, dryRunData(t, out))
	g1, g2 := groupIDsOf(t, els["e1"]), groupIDsOf(t, els["e2"])
	gen := g2[len(g2)-1]
	if gen == "existing" || gen == "e1" || gen == "e2" || gen == "" {
		t.Fatalf("generated group id collides or empty: %q", gen)
	}
	if g1[len(g1)-1] != gen || g1[0] != "existing" {
		t.Fatalf("e1 must keep 'existing' and share the generated id: %v", g1)
	}
}

func TestSceneGroupRejectsBadInputBeforeGet(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("must reject before GET") })
	for _, args := range [][]string{
		{"docs", "scene", "element", "group", "d1", "--id", "e1"},                                       // < 2
		{"docs", "scene", "element", "group", "d1", "--id", "e1", "--id", "e1"},                         // duplicate --id
		{"docs", "scene", "element", "group", "d1", "--id", "e1", "--id", "e2", "--group-id", "bad id"}, // invalid charset
		{"docs", "scene", "element", "group", "d1", "--id", "e1", "--id", "e2", "--group-id", ""},       // empty
	} {
		if _, _, err := execRoot(t, cap.f, args...); err == nil {
			t.Fatalf("expected pre-GET rejection: %v", args)
		}
	}
	if cap.requests != 0 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

func TestSceneGroupRejectsReservedAndDuplicateGroupID(t *testing.T) {
	// reserved: --group-id equals an existing element id.
	reserved := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("must not PATCH: %s", r.Method)
		}
		io.WriteString(w, reserved)
	})
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "group", "d1", "--id", "e1", "--id", "e2", "--group-id", "e1"); err == nil {
		t.Fatal("expected reserved (element-id collision) rejection")
	}
	if cap.requests != 1 {
		t.Fatalf("requests=%d", cap.requests)
	}

	// duplicate: --group-id already applied to a selected element.
	dup := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"groupIds":["G"]},
		{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	_, cap2 := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("must not PATCH: %s", r.Method)
		}
		io.WriteString(w, dup)
	})
	if _, _, err := execRoot(t, cap2.f, "docs", "scene", "element", "group", "d1", "--id", "e1", "--id", "e2", "--group-id", "G"); err == nil {
		t.Fatal("expected duplicate group-id rejection")
	}
	if cap2.requests != 1 {
		t.Fatalf("requests=%d", cap2.requests)
	}
}

func TestSceneGroupRejectsTombstoneAndMissingAllOrNothing(t *testing.T) {
	body := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":true}
	],"baseVersion":"BV"}`
	for _, args := range [][]string{
		{"docs", "scene", "element", "group", "d1", "--id", "e1", "--id", "e2"},   // e2 tombstoned
		{"docs", "scene", "element", "group", "d1", "--id", "e1", "--id", "gone"}, // missing
	} {
		_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Fatalf("must not PATCH on all-or-nothing failure: %s", r.Method)
			}
			io.WriteString(w, body)
		})
		if _, _, err := execRoot(t, cap.f, args...); err == nil {
			t.Fatalf("expected all-or-nothing rejection: %v", args)
		} else if cap.requests != 1 {
			t.Fatalf("requests=%d for %v", cap.requests, args)
		}
	}
}

func TestSceneGroupRejectsMalformedGroupIds(t *testing.T) {
	body := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"groupIds":[123]},
		{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("must not PATCH a malformed scene: %s", r.Method)
		}
		io.WriteString(w, body)
	})
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "group", "d1", "--id", "e1", "--id", "e2"); err == nil {
		t.Fatal("expected malformed groupIds rejection")
	}
	if cap.requests != 1 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

// ---- ungroup: common group, ambiguity, explicit id, nesting preserved ----

func TestSceneUngroupRemovesSingleCommonGroup(t *testing.T) {
	// e1 in {inner, shared}, e2 in {other, shared}: the only common group is
	// "shared". It is removed; each element's other (nested) group survives.
	body := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a0","version":5,"versionNonce":9,"isDeleted":false,"groupIds":["inner","shared"]},
		{"id":"e2","type":"rectangle","index":"a1","version":5,"versionNonce":9,"isDeleted":false,"groupIds":["other","shared"]}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "ungroup", "d1", "--id", "e1", "--id", "e2")
	if err != nil {
		t.Fatal(err)
	}
	els := dryRunElements(t, dryRunData(t, out))
	if got := groupIDsOf(t, els["e1"]); len(got) != 1 || got[0] != "inner" {
		t.Fatalf("e1 groupIds=%v (nested 'inner' must survive)", got)
	}
	if got := groupIDsOf(t, els["e2"]); len(got) != 1 || got[0] != "other" {
		t.Fatalf("e2 groupIds=%v (nested 'other' must survive)", got)
	}
}

func TestSceneUngroupExplicitGroupIDPreservesOthers(t *testing.T) {
	body := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"groupIds":["a","b","c"]}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "ungroup", "d1", "--id", "e1", "--group-id", "b")
	if err != nil {
		t.Fatal(err)
	}
	els := dryRunElements(t, dryRunData(t, out))
	if got := groupIDsOf(t, els["e1"]); len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("e1 groupIds=%v (only 'b' removed, order preserved)", got)
	}
}

func TestSceneUngroupNoCommonAndNonMemberFailLoud(t *testing.T) {
	// No common group.
	disjoint := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"groupIds":["x"]},
		{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"groupIds":["y"]}
	],"baseVersion":"BV"}`
	// Explicit group-id not present on every element.
	notMember := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"groupIds":["z"]},
		{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	for _, tc := range []struct {
		body string
		args []string
	}{
		{disjoint, []string{"docs", "scene", "element", "ungroup", "d1", "--id", "e1", "--id", "e2"}},
		{notMember, []string{"docs", "scene", "element", "ungroup", "d1", "--id", "e1", "--id", "e2", "--group-id", "z"}},
	} {
		b := tc.body
		_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Fatalf("must not PATCH: %s", r.Method)
			}
			io.WriteString(w, b)
		})
		if _, _, err := execRoot(t, cap.f, tc.args...); err == nil {
			t.Fatalf("expected fail-loud rejection: %v", tc.args)
		} else if cap.requests != 1 {
			t.Fatalf("requests=%d for %v", cap.requests, tc.args)
		}
	}
}

// TestSceneUngroupDefaultRemovesOutermostCommon pins requirement (3): with no
// --group-id, a selection sharing more than one common group is NOT an ambiguity
// error — the outermost common group (the last in the innermost→outermost
// groupIds order) is removed, and every inner common group survives.
func TestSceneUngroupDefaultRemovesOutermostCommon(t *testing.T) {
	body := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"groupIds":["inner","outer"]},
		{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"groupIds":["inner","outer"]}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "ungroup", "d1", "--id", "e1", "--id", "e2")
	if err != nil {
		t.Fatal(err)
	}
	els := dryRunElements(t, dryRunData(t, out))
	for _, id := range []string{"e1", "e2"} {
		if got := groupIDsOf(t, els[id]); len(got) != 1 || got[0] != "inner" {
			t.Fatalf("%s groupIds=%v (outermost 'outer' removed, 'inner' must survive)", id, got)
		}
	}
}

// ---- transform-many / style-many: applied to all, single atomic PATCH ----

func TestTransformManyScaleTextSelectionAndBoundPair(t *testing.T) {
	cases := []struct {
		name string
		body string
		ids  []string
	}{
		{
			name: "independent text selection",
			body: `{"elements":[{"id":"t1","type":"text","angle":0,"x":10,"y":20,"width":100,"height":40,"baseline":16,"fontSize":20,"lineHeight":1.25,"index":"a0","version":1,"versionNonce":1,"isDeleted":false},{"id":"t2","type":"text","angle":0,"x":210,"y":120,"width":80,"height":30,"baseline":12,"fontSize":16,"lineHeight":1.4,"index":"a1","version":1,"versionNonce":1,"isDeleted":false}],"baseVersion":"BV"}`,
			ids:  []string{"t1", "t2"},
		},
		{
			name: "container and bound text",
			body: `{"elements":[{"id":"c1","type":"rectangle","angle":0,"x":10,"y":20,"width":200,"height":100,"boundElements":[{"id":"t1","type":"text"}],"index":"a0","version":1,"versionNonce":1,"isDeleted":false},{"id":"t1","type":"text","angle":0,"x":40,"y":50,"width":100,"height":40,"baseline":16,"fontSize":20,"lineHeight":1.25,"containerId":"c1","autoResize":false,"index":"a1","version":1,"versionNonce":1,"isDeleted":false}],"baseVersion":"BV"}`,
			ids:  []string{"c1", "t1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, cap := semanticFactory(t, serveScene(t, tc.body))
			args := []string{"--dry-run", "docs", "scene", "element", "transform-many", "d1"}
			for _, id := range tc.ids {
				args = append(args, "--id", id)
			}
			args = append(args, "--scale", "2")
			out, _, err := execRoot(t, cap.f, args...)
			if err != nil {
				t.Fatal(err)
			}
			data := dryRunData(t, out)
			elements := data["body"].(map[string]any)["elements"].([]any)
			for _, raw := range elements {
				e := raw.(map[string]any)
				if e["type"] == "text" {
					wantFont, wantBaseline, wantLineHeight := float64(40), float64(32), float64(1.25)
					if e["id"] == "t2" {
						wantFont, wantBaseline, wantLineHeight = 32, 24, 1.4
					}
					if e["fontSize"] != wantFont || e["baseline"] != wantBaseline || e["lineHeight"] != wantLineHeight {
						t.Fatalf("scaled text=%v", e)
					}
				}
			}
			if len(elements) == 2 {
				second := elements[1].(map[string]any)
				if tc.name == "independent text selection" && (second["x"] != float64(410) || second["y"] != float64(220)) {
					t.Errorf("whole-selection position=%v,%v", second["x"], second["y"])
				}
			}
		})
	}
}

func TestSceneTransformManyAppliesToAll(t *testing.T) {
	body := `{"elements":[
		{"id":"e1","type":"rectangle","x":1,"y":2,"width":10,"height":20,"angle":0,"index":"a0","version":5,"versionNonce":9,"isDeleted":false},
		{"id":"e2","type":"rectangle","x":3,"y":4,"width":10,"height":20,"angle":0,"index":"a1","version":2,"versionNonce":3,"isDeleted":false}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "transform-many", "d1",
		"--id", "e1", "--id", "e2", "--x", "50", "--width", "300")
	if err != nil {
		t.Fatal(err)
	}
	els := dryRunElements(t, dryRunData(t, out))
	if len(els) != 2 {
		t.Fatalf("expected 2 elements, got %d", len(els))
	}
	for _, id := range []string{"e1", "e2"} {
		e := els[id]
		if e["x"] != float64(50) || e["width"] != float64(300) {
			t.Fatalf("%s not transformed: %v", id, e)
		}
	}
	if els["e1"]["version"] != float64(6) || els["e2"]["version"] != float64(3) {
		t.Fatalf("versions not bumped: %v %v", els["e1"]["version"], els["e2"]["version"])
	}
	// y is untouched on both.
	if els["e1"]["y"] != float64(2) || els["e2"]["y"] != float64(4) {
		t.Fatalf("y should be preserved: %v %v", els["e1"]["y"], els["e2"]["y"])
	}
}

func TestSceneStyleManyAppliesToAll(t *testing.T) {
	body := `{"elements":[
		{"id":"e1","type":"rectangle","strokeColor":"#000","opacity":100,"index":"a0","version":5,"versionNonce":9,"isDeleted":false},
		{"id":"e2","type":"ellipse","strokeColor":"#000","opacity":100,"index":"a1","version":1,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "style-many", "d1",
		"--id", "e1", "--id", "e2", "--stroke-color", "#1971c2", "--opacity", "90")
	if err != nil {
		t.Fatal(err)
	}
	els := dryRunElements(t, dryRunData(t, out))
	for _, id := range []string{"e1", "e2"} {
		e := els[id]
		if e["strokeColor"] != "#1971c2" || e["opacity"] != float64(90) {
			t.Fatalf("%s not styled: %v", id, e)
		}
	}
}

func TestSceneStyleManyTypeGatingIsAllOrNothing(t *testing.T) {
	// A text-only flag against a heterogeneous selection fails loud (the rectangle
	// rejects it), and nothing is PATCHed.
	body := `{"elements":[
		{"id":"e1","type":"text","text":"hi","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"fontSize":20},
		{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("must not PATCH: %s", r.Method)
		}
		io.WriteString(w, body)
	})
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "style-many", "d1", "--id", "e1", "--id", "e2", "--font-family", "times-new-roman"); err == nil {
		t.Fatal("expected text-only flag rejection on rectangle member")
	}
	if cap.requests != 1 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

// ---- layer-many: stable relative order, contiguous block, anchor/dup guards ----

func TestSceneLayerManyFrontPreservesRelativeOrder(t *testing.T) {
	// Move e1 (a0) and e3 (a2) to the front; e2 (a1) and e4 (a3) stay. The block
	// must land above every remaining element and keep e1 below e3.
	body := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e3","type":"rectangle","index":"a2","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e4","type":"rectangle","index":"a3","version":1,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "layer-many", "d1",
		"--id", "e3", "--id", "e1", "--position", "front")
	if err != nil {
		t.Fatal(err)
	}
	els := dryRunElements(t, dryRunData(t, out))
	i1 := els["e1"]["index"].(string)
	i3 := els["e3"]["index"].(string)
	if i1 <= "a3" || i3 <= "a3" {
		t.Fatalf("block not above remaining max a3: e1=%q e3=%q", i1, i3)
	}
	if !(i1 < i3) {
		t.Fatalf("relative order not preserved (e1 must stay below e3): e1=%q e3=%q", i1, i3)
	}
}

func TestSceneLayerManyBeforeAnchor(t *testing.T) {
	body := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e3","type":"rectangle","index":"a2","version":1,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "layer-many", "d1",
		"--id", "e1", "--position", "before", "--relative-to", "e3")
	if err != nil {
		t.Fatal(err)
	}
	els := dryRunElements(t, dryRunData(t, out))
	i1 := els["e1"]["index"].(string)
	if !(i1 > "a1" && i1 < "a2") {
		t.Fatalf("e1 not placed between e2 (a1) and anchor e3 (a2): %q", i1)
	}
}

// reservedSmallestIndex is the fractional-index lower bound the library never
// emits as a real key; a generated index equal to it is a bug.
const reservedSmallestIndex = "A00000000000000000000000000"

// TestSceneLayerManyBackAtMinimumBandSingle pins the lower-bound edge for a
// single-element back move: when the remaining minimum index is "A…01" (the
// immediate successor of the reserved smallestInteger), the moved element must
// receive a valid canonical index strictly below it — never the reserved
// smallestInteger.
func TestSceneLayerManyBackAtMinimumBandSingle(t *testing.T) {
	body := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a5","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e2","type":"rectangle","index":"A00000000000000000000000001","version":1,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "layer-many", "d1",
		"--id", "e1", "--position", "back")
	if err != nil {
		t.Fatal(err)
	}
	els := dryRunElements(t, dryRunData(t, out))
	i1 := els["e1"]["index"].(string)
	if err := fracindex.ValidateOrderKey(i1); err != nil {
		t.Fatalf("e1 index %q is not canonical: %v", i1, err)
	}
	if i1 == reservedSmallestIndex {
		t.Fatalf("e1 index is the reserved smallestInteger %q", i1)
	}
	if !(i1 < "A00000000000000000000000001") {
		t.Fatalf("e1 index %q not below remaining minimum A…01", i1)
	}
}

// TestSceneLayerManyBackAtMinimumBandMultiple pins the same floor edge for a
// multi-element back move: the block lands below the remaining minimum "A…01",
// every generated index is canonical and strictly increasing, and none is the
// reserved smallestInteger.
func TestSceneLayerManyBackAtMinimumBandMultiple(t *testing.T) {
	body := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a5","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e2","type":"rectangle","index":"a6","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e3","type":"rectangle","index":"A00000000000000000000000001","version":1,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "layer-many", "d1",
		"--id", "e1", "--id", "e2", "--position", "back")
	if err != nil {
		t.Fatal(err)
	}
	els := dryRunElements(t, dryRunData(t, out))
	i1 := els["e1"]["index"].(string)
	i2 := els["e2"]["index"].(string)
	for _, idx := range []string{i1, i2} {
		if err := fracindex.ValidateOrderKey(idx); err != nil {
			t.Fatalf("index %q is not canonical: %v", idx, err)
		}
		if idx == reservedSmallestIndex {
			t.Fatalf("index is the reserved smallestInteger %q", idx)
		}
		if !(idx < "A00000000000000000000000001") {
			t.Fatalf("index %q not below remaining minimum A…01", idx)
		}
	}
	// e1 kept its lower relative z-order (a5 < a6), so it stays below e2.
	if !(i1 < i2) {
		t.Fatalf("relative order not preserved / not strictly increasing: e1=%q e2=%q", i1, i2)
	}
}

func TestSceneLayerManyRejectsAnchorInSelectionBeforeGet(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("must reject before GET") })
	for _, pos := range []string{"before", "after"} {
		if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "layer-many", "d1",
			"--id", "e1", "--id", "e2", "--position", pos, "--relative-to", "e1"); err == nil {
			t.Fatalf("expected anchor-in-selection rejection for %s", pos)
		}
	}
	if cap.requests != 0 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

func TestSceneLayerManyRejectsDuplicateIndex(t *testing.T) {
	// Two live elements share an index → corrupt z-order → refuse without PATCH.
	body := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e2","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e3","type":"rectangle","index":"a2","version":1,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("must not PATCH a corrupt z-order: %s", r.Method)
		}
		io.WriteString(w, body)
	})
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "layer-many", "d1", "--id", "e3", "--position", "back"); err == nil {
		t.Fatal("expected duplicate-index rejection")
	}
	if cap.requests != 1 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

// ---- dry-run reads but never patches; PATCH response passthrough ----

func TestSceneMultiDryRunReadsButDoesNotPatch(t *testing.T) {
	body := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	for _, args := range [][]string{
		{"--dry-run", "docs", "scene", "element", "group", "d1", "--id", "e1", "--id", "e2"},
		{"--dry-run", "docs", "scene", "element", "transform-many", "d1", "--id", "e1", "--id", "e2", "--x", "9"},
		{"--dry-run", "docs", "scene", "element", "layer-many", "d1", "--id", "e2", "--position", "back"},
	} {
		_, cap := semanticFactory(t, serveScene(t, body))
		out, _, err := execRoot(t, cap.f, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if cap.requests != 1 {
			t.Fatalf("requests=%d for %v (dry-run must GET once, never PATCH)", cap.requests, args)
		}
		if !json.Valid([]byte(out)) {
			t.Fatalf("output not valid JSON: %s", out)
		}
	}
}

// TestSceneMultiPatchResponsePassthrough pins that the backend's PATCH response
// body flows through to the success envelope unchanged.
func TestSceneMultiPatchResponsePassthrough(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"elements":[{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false},{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false}],"baseVersion":"BV"}`)
			return
		}
		if r.Method != http.MethodPatch {
			t.Fatalf("write path must be PATCH, got %s", r.Method)
		}
		if r.Header.Get("If-Match") != "BV" {
			t.Fatalf("If-Match=%q", r.Header.Get("If-Match"))
		}
		io.WriteString(w, `{"applied":true,"newBaseVersion":"BV2"}`)
	})
	out, _, err := execRoot(t, cap.f, "docs", "scene", "element", "group", "d1", "--id", "e1", "--id", "e2")
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope: %v\n%s", err, out)
	}
	data, _ := env["data"].(map[string]any)
	if data["applied"] != true || data["newBaseVersion"] != "BV2" {
		t.Fatalf("PATCH response not passed through: %v", data)
	}
	if cap.requests != 2 {
		t.Fatalf("expected GET+PATCH, requests=%d", cap.requests)
	}
}

// TestSceneMultiStalePatchSurfaced pins that a stale-base-version PATCH failure
// is surfaced as an error (not swallowed): GET succeeds, PATCH returns 412.
func TestSceneMultiStalePatchSurfaced(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"elements":[{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false},{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false}],"baseVersion":"BV"}`)
			return
		}
		if r.Method != http.MethodPatch {
			t.Fatalf("write path must be PATCH, got %s", r.Method)
		}
		if r.Header.Get("If-Match") != "BV" {
			t.Fatalf("If-Match=%q", r.Header.Get("If-Match"))
		}
		w.WriteHeader(http.StatusPreconditionFailed)
		io.WriteString(w, `{"error":{"type":"conflict","code":"base_version_stale","message":"stale"}}`)
	})
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "group", "d1", "--id", "e1", "--id", "e2"); err == nil {
		t.Fatal("expected stale PATCH to surface an error")
	}
	if cap.requests != 2 {
		t.Fatalf("expected GET+PATCH, requests=%d", cap.requests)
	}
}

// TestSceneMultiNoOpRejected pins that a batch that would change nothing is
// rejected without a PATCH. Ungrouping a group nobody shares can't happen (it
// fails earlier), so use transform-many setting values already in place.
func TestSceneMultiNoOpRejected(t *testing.T) {
	body := `{"elements":[
		{"id":"e1","type":"rectangle","x":5,"y":2,"width":10,"height":20,"angle":0,"index":"a0","version":1,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("no-op must not PATCH: %s", r.Method)
		}
		io.WriteString(w, body)
	})
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "transform-many", "d1", "--id", "e1", "--x", "5"); err == nil {
		t.Fatal("expected no-op rejection")
	}
	if cap.requests != 1 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

// ---- fail-closed structural completeness (requirement 1) ----

// TestSceneMultiFailsClosedOnPartialBoundText pins that selecting only part of a
// container/bound-text unit fails before any PATCH: the container and its bound
// text form one unit, and neither half may be mutated without the other.
func TestSceneMultiFailsClosedOnPartialBoundText(t *testing.T) {
	body := `{"elements":[
		{"id":"c1","type":"rectangle","x":1,"y":1,"width":10,"height":10,"index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"id":"t1","type":"text"}]},
		{"id":"t1","type":"text","x":2,"y":2,"fontSize":20,"index":"a1","version":1,"versionNonce":1,"isDeleted":false,"containerId":"c1","autoResize":true,"boundElements":null}
	],"baseVersion":"BV"}`
	for _, sel := range [][]string{{"c1"}, {"t1"}} {
		_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Fatalf("partial bound-text unit must not PATCH: %s", r.Method)
			}
			io.WriteString(w, body)
		})
		args := []string{"docs", "scene", "element", "transform-many", "d1", "--x", "99"}
		for _, id := range sel {
			args = append(args, "--id", id)
		}
		if _, _, err := execRoot(t, cap.f, args...); err == nil {
			t.Fatalf("expected fail-closed for partial bound-text selection %v", sel)
		}
		if cap.requests != 1 {
			t.Fatalf("requests=%d for %v", cap.requests, sel)
		}
	}
}

// TestSceneMultiFullBoundTextUnitSucceeds pins requirement (1)'s valid case: when
// the whole container/bound-text unit is selected and every member materially
// changes, the batch applies as a single PATCH.
func TestSceneMultiFullBoundTextUnitSucceeds(t *testing.T) {
	body := `{"elements":[
		{"id":"c1","type":"rectangle","x":1,"y":1,"width":10,"height":10,"index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"id":"t1","type":"text"}]},
		{"id":"t1","type":"text","x":2,"y":2,"fontSize":20,"index":"a1","version":1,"versionNonce":1,"isDeleted":false,"containerId":"c1","autoResize":true,"boundElements":null}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "transform-many", "d1", "--id", "c1", "--id", "t1", "--x", "99")
	if err != nil {
		t.Fatal(err)
	}
	els := dryRunElements(t, dryRunData(t, out))
	if len(els) != 2 || els["c1"]["x"] != float64(99) || els["t1"]["x"] != float64(99) {
		t.Fatalf("full unit not transformed atomically: %v", els)
	}
	// The binding fields must survive untouched.
	if els["t1"]["containerId"] != "c1" {
		t.Fatalf("containerId dropped: %v", els["t1"])
	}
}

// TestSceneMultiFailsClosedOnPartialFrame pins that selecting a frame child
// without its frame element and siblings fails closed.
func TestSceneMultiFailsClosedOnPartialFrame(t *testing.T) {
	body := `{"elements":[
		{"id":"f1","type":"frame","x":0,"y":0,"width":100,"height":100,"index":"a0","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"a1","type":"rectangle","x":1,"y":1,"width":10,"height":10,"index":"a1","version":1,"versionNonce":1,"isDeleted":false,"frameId":"f1"},
		{"id":"a2","type":"rectangle","x":2,"y":2,"width":10,"height":10,"index":"a2","version":1,"versionNonce":1,"isDeleted":false,"frameId":"f1"}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("partial frame unit must not PATCH: %s", r.Method)
		}
		io.WriteString(w, body)
	})
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "style-many", "d1", "--id", "a1", "--opacity", "50"); err == nil {
		t.Fatal("expected fail-closed for partial frame selection")
	}
	if cap.requests != 1 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

// TestSceneMultiFailsClosedOnPartialGroup pins that selecting a strict subset of
// an existing live group fails closed — the group may not be silently split.
func TestSceneMultiFailsClosedOnPartialGroup(t *testing.T) {
	body := `{"elements":[
		{"id":"e1","type":"rectangle","x":1,"index":"a0","version":1,"versionNonce":1,"isDeleted":false,"groupIds":["G"]},
		{"id":"e2","type":"rectangle","x":2,"index":"a1","version":1,"versionNonce":1,"isDeleted":false,"groupIds":["G"]},
		{"id":"e3","type":"rectangle","x":3,"index":"a2","version":1,"versionNonce":1,"isDeleted":false,"groupIds":["G"]}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("partial group must not PATCH: %s", r.Method)
		}
		io.WriteString(w, body)
	})
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "transform-many", "d1", "--id", "e1", "--id", "e2", "--x", "99"); err == nil {
		t.Fatal("expected fail-closed for partial group selection (e3 missing)")
	}
	if cap.requests != 1 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

// ---- group-id collision anywhere (requirement 2) ----

// TestSceneGroupRejectsGroupIDInUseAnywhere pins that --group-id is rejected when
// it already occurs in the groupIds of ANY live element — even one outside the
// selection — so grouping never merges the selection into a pre-existing group.
func TestSceneGroupRejectsGroupIDInUseAnywhere(t *testing.T) {
	body := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e3","type":"rectangle","index":"a2","version":1,"versionNonce":1,"isDeleted":false,"groupIds":["G"]}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("must not PATCH: %s", r.Method)
		}
		io.WriteString(w, body)
	})
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "group", "d1", "--id", "e1", "--id", "e2", "--group-id", "G"); err == nil {
		t.Fatal("expected rejection: --group-id already in use by a non-selected live element")
	}
	if cap.requests != 1 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

// ---- every selected element must change; no-op or malformed fails the batch (requirement 4) ----

// TestSceneMultiFailsOnNoOpOrMalformedMember pins that a single no-op member or a
// single malformed member (here an invalid existing version that fails the bump)
// fails the entire batch before any PATCH.
func TestSceneMultiFailsOnNoOpOrMalformedMember(t *testing.T) {
	noop := `{"elements":[
		{"id":"e1","type":"rectangle","x":5,"index":"a0","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e2","type":"rectangle","x":7,"index":"a1","version":1,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	malformed := `{"elements":[
		{"id":"e1","type":"rectangle","x":1,"index":"a0","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e2","type":"rectangle","x":2,"index":"a1","version":0,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	for _, body := range []string{noop, malformed} {
		b := body
		_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Fatalf("no-op/malformed member must not PATCH: %s", r.Method)
			}
			io.WriteString(w, b)
		})
		// e1 x=5 with --x 5 is a no-op for the first body; e2 version 0 fails the
		// bump for the second. Either way the whole batch must fail.
		x := "5"
		if b == malformed {
			x = "9"
		}
		if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "transform-many", "d1", "--id", "e1", "--id", "e2", "--x", x); err == nil {
			t.Fatal("expected whole-batch failure before PATCH")
		}
		if cap.requests != 1 {
			t.Fatalf("requests=%d", cap.requests)
		}
	}
}

// ---- layer-many: structural components move as atomic, contiguous, ordered blocks (requirements 1 & 4) ----

// TestSceneLayerManyFlattensInterleavedGroupsAndFrame is the requirement-4
// regression: two complete groups whose members are interleaved in the source
// z-order — plus a frame unit likewise interleaved — must come out of a front
// move as contiguous, non-interleaved runs. Components are ordered by their
// earliest member's original index (G1 @ a0, G2 @ a1, frame @ a2); within each
// component the original relative index order is preserved. Every generated
// index is canonical and the whole moving run lands above the one element left
// behind.
func TestSceneLayerManyFlattensInterleavedGroupsAndFrame(t *testing.T) {
	// Source z-order: e1(G1) e2(G2) f1(frame) e3(G1) c1(f1) e4(G2) c2(f1) keep.
	body := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"groupIds":["G1"]},
		{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"groupIds":["G2"]},
		{"id":"f1","type":"frame","index":"a2","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e3","type":"rectangle","index":"a3","version":1,"versionNonce":1,"isDeleted":false,"groupIds":["G1"]},
		{"id":"c1","type":"rectangle","index":"a4","version":1,"versionNonce":1,"isDeleted":false,"frameId":"f1"},
		{"id":"e4","type":"rectangle","index":"a5","version":1,"versionNonce":1,"isDeleted":false,"groupIds":["G2"]},
		{"id":"c2","type":"rectangle","index":"a6","version":1,"versionNonce":1,"isDeleted":false,"frameId":"f1"},
		{"id":"keep","type":"rectangle","index":"a7","version":1,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "layer-many", "d1",
		"--id", "e4", "--id", "e1", "--id", "c2", "--id", "e2", "--id", "f1", "--id", "e3", "--id", "c1",
		"--position", "front")
	if err != nil {
		t.Fatal(err)
	}
	els := dryRunElements(t, dryRunData(t, out))
	// Expected flattened z-order: G1 (e1,e3), then G2 (e2,e4), then frame (f1,c1,c2).
	want := []string{"e1", "e3", "e2", "e4", "f1", "c1", "c2"}
	idx := make([]string, len(want))
	for i, id := range want {
		got, ok := els[id]
		if !ok {
			t.Fatalf("moved element %q missing from PATCH body", id)
		}
		idx[i] = got["index"].(string)
		if err := fracindex.ValidateOrderKey(idx[i]); err != nil {
			t.Fatalf("%s index %q is not canonical: %v", id, idx[i], err)
		}
	}
	for i := 1; i < len(idx); i++ {
		if !(idx[i-1] < idx[i]) {
			t.Fatalf("moving run not strictly increasing/contiguous in component order %v: %v", want, idx)
		}
	}
	// The whole run lands above the only element left behind (keep @ a7).
	if !(idx[0] > "a7") {
		t.Fatalf("moving run not above remaining max a7: first=%q", idx[0])
	}
	if _, moved := els["keep"]; moved {
		t.Fatal("keep must not be in the PATCH body (it did not move)")
	}
}

// TestSceneLayerManyTwoInterleavedGroupsBefore repeats the contiguity guarantee
// for a before-anchor move with two interleaved groups (no frame), proving the
// component-atomic ordering is independent of the target position. The block
// lands in the fractional gap between two adjacent low elements, so every
// generated index differs from every original (no member is a no-op).
func TestSceneLayerManyTwoInterleavedGroupsBefore(t *testing.T) {
	// Source: low(a0) anchor(a1) g1a(G1,a3) g2a(G2,a4) g1b(G1,a5) g2b(G2,a6).
	body := `{"elements":[
		{"id":"low","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"anchor","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"g1a","type":"rectangle","index":"a3","version":1,"versionNonce":1,"isDeleted":false,"groupIds":["G1"]},
		{"id":"g2a","type":"rectangle","index":"a4","version":1,"versionNonce":1,"isDeleted":false,"groupIds":["G2"]},
		{"id":"g1b","type":"rectangle","index":"a5","version":1,"versionNonce":1,"isDeleted":false,"groupIds":["G1"]},
		{"id":"g2b","type":"rectangle","index":"a6","version":1,"versionNonce":1,"isDeleted":false,"groupIds":["G2"]}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "layer-many", "d1",
		"--id", "g2b", "--id", "g1a", "--id", "g2a", "--id", "g1b",
		"--position", "before", "--relative-to", "anchor")
	if err != nil {
		t.Fatal(err)
	}
	els := dryRunElements(t, dryRunData(t, out))
	want := []string{"g1a", "g1b", "g2a", "g2b"} // G1 (min a3) contiguous, then G2 (min a4)
	var prev string
	for i, id := range want {
		cur := els[id]["index"].(string)
		if err := fracindex.ValidateOrderKey(cur); err != nil {
			t.Fatalf("%s index %q not canonical: %v", id, cur, err)
		}
		if i > 0 && !(prev < cur) {
			t.Fatalf("run not contiguous in %v: %v after %v", want, cur, prev)
		}
		// Between low (a0) and anchor (a1): strictly inside the fractional gap.
		if !(cur > "a0" && cur < "a1") {
			t.Fatalf("%s index %q not between low a0 and anchor a1", id, cur)
		}
		prev = cur
	}
}

// ---- strict fail-closed structural validation before mutation (requirement 2) ----

// TestSceneMultiRejectsMalformedOrDanglingRefs pins the strict structural gate:
// a malformed frameId/containerId, a dangling ref, an unsupported/invalid
// boundElements entry, or a contradictory reciprocal binding fails the whole
// batch before any PATCH — for any multi command (driven here via transform-many).
func TestSceneMultiRejectsMalformedOrDanglingRefs(t *testing.T) {
	cases := []struct {
		name string
		body string
		ids  []string
	}{
		{"frameId not a string", `{"elements":[
			{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"frameId":123}
		],"baseVersion":"BV"}`, []string{"e1"}},
		{"frameId empty string", `{"elements":[
			{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"frameId":""}
		],"baseVersion":"BV"}`, []string{"e1"}},
		{"frameId dangling", `{"elements":[
			{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"frameId":"gone"}
		],"baseVersion":"BV"}`, []string{"e1"}},
		{"frameId not a frame", `{"elements":[
			{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"frameId":"e2"},
			{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false}
		],"baseVersion":"BV"}`, []string{"e1"}},
		{"frameId references tombstone", `{"elements":[
			{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"frameId":"f1"},
			{"id":"f1","type":"frame","index":"a1","version":1,"versionNonce":1,"isDeleted":true}
		],"baseVersion":"BV"}`, []string{"e1"}},
		{"containerId on non-text", `{"elements":[
			{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"containerId":"c1"},
			{"id":"c1","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false}
		],"baseVersion":"BV"}`, []string{"e1"}},
		{"containerId dangling", `{"elements":[
			{"id":"t1","type":"text","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"containerId":"gone","fontSize":20}
		],"baseVersion":"BV"}`, []string{"t1"}},
		{"container is text", `{"elements":[
			{"id":"t1","type":"text","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"containerId":"t2","fontSize":20},
			{"id":"t2","type":"text","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"fontSize":20}
		],"baseVersion":"BV"}`, []string{"t1"}},
		{"boundElements not array", `{"elements":[
			{"id":"c1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":{"id":"x"}}
		],"baseVersion":"BV"}`, []string{"c1"}},
		{"boundElements entry not object", `{"elements":[
			{"id":"c1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":["t1"]}
		],"baseVersion":"BV"}`, []string{"c1"}},
		{"boundElements empty id", `{"elements":[
			{"id":"c1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"text","id":""}]}
		],"baseVersion":"BV"}`, []string{"c1"}},
		{"boundElements unsupported type", `{"elements":[
			{"id":"c1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"image","id":"x"}]}
		],"baseVersion":"BV"}`, []string{"c1"}},
		{"boundElements text dangling", `{"elements":[
			{"id":"c1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"text","id":"gone"}]}
		],"baseVersion":"BV"}`, []string{"c1"}},
		{"boundElements text wrong target type", `{"elements":[
			{"id":"c1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"text","id":"e2"}]},
			{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false}
		],"baseVersion":"BV"}`, []string{"c1"}},
		{"boundElements arrow wrong target type", `{"elements":[
			{"id":"c1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"arrow","id":"e2"}]},
			{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false}
		],"baseVersion":"BV"}`, []string{"c1"}},
		{"container binds two texts", `{"elements":[
			{"id":"c1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"text","id":"t1"},{"type":"text","id":"t2"}]},
			{"id":"t1","type":"text","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"fontSize":20},
			{"id":"t2","type":"text","index":"a2","version":1,"versionNonce":1,"isDeleted":false,"fontSize":20}
		],"baseVersion":"BV"}`, []string{"c1"}},
		{"contradictory reciprocal binding", `{"elements":[
			{"id":"c1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"text","id":"t2"}]},
			{"id":"t1","type":"text","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"containerId":"c1","fontSize":20},
			{"id":"t2","type":"text","index":"a2","version":1,"versionNonce":1,"isDeleted":false,"fontSize":20}
		],"baseVersion":"BV"}`, []string{"t1"}},
		{"two texts claim same container", `{"elements":[
			{"id":"c1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false},
			{"id":"t1","type":"text","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"containerId":"c1","fontSize":20},
			{"id":"t2","type":"text","index":"a2","version":1,"versionNonce":1,"isDeleted":false,"containerId":"c1","fontSize":20}
		],"baseVersion":"BV"}`, []string{"t1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.body
			_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Fatalf("malformed structure must not PATCH: %s", r.Method)
				}
				io.WriteString(w, b)
			})
			args := []string{"docs", "scene", "element", "transform-many", "d1", "--x", "99"}
			for _, id := range tc.ids {
				args = append(args, "--id", id)
			}
			if _, _, err := execRoot(t, cap.f, args...); err == nil {
				t.Fatalf("expected structural rejection for %q", tc.name)
			}
			if cap.requests != 1 {
				t.Fatalf("requests=%d for %q", cap.requests, tc.name)
			}
		})
	}
}

// TestSceneMultiOneSidedBindingUnions pins the asymmetric-but-valid case: a
// binding declared from only ONE side (containerId without the reverse
// boundElements entry, or vice versa) is accepted and still unions the pair, so
// selecting just one half fails closed while selecting the whole unit succeeds.
func TestSceneMultiOneSidedBindingUnions(t *testing.T) {
	containerSide := `{"elements":[
		{"id":"c1","type":"rectangle","x":1,"index":"a0","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"t1","type":"text","x":2,"index":"a1","version":1,"versionNonce":1,"isDeleted":false,"containerId":"c1","fontSize":20}
	],"baseVersion":"BV"}`
	boundSide := `{"elements":[
		{"id":"c1","type":"rectangle","x":1,"index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"text","id":"t1"}]},
		{"id":"t1","type":"text","x":2,"index":"a1","version":1,"versionNonce":1,"isDeleted":false,"fontSize":20}
	],"baseVersion":"BV"}`
	for _, body := range []string{containerSide, boundSide} {
		b := body
		// Selecting one half fails closed.
		_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Fatalf("partial one-sided unit must not PATCH: %s", r.Method)
			}
			io.WriteString(w, b)
		})
		if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "transform-many", "d1", "--id", "c1", "--x", "99"); err == nil {
			t.Fatal("expected fail-closed selecting only the container of a one-sided binding")
		}
		if cap.requests != 1 {
			t.Fatalf("requests=%d", cap.requests)
		}
		// Selecting the whole unit succeeds as a single PATCH.
		_, cap2 := semanticFactory(t, serveScene(t, b))
		out, _, err := execRoot(t, cap2.f, "--dry-run", "docs", "scene", "element", "transform-many", "d1", "--id", "c1", "--id", "t1", "--x", "99")
		if err != nil {
			t.Fatalf("whole one-sided unit must succeed: %v", err)
		}
		els := dryRunElements(t, dryRunData(t, out))
		if len(els) != 2 || els["c1"]["x"] != float64(99) || els["t1"]["x"] != float64(99) {
			t.Fatalf("one-sided unit not transformed atomically: %v", els)
		}
	}
}

// TestSceneMultiArrowBindingNotUnioned pins that a boundElements arrow ref is
// validated (it must resolve to a live arrow) but is NOT unioned into the
// shape's structural unit: an arrow bound to a rectangle does not force the
// arrow into the selection, so the rectangle may be mutated alone.
func TestSceneMultiArrowBindingNotUnioned(t *testing.T) {
	body := `{"elements":[
		{"id":"c1","type":"rectangle","x":1,"index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"arrow","id":"ar1"}]},
		{"id":"ar1","type":"arrow","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"points":[[0,0],[10,10]]}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "transform-many", "d1", "--id", "c1", "--x", "99")
	if err != nil {
		t.Fatalf("rectangle with a bound arrow must be mutable alone: %v", err)
	}
	els := dryRunElements(t, dryRunData(t, out))
	if len(els) != 1 || els["c1"]["x"] != float64(99) {
		t.Fatalf("arrow must not be pulled into the batch: %v", els)
	}
}

// ---- frame ancestry: self-reference and cycles rejected before union (requirement 1) ----

// TestSceneMultiRejectsFrameCycles pins that a frameId self-reference and any
// longer frame ancestry cycle (2-cycle, indirect 3-cycle) are rejected before any
// union and before any PATCH — a cyclic frameId graph is corrupt and would
// otherwise silently collapse into one component.
func TestSceneMultiRejectsFrameCycles(t *testing.T) {
	cases := []struct {
		name string
		body string
		sel  string
	}{
		{"self-reference", `{"elements":[
			{"id":"f1","type":"frame","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"frameId":"f1"}
		],"baseVersion":"BV"}`, "f1"},
		{"two-cycle", `{"elements":[
			{"id":"f1","type":"frame","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"frameId":"f2"},
			{"id":"f2","type":"frame","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"frameId":"f1"}
		],"baseVersion":"BV"}`, "f1"},
		{"indirect three-cycle", `{"elements":[
			{"id":"f1","type":"frame","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"frameId":"f2"},
			{"id":"f2","type":"frame","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"frameId":"f3"},
			{"id":"f3","type":"frame","index":"a2","version":1,"versionNonce":1,"isDeleted":false,"frameId":"f1"}
		],"baseVersion":"BV"}`, "f2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.body
			_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Fatalf("frame cycle must not PATCH: %s", r.Method)
				}
				io.WriteString(w, b)
			})
			if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "transform-many", "d1", "--id", tc.sel, "--x", "99"); err == nil {
				t.Fatalf("expected frame-cycle rejection for %q", tc.name)
			}
			if cap.requests != 1 {
				t.Fatalf("requests=%d for %q", cap.requests, tc.name)
			}
		})
	}
}

// TestSceneMultiAcceptsNestedFrames pins that an acyclic nested-frame chain (a
// frame inside a frame, with a leaf child) is still a valid single structural
// component: selecting the whole chain succeeds as one PATCH.
func TestSceneMultiAcceptsNestedFrames(t *testing.T) {
	body := `{"elements":[
		{"id":"f1","type":"frame","index":"a0","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"f2","type":"frame","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"frameId":"f1"},
		{"id":"c1","type":"rectangle","index":"a2","version":1,"versionNonce":1,"isDeleted":false,"frameId":"f2"}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "transform-many", "d1",
		"--id", "f1", "--id", "f2", "--id", "c1", "--x", "99")
	if err != nil {
		t.Fatalf("acyclic nested frames must be a valid unit: %v", err)
	}
	els := dryRunElements(t, dryRunData(t, out))
	if len(els) != 3 || els["f1"]["x"] != float64(99) || els["f2"]["x"] != float64(99) || els["c1"]["x"] != float64(99) {
		t.Fatalf("nested-frame unit not transformed atomically: %v", els)
	}
}

// ---- arrow endpoint bindings: validated, never unioned (requirement 2) ----

// TestSceneMultiRejectsArrowBindings pins the arrow startBinding/endBinding gate:
// a malformed binding, a dangling/self/non-bindable target, or a container that
// lists an arrow whose endpoints do not bind back (contradictory) fails the whole
// batch before any PATCH.
func TestSceneMultiRejectsArrowBindings(t *testing.T) {
	cases := []struct {
		name string
		body string
		sel  string
	}{
		{"startBinding not an object", `{"elements":[
			{"id":"ar1","type":"arrow","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"startBinding":"x"}
		],"baseVersion":"BV"}`, "ar1"},
		{"startBinding missing elementId", `{"elements":[
			{"id":"ar1","type":"arrow","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"startBinding":{"focus":0.1}}
		],"baseVersion":"BV"}`, "ar1"},
		{"startBinding empty elementId", `{"elements":[
			{"id":"ar1","type":"arrow","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"startBinding":{"elementId":""}}
		],"baseVersion":"BV"}`, "ar1"},
		{"endBinding dangling", `{"elements":[
			{"id":"ar1","type":"arrow","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"endBinding":{"elementId":"gone"}}
		],"baseVersion":"BV"}`, "ar1"},
		{"startBinding self", `{"elements":[
			{"id":"ar1","type":"arrow","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"startBinding":{"elementId":"ar1"}}
		],"baseVersion":"BV"}`, "ar1"},
		{"binds to another arrow", `{"elements":[
			{"id":"ar1","type":"arrow","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"endBinding":{"elementId":"ar2"}},
			{"id":"ar2","type":"arrow","index":"a1","version":1,"versionNonce":1,"isDeleted":false}
		],"baseVersion":"BV"}`, "ar1"},
		{"binds to a line", `{"elements":[
			{"id":"ar1","type":"arrow","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"endBinding":{"elementId":"ln1"}},
			{"id":"ln1","type":"line","index":"a1","version":1,"versionNonce":1,"isDeleted":false}
		],"baseVersion":"BV"}`, "ar1"},
		{"contradictory container/arrow", `{"elements":[
			{"id":"c1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"arrow","id":"ar1"}]},
			{"id":"d1","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false},
			{"id":"ar1","type":"arrow","index":"a2","version":1,"versionNonce":1,"isDeleted":false,"endBinding":{"elementId":"d1"}}
		],"baseVersion":"BV"}`, "ar1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.body
			_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Fatalf("bad arrow binding must not PATCH: %s", r.Method)
				}
				io.WriteString(w, b)
			})
			if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "transform-many", "d1", "--id", tc.sel, "--x", "99"); err == nil {
				t.Fatalf("expected arrow-binding rejection for %q", tc.name)
			}
			if cap.requests != 1 {
				t.Fatalf("requests=%d for %q", cap.requests, tc.name)
			}
		})
	}
}

// TestSceneMultiArrowEndpointBindingsValid pins the valid arrow-binding cases: a
// one-sided arrow→container binding, a one-sided container→arrow binding, and a
// reciprocal binding are all accepted, and in none of them is the arrow unioned
// into the shape's structural unit — the shape stays mutable on its own.
func TestSceneMultiArrowEndpointBindingsValid(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		// Arrow endpoint points at the shape; the shape does not list the arrow.
		{"one-sided arrow->container", `{"elements":[
			{"id":"r1","type":"rectangle","x":1,"index":"a0","version":1,"versionNonce":1,"isDeleted":false},
			{"id":"ar1","type":"arrow","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"points":[[0,0],[5,5]],"endBinding":{"elementId":"r1"}}
		],"baseVersion":"BV"}`},
		// Shape lists the arrow; the arrow carries no endpoint bindings.
		{"one-sided container->arrow", `{"elements":[
			{"id":"r1","type":"rectangle","x":1,"index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"arrow","id":"ar1"}]},
			{"id":"ar1","type":"arrow","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"points":[[0,0],[5,5]]}
		],"baseVersion":"BV"}`},
		// Both sides agree: the shape lists the arrow AND the arrow binds back to it.
		{"reciprocal", `{"elements":[
			{"id":"r1","type":"rectangle","x":1,"index":"a0","version":1,"versionNonce":1,"isDeleted":false,"boundElements":[{"type":"arrow","id":"ar1"}]},
			{"id":"ar1","type":"arrow","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"points":[[0,0],[5,5]],"startBinding":{"elementId":"r1"}}
		],"baseVersion":"BV"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, cap := semanticFactory(t, serveScene(t, tc.body))
			out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "transform-many", "d1", "--id", "r1", "--x", "99")
			if err != nil {
				t.Fatalf("valid arrow binding must let the shape mutate alone: %v", err)
			}
			els := dryRunElements(t, dryRunData(t, out))
			if len(els) != 1 || els["r1"]["x"] != float64(99) {
				t.Fatalf("arrow must not be pulled into the batch (%s): %v", tc.name, els)
			}
		})
	}
}

// ---- layer-many: anchor's full structural component is atomic (requirement 3) ----

// TestSceneLayerManyBeforeAnchorInInterleavedGroup pins that a before-move against
// an anchor that belongs to a group splices the moving run just BELOW the group's
// earliest live member — never between two group members — even when the group is
// noncontiguous in the source z-order (a filler element sits between its members).
func TestSceneLayerManyBeforeAnchorInInterleavedGroup(t *testing.T) {
	// Source z-order: gA(G,a1) fill(a2) gB(G,a3) mover(a9). Anchor is gB (the later
	// group member); "before gB" must land mover below gA (a1), not between fill and gB.
	body := `{"elements":[
		{"id":"gA","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false,"groupIds":["G"]},
		{"id":"fill","type":"rectangle","index":"a2","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"gB","type":"rectangle","index":"a3","version":1,"versionNonce":1,"isDeleted":false,"groupIds":["G"]},
		{"id":"mover","type":"rectangle","index":"a9","version":1,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "layer-many", "d1",
		"--id", "mover", "--position", "before", "--relative-to", "gB")
	if err != nil {
		t.Fatal(err)
	}
	els := dryRunElements(t, dryRunData(t, out))
	if len(els) != 1 {
		t.Fatalf("only mover should move: %v", els)
	}
	mi := els["mover"]["index"].(string)
	if err := fracindex.ValidateOrderKey(mi); err != nil {
		t.Fatalf("mover index %q not canonical: %v", mi, err)
	}
	// mover must land below the group's EARLIEST member gA (a1) — the component is
	// treated atomically, so the run never splits it.
	if !(mi < "a1") {
		t.Fatalf("mover %q not before the group's earliest member gA (a1) — component was split", mi)
	}
}

// TestSceneLayerManyAfterAnchorInFrameComponent pins the same atomicity for an
// after-move against an anchor that is a frame child: the run splices just ABOVE
// the frame component's latest live member, never between its members, with the
// component noncontiguous in the source order.
func TestSceneLayerManyAfterAnchorInFrameComponent(t *testing.T) {
	// Source: mover(a0) f1(frame,a1) fill(a2) c1(f1,a3) c2(f1,a5). Anchor is c1;
	// "after c1" must land mover above the frame component's latest member c2 (a5).
	body := `{"elements":[
		{"id":"mover","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"f1","type":"frame","index":"a1","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"fill","type":"rectangle","index":"a2","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"c1","type":"rectangle","index":"a3","version":1,"versionNonce":1,"isDeleted":false,"frameId":"f1"},
		{"id":"c2","type":"rectangle","index":"a5","version":1,"versionNonce":1,"isDeleted":false,"frameId":"f1"}
	],"baseVersion":"BV"}`
	_, cap := semanticFactory(t, serveScene(t, body))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "layer-many", "d1",
		"--id", "mover", "--position", "after", "--relative-to", "c1")
	if err != nil {
		t.Fatal(err)
	}
	els := dryRunElements(t, dryRunData(t, out))
	if len(els) != 1 {
		t.Fatalf("only mover should move: %v", els)
	}
	mi := els["mover"]["index"].(string)
	if err := fracindex.ValidateOrderKey(mi); err != nil {
		t.Fatalf("mover index %q not canonical: %v", mi, err)
	}
	// mover must land above the frame component's LATEST member c2 (a5).
	if !(mi > "a5") {
		t.Fatalf("mover %q not after the frame component's latest member c2 (a5) — component was split", mi)
	}
}

// ---- layer-many: semantic no-op emits success without PATCH (requirement 4) ----

// assertLayerNoOp parses the success envelope of a no-op layer move and asserts the
// noop marker is set.
func assertLayerNoOp(t *testing.T, out string) {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope: %v\n%s", err, out)
	}
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("no data object: %s", out)
	}
	if data["noop"] != true {
		t.Fatalf("expected noop:true, got: %v", data)
	}
}

// TestSceneLayerManySemanticNoOp pins that a move which reproduces the current live
// z-order exactly is recognized as a no-op: it emits a success envelope with
// noop:true and issues NO PATCH (only the read GET), avoiding churn. Covers
// already-front, already-back, already-before, and already-after.
func TestSceneLayerManySemanticNoOp(t *testing.T) {
	body := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e3","type":"rectangle","index":"a2","version":1,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	cases := []struct {
		name string
		args []string
	}{
		{"already-front", []string{"--id", "e3", "--position", "front"}},
		{"already-back", []string{"--id", "e1", "--position", "back"}},
		{"already-before", []string{"--id", "e1", "--position", "before", "--relative-to", "e2"}},
		{"already-after", []string{"--id", "e3", "--position", "after", "--relative-to", "e2"}},
		{"already-front-multi", []string{"--id", "e2", "--id", "e3", "--position", "front"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, cap := semanticFactory(t, serveScene(t, body))
			args := append([]string{"docs", "scene", "element", "layer-many", "d1"}, tc.args...)
			out, _, err := execRoot(t, cap.f, args...)
			if err != nil {
				t.Fatalf("no-op must succeed: %v", err)
			}
			if cap.requests != 1 {
				t.Fatalf("no-op must not PATCH; requests=%d", cap.requests)
			}
			assertLayerNoOp(t, out)
		})
	}
}

// TestSceneLayerManyNonNoOpStillPatches guards the boundary of the no-op check: a
// front move of an element that is NOT already at the front changes the z-order and
// must still issue a real PATCH (GET then PATCH), carrying the moved element's new
// index under If-Match.
func TestSceneLayerManyNonNoOpStillPatches(t *testing.T) {
	body := `{"elements":[
		{"id":"e1","type":"rectangle","index":"a0","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e2","type":"rectangle","index":"a1","version":1,"versionNonce":1,"isDeleted":false},
		{"id":"e3","type":"rectangle","index":"a2","version":1,"versionNonce":1,"isDeleted":false}
	],"baseVersion":"BV"}`
	var patch map[string]any
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, body)
			return
		}
		if r.Method != http.MethodPatch {
			t.Fatalf("write path must be PATCH, got %s", r.Method)
		}
		if r.Header.Get("If-Match") != "BV" {
			t.Fatalf("If-Match=%q", r.Header.Get("If-Match"))
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			t.Fatalf("decode PATCH body: %v", err)
		}
		io.WriteString(w, `{"applied":true}`)
	})
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "layer-many", "d1",
		"--id", "e1", "--position", "front"); err != nil {
		t.Fatal(err)
	}
	if cap.requests != 2 {
		t.Fatalf("expected GET+PATCH, requests=%d", cap.requests)
	}
	els, ok := patch["elements"].([]any)
	if !ok || len(els) != 1 {
		t.Fatalf("expected a real PATCH moving only e1: %v", patch["elements"])
	}
	e := els[0].(map[string]any)
	if e["id"] != "e1" {
		t.Fatalf("expected e1 in PATCH body: %v", e)
	}
	if idx, _ := e["index"].(string); !(idx > "a2") {
		t.Fatalf("e1 not moved above remaining max a2: %v", e["index"])
	}
}
