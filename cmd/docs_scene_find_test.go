package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func runFind(t *testing.T, sceneBody string, args ...string) map[string]any {
	t.Helper()
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("find must not %s", r.Method)
		}
		_, _ = io.WriteString(w, sceneBody)
	})
	full := append([]string{"docs", "scene", "find"}, args...)
	out, _, err := execRoot(t, cap.f, full...)
	if err != nil {
		t.Fatalf("find %v: %v", args, err)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope: %v\n%s", err, out)
	}
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("no data: %s", out)
	}
	return data
}

func findMatches(t *testing.T, data map[string]any) []map[string]any {
	t.Helper()
	raw, _ := data["matches"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		m, _ := r.(map[string]any)
		out = append(out, m)
	}
	return out
}

func TestSceneFindMatchesLiveOriginalTextOnly(t *testing.T) {
	scene := `{"baseVersion":"BV","files":{},"elements":[
		{"id":"t1","type":"text","originalText":"Hello world","y":10},
		{"id":"r1","type":"rectangle","text":"Hello shape","y":5},
		{"id":"t2","type":"text","originalText":"deleted hello","isDeleted":true,"y":1},
		{"id":"t3","type":"text","text":"fallback must not match","y":2}
	]}`
	matches := findMatches(t, runFind(t, scene, "d1", "hello"))
	if len(matches) != 1 || matches[0]["textElementId"] != "t1" {
		t.Fatalf("live originalText only: %v", matches)
	}
}

func TestSceneFindTrimsAndMatchesCaseInsensitively(t *testing.T) {
	scene := `{"baseVersion":"BV","files":{},"elements":[
		{"id":"t1","type":"text","originalText":"DECISION point","y":10}
	]}`
	data := runFind(t, scene, "d1", "  decision  ")
	matches := findMatches(t, data)
	if len(matches) != 1 || data["query"] != "decision" {
		t.Fatalf("trim/case semantics failed: %v", data)
	}
}

func TestSceneFindMatchesOriginalTextAcrossVisualWrap(t *testing.T) {
	scene := `{"baseVersion":"BV","files":{},"elements":[
		{"id":"t1","type":"text","originalText":"foo bar","text":"foo\nbar","y":0}
	]}`
	if len(findMatches(t, runFind(t, scene, "d1", "foo bar"))) != 1 {
		t.Fatal("expected match against originalText")
	}
}

func TestSceneFindBoundTextIncludesContainerContext(t *testing.T) {
	scene := `{"baseVersion":"BV","files":{},"elements":[
		{"id":"label1","type":"text","originalText":"Ship it","containerId":"rect1","frameId":"f1","x":10,"y":20}
	]}`
	matches := findMatches(t, runFind(t, scene, "d1", "ship"))
	if len(matches) != 1 || matches[0]["textElementId"] != "label1" || matches[0]["containerId"] != "rect1" || matches[0]["bound"] != true || matches[0]["frameId"] != "f1" {
		t.Fatalf("missing bound context: %v", matches)
	}
}

func TestSceneFindReturnsEveryNonOverlappingOccurrence(t *testing.T) {
	scene := `{"baseVersion":"BV","files":{},"elements":[
		{"id":"t1","type":"text","originalText":"aaaa","y":0}
	]}`
	data := runFind(t, scene, "d1", "aa")
	matches := findMatches(t, data)
	if len(matches) != 2 || data["totalMatches"] != float64(2) {
		t.Fatalf("expected two non-overlapping hits: %v", data)
	}
	if matches[0]["index"] != float64(0) || matches[1]["index"] != float64(2) {
		t.Fatalf("wrong offsets: %v", matches)
	}
}

func TestSceneFindUsesUTF16Offsets(t *testing.T) {
	scene := `{"baseVersion":"BV","files":{},"elements":[
		{"id":"t1","type":"text","originalText":"😀foo😀","y":0}
	]}`
	matches := findMatches(t, runFind(t, scene, "d1", "foo"))
	if len(matches) != 1 || matches[0]["index"] != float64(2) || matches[0]["length"] != float64(3) {
		t.Fatalf("expected JS UTF-16 index 2: %v", matches)
	}
}

func TestSceneFindSortsByYThenSceneOrder(t *testing.T) {
	scene := `{"baseVersion":"BV","files":{},"elements":[
		{"id":"same-first","type":"text","originalText":"x","y":5},
		{"id":"low","type":"text","originalText":"x","y":100},
		{"id":"same-second","type":"text","originalText":"x","y":5}
	]}`
	matches := findMatches(t, runFind(t, scene, "d1", "x"))
	want := []string{"same-first", "same-second", "low"}
	for i, id := range want {
		if matches[i]["textElementId"] != id {
			t.Fatalf("order=%v", matches)
		}
	}
}

func TestSceneFindWhitespaceOnlyQueryIsEmpty(t *testing.T) {
	scene := `{"baseVersion":"BV","files":{},"elements":[
		{"id":"t1","type":"text","originalText":"anything","y":0}
	]}`
	data := runFind(t, scene, "d1", "   ")
	if len(findMatches(t, data)) != 0 || data["totalMatches"] != float64(0) {
		t.Fatalf("whitespace query must be empty: %v", data)
	}
}

func TestSceneFindLimitAndExactTruncatedFlag(t *testing.T) {
	scene := `{"baseVersion":"BV","files":{},"elements":[
		{"id":"a","type":"text","originalText":"m","y":1},
		{"id":"b","type":"text","originalText":"m","y":2}
	]}`
	data := runFind(t, scene, "d1", "m", "--limit", "1")
	if data["truncated"] != true || data["totalMatches"] != float64(2) || len(findMatches(t, data)) != 1 {
		t.Fatalf("limit result: %v", data)
	}
	data = runFind(t, scene, "d1", "m", "--limit", "2")
	if data["truncated"] != false {
		t.Fatalf("exact limit is not truncated: %v", data)
	}
}

func TestSceneFindLimitBounds(t *testing.T) {
	for _, bad := range []string{"0", "-1", "1001"} {
		_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("invalid --limit %s must not trigger a request", bad)
		})
		_, _, err := execRoot(t, cap.f, "docs", "scene", "find", "d1", "q", "--limit", bad)
		if err == nil || cap.requests != 0 {
			t.Fatalf("--limit %s err=%v requests=%d", bad, err, cap.requests)
		}
	}
}
