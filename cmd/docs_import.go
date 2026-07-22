package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Mininglamp-OSS/octo-cli/internal/client"
	"github.com/Mininglamp-OSS/octo-cli/internal/cmdutil"
	"github.com/Mininglamp-OSS/octo-cli/internal/output"
)

const maxDocsImportBytes int64 = 25 * 1024 * 1024

type docsImportFormat struct {
	name        string
	contentType string
}

// registerDocsImportCmd adds the file-oriented import workflow after the
// metadata-driven docs tree is built. The backend parses and applies each file
// atomically so a large converted document never has to pass through the generic
// JSON edit-body limit.
func registerDocsImportCmd(root *cobra.Command, f *cmdutil.Factory) {
	var docs *cobra.Command
	for _, child := range root.Commands() {
		if child.Name() == "docs" {
			docs = child
			break
		}
	}
	if docs == nil {
		return
	}

	var filePath string
	cmd := &cobra.Command{
		Use:   "import <docId>",
		Short: "Import a .docx, .md, or .xlsx file into an existing document",
		Long: `Import a local file into an existing Octo document.

The file extension selects the importer: .docx and .md/.markdown target a doc;
.xlsx targets a sheet. The backend parses and atomically replaces the existing
document or sheet content in one request.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDocsImport(cmd, f, args[0], filePath)
		},
	}
	cmd.Flags().StringVar(&filePath, "file", "", "path to the .docx, .md, .markdown, or .xlsx file (required)")
	_ = cmd.MarkFlagRequired("file") //nolint:errcheck // static flag name
	docs.AddCommand(cmd)
}

func docsImportFormatForPath(path string) (docsImportFormat, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".docx":
		return docsImportFormat{name: "docx", contentType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document"}, nil
	case ".md", ".markdown":
		return docsImportFormat{name: "markdown", contentType: "text/markdown"}, nil
	case ".xlsx":
		return docsImportFormat{name: "xlsx", contentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"}, nil
	default:
		return docsImportFormat{}, output.ErrValidation("unsupported import file type", "use a .docx, .md, .markdown, or .xlsx file")
	}
}

func runDocsImport(cmd *cobra.Command, f *cmdutil.Factory, docID, filePath string) error {
	format, err := docsImportFormatForPath(filePath)
	if err != nil {
		_ = f.EmitError(err) //nolint:errcheck // best-effort emit before returning err
		return err
	}
	info, err := os.Stat(filePath)
	if err != nil {
		ee := output.ErrValidation(fmt.Sprintf("--file: %v", err), "check path and permissions")
		_ = f.EmitError(ee) //nolint:errcheck
		return ee
	}
	if !info.Mode().IsRegular() {
		ee := output.ErrValidation("--file must point to a regular file", "pass a local .docx, .md, .markdown, or .xlsx file")
		_ = f.EmitError(ee) //nolint:errcheck
		return ee
	}
	if info.Size() == 0 {
		ee := output.ErrValidation("--file is empty", "pass a non-empty import file")
		_ = f.EmitError(ee) //nolint:errcheck
		return ee
	}
	if info.Size() > maxDocsImportBytes {
		ee := output.ErrValidation("--file exceeds the 25 MiB import limit", "use a smaller import file")
		_ = f.EmitError(ee) //nolint:errcheck
		return ee
	}
	if f.Globals != nil && f.Globals.DryRun {
		dryRun, marshalErr := json.Marshal(map[string]any{
			"dry_run":      true,
			"method":       http.MethodPost,
			"path":         "/v1/bot/docs/" + url.PathEscape(docID) + "/import/" + format.name,
			"file":         filePath,
			"size":         info.Size(),
			"content_type": format.contentType,
		})
		if marshalErr != nil {
			return marshalErr
		}
		return f.EmitSuccess(dryRun)
	}
	bytes, err := os.ReadFile(filePath)
	if err != nil {
		ee := output.ErrValidation(fmt.Sprintf("--file: %v", err), "check path and permissions")
		_ = f.EmitError(ee) //nolint:errcheck
		return ee
	}

	cli, err := f.Client()
	if err != nil {
		_ = f.EmitError(err) //nolint:errcheck
		return err
	}
	basePath := "/v1/bot/docs/" + url.PathEscape(docID)
	parsedRaw, err := cli.Do(cmd.Context(), &client.Request{
		Method:              http.MethodPost,
		Path:                basePath + "/import/" + format.name,
		Headers:             map[string]string{"X-Octo-Import-Apply": "true"},
		RawBody:             bytes,
		ContentType:         format.contentType,
		SuppressSpaceHeader: true,
	})
	if err != nil {
		_ = f.EmitError(err) //nolint:errcheck
		return err
	}
	var applied map[string]any
	if err := json.Unmarshal(parsedRaw, &applied); err != nil {
		return f.EmitSuccess(parsedRaw)
	}
	applied["format"] = format.name
	out, err := json.Marshal(applied)
	if err != nil {
		return err
	}
	return f.EmitSuccess(out)
}
