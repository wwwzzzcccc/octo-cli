package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-cli/internal/client"
	"github.com/Mininglamp-OSS/octo-cli/internal/cmdutil"
	"github.com/Mininglamp-OSS/octo-cli/internal/config"
	"github.com/Mininglamp-OSS/octo-cli/internal/credential"
	"github.com/Mininglamp-OSS/octo-cli/internal/output"
)

func writeTestImage(t *testing.T, name string, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func pngTestBytes() []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0xa5}, 32)...)
}

func sceneImageFactory(t *testing.T, srv *httptest.Server, opts client.Options) *cmdutil.TestFactory {
	t.Helper()
	f := newTestFactoryWithReg()
	cfg := &config.Config{APIBaseURL: srv.URL, BotToken: "bf_test", Format: "json"}
	cred := &credential.BotCredential{Token: "bf_test", Source: "test"}
	f.SetConfig(cfg)
	f.SetCredential(cred)
	opts.ErrOut = f.ErrOut
	f.SetClient(client.New(cfg, cred, opts))
	return f
}

func TestDocsSceneElementImage_SendsRawImageAndReturnsCanonicalResponse(t *testing.T) {
	image := pngTestBytes()
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.EscapedPath() != "/v1/bot/docs/board%2F1/scene/images" {
			t.Errorf("request = %s %s", r.Method, r.URL.EscapedPath())
		}
		if got := r.URL.Query().Get("x"); got != "12.5" {
			t.Errorf("x = %q", got)
		}
		if got := r.URL.Query().Get("y"); got != "-4" {
			t.Errorf("y = %q", got)
		}
		if got := r.URL.Query().Get("width"); got != "320" {
			t.Errorf("width = %q", got)
		}
		if got := r.URL.Query().Get("height"); got != "" {
			t.Errorf("height should be omitted, got %q", got)
		}
		if got := r.Header.Get("If-Match"); got != "base-secret" {
			t.Errorf("If-Match = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "image/png" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := r.Header.Get("X-File-Name"); got != "board.png" {
			t.Errorf("X-File-Name = %q", got)
		}
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"docId":"board/1","element":{"id":"e1","type":"image","opacity":100},"fileRef":{"fileId":"f1","attachId":"a1"},"baseVersion":"next","newDocVersionSeq":7,"bytes":40,"schemaVersion":3}`)
	}))
	defer srv.Close()

	f := sceneImageFactory(t, srv, client.Options{NoRetry: true, Verbose: true})
	path := writeTestImage(t, "board.png", image)
	out, errOut, err := execRoot(t, f, "docs", "scene", "element", "image", "board/1", "--file", path, "--base-version", "base-secret", "--x", "12.5", "--y", "-4", "--width", "320")
	if err != nil {
		t.Fatalf("image: %v\n%s", err, errOut)
	}
	if !bytes.Equal(gotBody, image) {
		t.Fatalf("raw body changed: got %x want %x", gotBody, image)
	}
	if strings.Contains(errOut, string(image)) || strings.Contains(errOut, "base-secret") {
		t.Fatalf("verbose output leaked body or base version: %q", errOut)
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			BaseVersion string `json:"baseVersion"`
			Element     struct {
				ID string `json:"id"`
			} `json:"element"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("response JSON: %v\n%s", err, out)
	}
	if !env.OK || env.Data.BaseVersion != "next" || env.Data.Element.ID != "e1" {
		t.Fatalf("unexpected response: %s", out)
	}
}

func TestDocsSceneElementImage_RejectsNonImageWithoutRequest(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer srv.Close()
	f := sceneImageFactory(t, srv, client.Options{NoRetry: true})
	path := writeTestImage(t, "fake.png", []byte("not an image"))
	_, errOut, err := execRoot(t, f, "docs", "scene", "element", "image", "b1", "--file", path, "--base-version", "v1", "--x", "0", "--y", "0")
	if err == nil || calls != 0 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	if ee := output.AsExitError(err); ee == nil || ee.Type != "validation" {
		t.Fatalf("error = %#v", err)
	}
	if !strings.Contains(errOut, "unsupported image") {
		t.Fatalf("stderr = %s", errOut)
	}
}

func TestDocsSceneElementImage_RejectsOversizeBeforeReadingOrRequest(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer srv.Close()
	f := sceneImageFactory(t, srv, client.Options{NoRetry: true})
	path := filepath.Join(t.TempDir(), "huge.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxSceneImageUploadBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	_, _, err = execRoot(t, f, "docs", "scene", "element", "image", "b1", "--file", path, "--base-version", "v1", "--x", "0", "--y", "0")
	if err == nil || calls != 0 || !strings.Contains(err.Error(), "10 MiB") {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestDocsSceneElementImage_MapsBackendError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = io.WriteString(w, `{"error":"image_pixels_exceeded"}`)
	}))
	defer srv.Close()
	f := sceneImageFactory(t, srv, client.Options{NoRetry: true})
	path := writeTestImage(t, "board.png", pngTestBytes())
	_, errOut, err := execRoot(t, f, "docs", "scene", "element", "image", "b1", "--file", path, "--base-version", "v1", "--x", "0", "--y", "0")
	ee := output.AsExitError(err)
	if ee == nil || ee.Type != "validation" || ee.Code != "PAYLOAD_TOO_LARGE" {
		t.Fatalf("error = %#v; stderr=%s", err, errOut)
	}
	if !strings.Contains(errOut, "image_pixels_exceeded") {
		t.Fatalf("backend response missing: %s", errOut)
	}
}

func TestDocsSceneElementImage_UsesGlobalTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	f := sceneImageFactory(t, srv, client.Options{NoRetry: true, Timeout: "10ms"})
	path := writeTestImage(t, "board.png", pngTestBytes())
	_, _, err := execRoot(t, f, "docs", "scene", "element", "image", "b1", "--file", path, "--base-version", "v1", "--x", "0", "--y", "0")
	ee := output.AsExitError(err)
	if ee == nil || ee.Type != "network" {
		t.Fatalf("error = %#v", err)
	}
}

func TestDocsSceneElementImage_DryRunDoesNotExposeBytesOrBaseVersion(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	f := sceneImageFactory(t, srv, client.Options{NoRetry: true})
	image := pngTestBytes()
	path := writeTestImage(t, "board.png", image)
	out, errOut, err := execRoot(t, f, "--dry-run", "docs", "scene", "element", "image", "b1", "--file", path, "--base-version", "private-version", "--x", "0", "--y", "0")
	if err != nil {
		t.Fatalf("dry-run: %v\n%s", err, errOut)
	}
	if strings.Contains(out, "private-version") || strings.Contains(out, string(image)) || strings.Contains(out, path) {
		t.Fatalf("dry-run leaked sensitive value: %s", out)
	}
	if !strings.Contains(out, `"file": "board.png"`) || !strings.Contains(out, `"bytes": 40`) {
		t.Fatalf("dry-run lacks safe metadata: %s", out)
	}
}
