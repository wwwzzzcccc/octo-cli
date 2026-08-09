package cmd

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScenePathRejectsDotSegmentsAndEscapesOpaqueID(t *testing.T) {
	for _, id := range []string{".", ".."} {
		if _, err := scenePath(id); err == nil {
			t.Fatalf("expected %q rejection", id)
		}
	}
	got, err := scenePath("doc/with space")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/v1/bot/docs/doc%2Fwith%20space/scene" {
		t.Fatalf("path=%q", got)
	}
}

func TestSceneGetRejectsDotDocIDBeforeRequest(t *testing.T) {
	_, cap := semanticFactory(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected request") })
	if _, _, err := execRoot(t, cap.f, "docs", "scene", "element", "transform", "..", "e1", "--x", "1"); err == nil || !strings.Contains(err.Error(), "must not be . or ..") {
		t.Fatalf("err=%v", err)
	}
	if cap.requests != 0 {
		t.Fatalf("requests=%d", cap.requests)
	}
}

func TestSceneImageRejectsDotDocIDBeforeRequest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tiny.png")
	png := []byte{137, 80, 78, 71, 13, 10, 26, 10, 0, 0, 0, 13, 73, 72, 68, 82, 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0}
	if err := os.WriteFile(path, png, 0600); err != nil {
		t.Fatal(err)
	}
	_, cap := semanticFactory(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected request") })
	_, _, err := execRoot(t, cap.f, "docs", "scene", "element", "image", "..", "--file", path, "--base-version", "BV", "--x", "0", "--y", "0")
	if err == nil || !strings.Contains(err.Error(), "must not be . or ..") {
		t.Fatalf("err=%v", err)
	}
	if cap.requests != 0 {
		t.Fatalf("requests=%d", cap.requests)
	}
}
