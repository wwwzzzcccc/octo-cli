package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Mininglamp-OSS/octo-cli/internal/client"
	"github.com/Mininglamp-OSS/octo-cli/internal/cmdutil"
	"github.com/Mininglamp-OSS/octo-cli/internal/config"
	"github.com/Mininglamp-OSS/octo-cli/internal/credential"
)

func mermaidTestFactory(t *testing.T, handler http.HandlerFunc) (*cmdutil.TestFactory, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); handler(w, r) }))
	t.Cleanup(srv.Close)
	f := newTestFactoryWithReg()
	cfg := &config.Config{APIBaseURL: srv.URL, BotToken: "app_test", Format: "json"}
	cred := &credential.BotCredential{Token: "app_test", Source: "test"}
	f.SetConfig(cfg)
	f.SetCredential(cred)
	f.SetClient(client.New(cfg, cred, client.Options{NoRetry: true}))
	return f, &calls
}

func TestDocsSceneImportMermaidSchemaAliasOnly(t *testing.T) {
	f := newTestFactoryWithReg()
	f.SetConfig(&config.Config{Format: "json"})
	reg := f.Registry()
	wires := len(reg.ListOperations("docs"))
	if _, ok := reg.GetOperation("docs.scene.import-mermaid"); ok {
		t.Fatal("schema-only alias leaked into runtime operations")
	}
	if _, ok := reg.GetSchemaOperation("docs.scene.import-mermaid"); !ok {
		t.Fatal("schema alias is not discoverable")
	}
	if got := len(reg.ListOperations("docs")); got != wires {
		t.Fatalf("runtime operation count changed: %d -> %d", wires, got)
	}
}

func TestDocsSceneImportMermaidSource(t *testing.T) {
	f, calls := mermaidTestFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.EscapedPath() != "/v1/bot/docs/a%2Fb/import/mermaid" || r.URL.Query().Get("mode") != "merge" {
			t.Errorf("request = %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Content-Type") != "text/vnd.mermaid" || r.Header.Get("X-Octo-Import-Apply") != "true" {
			t.Errorf("headers = %v", r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "graph TD; A-->B" {
			t.Errorf("body=%q", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":2}`))
	})
	out, _, err := execRoot(t, f, "docs", "scene", "import-mermaid", "a/b", "--source", "graph TD; A-->B")
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data["format"] != "mermaid" || env.Data["created"] != float64(2) {
		t.Fatalf("data=%v", env.Data)
	}
}

func TestDocsSceneImportMermaidFileAndStdin(t *testing.T) {
	for _, tc := range []struct {
		name        string
		args        []string
		stdin, want string
	}{
		{"file", nil, "", "flowchart LR; X-->Y"},
		{"stdin", []string{"--file", "-"}, "sequenceDiagram\nA->>B: hi", "sequenceDiagram\nA->>B: hi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := mermaidTestFactory(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if string(body) != tc.want {
					t.Errorf("body=%q", body)
				}
				_, _ = w.Write([]byte(`{}`))
			})
			args := tc.args
			if tc.name == "file" {
				path := filepath.Join(t.TempDir(), "diagram.mmd")
				if err := os.WriteFile(path, []byte(tc.want), 0600); err != nil {
					t.Fatal(err)
				}
				args = []string{"--file", path}
			}
			f.In.WriteString(tc.stdin)
			args = append([]string{"docs", "scene", "import-mermaid", "doc"}, args...)
			if _, _, err := execRoot(t, f, args...); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDocsSceneImportMermaidValidation(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		stdin    string
		contains string
	}{
		{"missing", nil, "", "exactly one"},
		{"mutual", []string{"--file", "-", "--source", "graph TD"}, "graph TD", "exactly one"},
		{"empty source", []string{"--source", " \n\t"}, "", "must not be empty"},
		{"empty stdin", []string{"--file", "-"}, " \n", "must not be empty"},
		{"oversize", []string{"--source", strings.Repeat("x", maxMermaidImportChars+1)}, "", "100,000"},
		{"mode", []string{"--source", "graph TD", "--mode", "append"}, "", "merge or replace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, calls := mermaidTestFactory(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected HTTP") })
			f.In.WriteString(tc.stdin)
			args := append([]string{"docs", "scene", "import-mermaid", "doc"}, tc.args...)
			_, stderr, err := execRoot(t, f, args...)
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("err=%v stderr=%s", err, stderr)
			}
			if calls.Load() != 0 {
				t.Fatalf("calls=%d", calls.Load())
			}
		})
	}
}

func TestDocsSceneImportMermaidDryRunDoesNotLeakOrRequest(t *testing.T) {
	secret := "graph TD; SECRET_NODE-->B"
	f, calls := mermaidTestFactory(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected HTTP") })
	out, _, err := execRoot(t, f, "--dry-run", "docs", "scene", "import-mermaid", "doc", "--source", secret, "--mode", "replace")
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || strings.Contains(out, "SECRET_NODE") {
		t.Fatalf("calls=%d output=%s", calls.Load(), out)
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data["method"] != "POST" || env.Data["mode"] != "replace" {
		t.Fatalf("data=%v", env.Data)
	}
	sourceMeta := env.Data["source"].(map[string]any)
	limits := env.Data["limits"].(map[string]any)
	semantics := env.Data["semantics"].(map[string]any)
	if sourceMeta["kind"] != "source" || sourceMeta["size"] != float64(25) || limits["max_characters"] != float64(maxMermaidImportChars) || semantics["replaces_existing"] != true {
		t.Fatalf("data=%v", env.Data)
	}
}

func TestDocsSceneImportMermaidBackendStructuredError(t *testing.T) {
	f, _ := mermaidTestFactory(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"code":"INVALID_MERMAID","message":"parse failed","detail":{"line":2}}`))
	})
	_, stderr, err := execRoot(t, f, "docs", "scene", "import-mermaid", "doc", "--source", "graph TD")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(stderr, "INVALID_MERMAID") || !strings.Contains(stderr, "parse failed") {
		t.Fatalf("stderr=%s err=%v", stderr, err)
	}
}
