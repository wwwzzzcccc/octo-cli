package cmd

import (
	"net/http"
	"testing"
)

func TestValidateEmbeddableURL(t *testing.T) {
	accept := []string{
		"https://youtube.com/watch?v=abc",
		"https://www.youtube.com/watch?v=abc",
		"https://youtu.be/abc",
		"http://vimeo.com/123",
		"https://player.vimeo.com/video/123",
		"https://www.figma.com/file/abc",
		"https://gist.github.com/user/abc",
		"https://x.com/user/status/1",
		"https://twitter.com/user/status/1",
		"https://stackblitz.com/edit/abc",
		"https://val.town/v/abc",
		"https://giphy.com/gifs/abc",
		"https://reddit.com/r/x/comments/y",
		"https://link.excalidraw.com/l/abc",
		"https://tenant.simplepdf.eu/doc", // first-subdomain wildcard
	}
	for _, u := range accept {
		if _, ok := validateEmbeddableURL(u); !ok {
			t.Errorf("expected %q allowed", u)
		}
	}
	reject := []string{
		"",
		"   ",
		"https://example.com",         // not on the allowlist
		"https://notyoutube.com",      // suffix must be exact bare host
		"https://youtube.com.evil.co", // allowlist host as a prefix label
		"https://simplepdf.eu",        // bare host: wildcard needs a subdomain
		"https://a.b.simplepdf.eu",    // two subdomains deep: only first-label wildcard
		"ftp://youtube.com/x",         // non-http scheme
		"javascript:alert(1)",         // no host / bad scheme
		"youtube.com/watch",           // relative, no scheme
		"https://",                    // no host
	}
	for _, u := range reject {
		if _, ok := validateEmbeddableURL(u); ok {
			t.Errorf("expected %q rejected", u)
		}
	}
}

func TestValidateEmbeddableURLLengthBound(t *testing.T) {
	long := "https://youtube.com/watch?v="
	for len(long) <= maxEmbeddableURLLen {
		long += "a"
	}
	if _, ok := validateEmbeddableURL(long); ok {
		t.Fatalf("over-length URL must be rejected")
	}
}

func TestEmbeddableIntrinsicSize(t *testing.T) {
	cases := map[string][2]float64{
		"https://youtube.com/watch?v=a":     {560, 315},
		"https://youtu.be/a":                {560, 315},
		"https://player.vimeo.com/video/1":  {560, 315},
		"https://www.figma.com/file/a":      {550, 550},
		"https://x.com/u/status/1":          {480, 480},
		"https://reddit.com/r/a":            {480, 480},
		"https://gist.github.com/u/a":       {550, 720},
		"https://stackblitz.com/edit/a":     {560, 840}, // generic fallback
		"https://tenant.simplepdf.eu/doc/a": {560, 840},
	}
	for u, want := range cases {
		w, h := embeddableIntrinsicSize(u)
		if w != want[0] || h != want[1] {
			t.Errorf("%q size = %vx%v, want %vx%v", u, w, h, want[0], want[1])
		}
	}
}

func TestSceneCreateEmbeddableDryRun(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"top","index":"a0"}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "create", "d1",
		"--type", "embeddable", "--id", "emb", "--url", "https://www.youtube.com/watch?v=abc", "--x", "10", "--y", "20")
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	if e["type"] != "embeddable" {
		t.Fatalf("type=%v", e["type"])
	}
	if e["link"] != "https://www.youtube.com/watch?v=abc" {
		t.Fatalf("link=%v", e["link"])
	}
	if e["strokeColor"] != "transparent" || e["backgroundColor"] != "transparent" {
		t.Fatalf("colors stroke=%v bg=%v", e["strokeColor"], e["backgroundColor"])
	}
	if e["width"] != float64(560) || e["height"] != float64(315) {
		t.Fatalf("intrinsic size = %vx%v", e["width"], e["height"])
	}
}

func TestSceneCreateEmbeddableGenericIntrinsicSize(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"top","index":"a0"}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "create", "d1",
		"--type", "embeddable", "--url", "https://stackblitz.com/edit/abc")
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	if e["width"] != float64(560) || e["height"] != float64(840) {
		t.Fatalf("generic size = %vx%v, want 560x840", e["width"], e["height"])
	}
}

func TestSceneCreateEmbeddableExplicitSizeAndColorOverride(t *testing.T) {
	_, cap := semanticFactory(t, serveScene(t, `{"elements":[{"id":"top","index":"a0"}],"baseVersion":"BV"}`))
	out, _, err := execRoot(t, cap.f, "--dry-run", "docs", "scene", "element", "create", "d1",
		"--type", "embeddable", "--url", "https://youtube.com/watch?v=a",
		"--width", "800", "--height", "600", "--stroke-color", "#ff0000")
	if err != nil {
		t.Fatal(err)
	}
	e := dryRunElement(t, dryRunData(t, out))
	if e["width"] != float64(800) || e["height"] != float64(600) {
		t.Fatalf("explicit size not honored: %vx%v", e["width"], e["height"])
	}
	if e["strokeColor"] != "#ff0000" {
		t.Fatalf("explicit stroke not honored: %v", e["strokeColor"])
	}
}

func TestSceneCreateEmbeddableRejectsNonAllowlistedURL(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("rejected URL must not trigger a request (%s)", r.Method)
	})
	_, _, err := execRoot(t, cap.f, "docs", "scene", "element", "create", "d1",
		"--type", "embeddable", "--url", "https://example.com")
	if err == nil {
		t.Fatal("expected rejection for non-allowlisted host")
	}
	if cap.requests != 0 {
		t.Fatalf("rejected create sent %d requests; want 0", cap.requests)
	}
}

func TestSceneCreateEmbeddableRequiresURL(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("missing --url must not trigger a request (%s)", r.Method)
	})
	_, _, err := execRoot(t, cap.f, "docs", "scene", "element", "create", "d1", "--type", "embeddable")
	if err == nil {
		t.Fatal("expected error when --url omitted")
	}
	if cap.requests != 0 {
		t.Fatalf("requests=%d; want 0", cap.requests)
	}
}

func TestSceneCreateURLRejectedForNonEmbeddable(t *testing.T) {
	_, cap := semanticFactory(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("misused --url must not trigger a request (%s)", r.Method)
	})
	_, _, err := execRoot(t, cap.f, "docs", "scene", "element", "create", "d1",
		"--type", "rectangle", "--url", "https://youtube.com/x")
	if err == nil {
		t.Fatal("expected error when --url used with a non-embeddable type")
	}
	if cap.requests != 0 {
		t.Fatalf("requests=%d; want 0", cap.requests)
	}
}
