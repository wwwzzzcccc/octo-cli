package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Mininglamp-OSS/octo-cli/internal/client"
	"github.com/Mininglamp-OSS/octo-cli/internal/cmdutil"
	"github.com/Mininglamp-OSS/octo-cli/internal/config"
	"github.com/Mininglamp-OSS/octo-cli/internal/credential"
)

func importTestRoot(t *testing.T, handler http.HandlerFunc) (*cmdutil.TestFactory, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	tf := newTestFactoryWithReg()
	cfg := &config.Config{APIBaseURL: srv.URL, BotToken: "app_test", Format: "json"}
	cred := &credential.BotCredential{Token: "app_test", Source: "test"}
	tf.SetConfig(cfg)
	tf.SetCredential(cred)
	tf.SetClient(client.New(cfg, cred, client.Options{ErrOut: io.Discard, NoRetry: true}))
	return tf, srv
}

func writeImportFixture(t *testing.T, ext string, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input"+ext)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDocsImportMarkdown_AppliesAtomically(t *testing.T) {
	var steps []string
	tf, _ := importTestRoot(t, func(w http.ResponseWriter, r *http.Request) {
		steps = append(steps, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if got := r.Header.Get("Content-Type"); got != "text/markdown" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := r.Header.Get("X-Octo-Import-Apply"); got != "true" {
			t.Errorf("apply header = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "# Imported\n" {
			t.Errorf("body = %q", body)
		}
		_, _ = w.Write([]byte(`{"docId":"d1","baseVersion":"BV_NEW==","newDocVersionSeq":2,"warnings":["note"]}`))
	})
	file := writeImportFixture(t, ".md", []byte("# Imported\n"))
	out, _, err := execRoot(t, tf, "docs", "import", "d1", "--file", file)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	wantSteps := []string{"POST /v1/bot/docs/d1/import/markdown"}
	if len(steps) != len(wantSteps) || steps[0] != wantSteps[0] {
		t.Fatalf("steps = %v, want %v", steps, wantSteps)
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Format   string   `json:"format"`
			Warnings []string `json:"warnings"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK || env.Data.Format != "markdown" || len(env.Data.Warnings) != 1 {
		t.Errorf("envelope = %+v", env)
	}
}

func TestDocsImportXlsx_WritesInSingleRequest(t *testing.T) {
	calls := 0
	tf, _ := importTestRoot(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/bot/docs/s1/import/xlsx" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
			t.Errorf("Content-Type = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"baseVersion":"BV2","warnings":[]}`))
	})
	file := writeImportFixture(t, ".xlsx", []byte("xlsx bytes"))
	if _, _, err := execRoot(t, tf, "docs", "import", "s1", "--file", file); err != nil {
		t.Fatalf("import: %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestDocsImport_RejectsUnsupportedExtensionBeforeRequest(t *testing.T) {
	called := false
	tf, _ := importTestRoot(t, func(w http.ResponseWriter, r *http.Request) { called = true })
	file := writeImportFixture(t, ".pdf", []byte("pdf"))
	if _, _, err := execRoot(t, tf, "docs", "import", "d1", "--file", file); err == nil {
		t.Fatal("expected unsupported extension error")
	}
	if called {
		t.Error("server must not be called")
	}
}

func TestDocsImport_DryRunDoesNotReadOrUploadFile(t *testing.T) {
	called := false
	tf, _ := importTestRoot(t, func(w http.ResponseWriter, r *http.Request) { called = true })
	file := writeImportFixture(t, ".docx", []byte("not a real docx"))
	out, _, err := execRoot(t, tf, "--dry-run", "docs", "import", "d1", "--file", file)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if called {
		t.Error("server must not be called during --dry-run")
	}
	var env struct {
		Data struct {
			DryRun      bool   `json:"dry_run"`
			Path        string `json:"path"`
			ContentType string `json:"content_type"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if !env.Data.DryRun || env.Data.Path != "/v1/bot/docs/d1/import/docx" {
		t.Errorf("dry-run envelope = %+v", env)
	}
	if env.Data.ContentType != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
		t.Errorf("content type = %q", env.Data.ContentType)
	}
}
