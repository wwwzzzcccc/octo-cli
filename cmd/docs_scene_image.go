package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Mininglamp-OSS/octo-cli/internal/client"
	"github.com/Mininglamp-OSS/octo-cli/internal/cmdutil"
	"github.com/Mininglamp-OSS/octo-cli/internal/output"
)

const maxSceneImageUploadBytes int64 = 10 * 1024 * 1024

type sceneImageFlags struct {
	file, baseVersion   string
	x, y, width, height float64
}

func registerSceneImageCmd(element *cobra.Command, f *cmdutil.Factory) {
	o := &sceneImageFlags{}
	cmd := &cobra.Command{
		Use:   "image <docId>",
		Short: "Upload and place a local image",
		Long: `Upload PNG, JPEG, or GIF bytes directly from a local file and place the
server-generated image element on the board. The bytes are sent as the raw HTTP
body and are never included in argv, verbose logs, or dry-run output.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSceneImage(cmd, f, args[0], o)
		},
	}
	cmd.Flags().StringVar(&o.file, "file", "", "local PNG, JPEG, or GIF path (required)")
	cmd.Flags().StringVar(&o.baseVersion, "base-version", "", "base-version from docs scene get (required)")
	cmd.Flags().Float64Var(&o.x, "x", 0, "top-left x coordinate (required)")
	cmd.Flags().Float64Var(&o.y, "y", 0, "top-left y coordinate (required)")
	cmd.Flags().Float64Var(&o.width, "width", 0, "optional positive display width")
	cmd.Flags().Float64Var(&o.height, "height", 0, "optional positive display height")
	_ = cmd.MarkFlagRequired("file")
	_ = cmd.MarkFlagRequired("base-version")
	_ = cmd.MarkFlagRequired("x")
	_ = cmd.MarkFlagRequired("y")
	element.AddCommand(cmd)
}

func runSceneImage(cmd *cobra.Command, f *cmdutil.Factory, docID string, o *sceneImageFlags) error {
	fail := func(err error) error {
		_ = f.EmitError(err) //nolint:errcheck // preserve the original structured error
		return err
	}
	if strings.TrimSpace(o.file) == "" {
		return fail(output.ErrValidation("--file is required", "pass a local PNG, JPEG, or GIF path"))
	}
	if strings.TrimSpace(o.baseVersion) == "" {
		return fail(output.ErrValidation("--base-version is required", "read the board with `docs scene get`, then pass its baseVersion"))
	}
	if !finite(o.x) || !finite(o.y) {
		return fail(output.ErrValidation("--x and --y must be finite", "pass finite scene coordinates"))
	}
	if cmd.Flags().Changed("width") && (!finite(o.width) || o.width <= 0) {
		return fail(output.ErrValidation("--width must be a positive finite number", "omit it or pass a value greater than zero"))
	}
	if cmd.Flags().Changed("height") && (!finite(o.height) || o.height <= 0) {
		return fail(output.ErrValidation("--height must be a positive finite number", "omit it or pass a value greater than zero"))
	}

	bytes, mime, err := readSceneImage(o.file)
	if err != nil {
		return fail(err)
	}
	query := url.Values{
		"x": []string{strconv.FormatFloat(o.x, 'g', -1, 64)},
		"y": []string{strconv.FormatFloat(o.y, 'g', -1, 64)},
	}
	if cmd.Flags().Changed("width") {
		query.Set("width", strconv.FormatFloat(o.width, 'g', -1, 64))
	}
	if cmd.Flags().Changed("height") {
		query.Set("height", strconv.FormatFloat(o.height, 'g', -1, 64))
	}
	name := filepath.Base(o.file)
	headers := map[string]string{
		"If-Match":    o.baseVersion,
		"X-File-Name": url.PathEscape(name),
	}
	docSegment, err := opaquePathSegment(docID, "docId")
	if err != nil {
		return fail(output.ErrValidation(err.Error(), "pass a document id, not a dot path segment"))
	}
	path := "/v1/bot/docs/" + docSegment + "/scene/images"

	if f.Globals != nil && f.Globals.DryRun {
		raw, marshalErr := json.Marshal(map[string]any{
			"dry_run": true,
			"method":  http.MethodPost,
			"path":    path,
			"query":   query,
			"file":    name,
			"bytes":   len(bytes),
			"mime":    mime,
		})
		if marshalErr != nil {
			return marshalErr
		}
		return f.EmitSuccess(raw)
	}

	cli, err := f.Client()
	if err != nil {
		return fail(err)
	}
	result, err := cli.Do(cmd.Context(), &client.Request{
		Method:              http.MethodPost,
		Path:                path,
		Query:               query,
		Headers:             headers,
		RawBody:             bytes,
		ContentType:         mime,
		SensitiveBody:       true,
		SuppressSpaceHeader: true,
	})
	if err != nil {
		return fail(err)
	}
	return f.EmitSuccess(result)
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func readSceneImage(path string) ([]byte, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, "", output.ErrValidation(fmt.Sprintf("open image %q: %v", path, err), "check --file points to a readable local file")
	}
	defer file.Close() //nolint:errcheck // read error is authoritative

	info, err := file.Stat()
	if err != nil {
		return nil, "", output.ErrValidation(fmt.Sprintf("stat image %q: %v", path, err), "check --file points to a readable regular file")
	}
	if !info.Mode().IsRegular() {
		return nil, "", output.ErrValidation("--file must be a regular file", "pass a local PNG, JPEG, or GIF file")
	}
	if info.Size() == 0 {
		return nil, "", output.ErrValidation("image file is empty", "pass a non-empty PNG, JPEG, or GIF file")
	}
	if info.Size() > maxSceneImageUploadBytes {
		return nil, "", output.ErrValidation("image exceeds the 10 MiB upload limit", "resize or compress the image before uploading")
	}
	// Read from the already-open descriptor (rather than reopening path) to
	// avoid a symlink/path swap between validation and upload. The extra byte
	// keeps the size gate fail-closed if the file grows after Stat.
	bytes, err := io.ReadAll(io.LimitReader(file, maxSceneImageUploadBytes+1))
	if err != nil {
		return nil, "", output.ErrValidation(fmt.Sprintf("read image %q: %v", path, err), "check --file points to a readable local file")
	}
	if int64(len(bytes)) > maxSceneImageUploadBytes {
		return nil, "", output.ErrValidation("image exceeds the 10 MiB upload limit", "resize or compress the image before uploading")
	}
	mime := sniffSceneImage(bytes)
	if mime == "" {
		return nil, "", output.ErrValidation("unsupported image file", "use a PNG, JPEG, or GIF file; extension alone is not accepted")
	}
	return bytes, mime, nil
}

func sniffSceneImage(b []byte) string {
	if len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n" {
		return "image/png"
	}
	if len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff {
		return "image/jpeg"
	}
	if len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a") {
		return "image/gif"
	}
	return ""
}
