package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/Mininglamp-OSS/octo-cli/internal/client"
	"github.com/Mininglamp-OSS/octo-cli/internal/cmdutil"
	"github.com/Mininglamp-OSS/octo-cli/internal/output"
)

const maxMermaidImportChars = 100_000

func registerDocsSceneMermaidCmd(scene *cobra.Command, f *cmdutil.Factory) {
	var filePath, source, mode string
	cmd := &cobra.Command{
		Use:   "import-mermaid <docId>",
		Short: "Send Mermaid source for server-side board import",
		Long: `Send Mermaid source to the board import transport for server-side conversion.

Exactly one of --file or --source is required. Use --file - to read UTF-8 source
from stdin. Merge requests preservation of existing board elements; replace
requests explicit replacement. Availability depends on the backend deployment
providing a Mermaid converter.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDocsSceneMermaidImport(cmd, f, args[0], filePath, source, mode)
		},
	}
	cmd.Flags().StringVar(&filePath, "file", "", "Mermaid source file path, or - for stdin")
	cmd.Flags().StringVar(&source, "source", "", "inline Mermaid source")
	cmd.Flags().StringVar(&mode, "mode", "merge", "import mode: merge or replace")
	scene.AddCommand(cmd)
}

func runDocsSceneMermaidImport(cmd *cobra.Command, f *cmdutil.Factory, docID, filePath, source, mode string) error {
	fileSet := cmd.Flags().Changed("file")
	sourceSet := cmd.Flags().Changed("source")
	if fileSet == sourceSet {
		return emitMermaidValidation(f, "exactly one of --file or --source is required", "pass inline source with --source, a regular file with --file, or stdin with --file -")
	}
	if mode != "merge" && mode != "replace" {
		return emitMermaidValidation(f, "--mode must be merge or replace", "use --mode merge to preserve existing elements, or --mode replace to explicitly overwrite the board")
	}

	kind := "source"
	var raw []byte
	var err error
	switch {
	case sourceSet:
		raw = []byte(source)
	case filePath == "-":
		kind = "stdin"
		raw, err = readMermaidSource(f.IOStreams.In)
	default:
		kind = "file"
		info, statErr := os.Stat(filePath)
		if statErr != nil {
			return emitMermaidValidation(f, fmt.Sprintf("--file: %v", statErr), "check the path and permissions")
		}
		if !info.Mode().IsRegular() {
			return emitMermaidValidation(f, "--file must point to a regular file", "pass a regular Mermaid source file, or use --file - for stdin")
		}
		file, openErr := os.Open(filePath)
		if openErr != nil {
			return emitMermaidValidation(f, fmt.Sprintf("--file: %v", openErr), "check the path and permissions")
		}
		raw, err = readMermaidSource(file)
		closeErr := file.Close()
		if err == nil && closeErr != nil {
			err = closeErr
		}
	}
	if err != nil {
		return emitMermaidValidation(f, fmt.Sprintf("read Mermaid source: %v", err), "check the input and try again")
	}
	if !utf8.Valid(raw) {
		return emitMermaidValidation(f, "Mermaid source must be valid UTF-8", "save the source as UTF-8 and try again")
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return emitMermaidValidation(f, "Mermaid source must not be empty", "provide non-whitespace Mermaid source")
	}
	chars := utf8.RuneCount(raw)
	if chars > maxMermaidImportChars {
		return emitMermaidValidation(f, "Mermaid source exceeds the 100,000 character limit", "use a smaller Mermaid diagram")
	}

	segment, err := opaquePathSegment(docID, "docId")
	if err != nil {
		return emitMermaidValidation(f, err.Error(), "pass a non-empty document id")
	}
	path := "/v1/bot/docs/" + segment + "/import/mermaid"
	if f.Globals != nil && f.Globals.DryRun {
		preview := map[string]any{
			"dry_run":      true,
			"method":       http.MethodPost,
			"path":         path + "?mode=" + url.QueryEscape(mode),
			"source":       map[string]any{"kind": kind, "size": chars},
			"content_type": "text/vnd.mermaid",
			"headers":      map[string]string{"X-Octo-Import-Apply": "true"},
			"mode":         mode,
			"semantics": map[string]any{
				"preserves_existing": mode == "merge",
				"replaces_existing":  mode == "replace",
				"applied_atomically": true,
			},
			"limits": map[string]any{"max_characters": maxMermaidImportChars, "requires_nonempty_trimmed_source": true, "encoding": "UTF-8"},
		}
		encoded, marshalErr := json.Marshal(preview)
		if marshalErr != nil {
			return marshalErr
		}
		return f.EmitSuccess(encoded)
	}

	cli, err := f.Client()
	if err != nil {
		_ = f.EmitError(err) //nolint:errcheck
		return err
	}
	response, err := cli.Do(cmd.Context(), &client.Request{
		Method:              http.MethodPost,
		Path:                path,
		Query:               url.Values{"mode": []string{mode}},
		Headers:             map[string]string{"X-Octo-Import-Apply": "true"},
		RawBody:             raw,
		ContentType:         "text/vnd.mermaid",
		SensitiveBody:       true,
		SuppressSpaceHeader: true,
	})
	if err != nil {
		_ = f.EmitError(err) //nolint:errcheck
		return err
	}
	var result map[string]any
	if err := json.Unmarshal(response, &result); err != nil || result == nil {
		return errors.New("Mermaid import response must be a JSON object")
	}
	result["format"] = "mermaid"
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return f.EmitSuccess(encoded)
}

// A valid UTF-8 source can occupy at most four bytes per character. Reading one
// four extra bytes bound file/stdin memory while still allowing exact character-count
// validation after decoding.
func readMermaidSource(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, maxMermaidImportChars*4+4))
}

func emitMermaidValidation(f *cmdutil.Factory, message, hint string) error {
	err := output.ErrValidation(message, hint)
	_ = f.EmitError(err) //nolint:errcheck
	return err
}
