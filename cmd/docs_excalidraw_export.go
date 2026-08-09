package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Mininglamp-OSS/octo-cli/internal/client"
	"github.com/Mininglamp-OSS/octo-cli/internal/cmdutil"
)

const (
	maxPortableExportAttachments = 100
	maxPortableAttachmentBytes   = 20 << 20
	maxPortableExportBytes       = 100 << 20
)

func registerDocsExcalidrawExportCmd(root *cobra.Command, f *cmdutil.Factory) {
	docs := commandAt(root, "docs")
	if docs == nil {
		return
	}
	var outputPath string
	cmd := &cobra.Command{
		Use:   "save-excalidraw <docId>",
		Short: "Save the live board as a round-trippable .excalidraw file",
		Long: `Read the authoritative live scene and save a version-2 Excalidraw
envelope containing elements, appState, and portable file data. Referenced board
attachments are downloaded through their fresh signed URLs and embedded as
dataURL values, so the output can be imported again with docs import.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDocsExcalidrawExport(cmd, f, args[0], outputPath)
		},
	}
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "destination .excalidraw path (required)")
	docs.AddCommand(cmd)
}

func runDocsExcalidrawExport(cmd *cobra.Command, f *cmdutil.Factory, docID, outputPath string) error {
	if strings.ToLower(filepath.Ext(outputPath)) != ".excalidraw" {
		return errors.New("--output must have the .excalidraw extension")
	}
	s, err := getScene(cmd, f, docID)
	if err != nil {
		return err
	}

	// clearElementsForExport: drop tombstoned elements and force linear elements'
	// lastCommittedPoint to null; everything else passes through verbatim (unlike
	// getVisibleElements, invisibly-small elements are NOT stripped on export).
	elements := clearElementsForExport(s.Elements)

	// filterOutDeletedFiles: only keep file entries referenced by a surviving
	// (non-deleted) element via its fileId.
	referenced := referencedFileIDs(elements)
	attachments, err := validatePortableAttachmentRefs(s.Files, referenced)
	if err != nil {
		return err
	}

	if f.Globals != nil && f.Globals.DryRun {
		preview, _ := json.Marshal(map[string]any{
			"dry_run":     true,
			"output":      outputPath,
			"elements":    len(elements),
			"files":       len(referenced),
			"appState":    exportedAppStateKeys(s.AppState),
			"baseVersion": s.BaseVersion,
		})
		return f.EmitSuccess(preview)
	}

	files := map[string]any{}
	var totalBytes int64
	for _, attachment := range attachments {
		fileID, raw, attachID := attachment.fileID, attachment.raw, attachment.attachID
		remaining := int64(maxPortableExportBytes) - totalBytes
		resolved, err := resolveAttachmentDataURL(cmd, f, docID, attachID, remaining)
		if err != nil {
			return fmt.Errorf("portable attachment %q: %w", fileID, err)
		}
		totalBytes += resolved.bytes
		entry := map[string]any{"id": fileID, "dataURL": resolved.dataURL, "mimeType": resolved.mimeType, "created": 0, "lastRetrieved": 0}
		if v, ok := raw["createdAt"].(float64); ok {
			entry["created"] = v
		}
		files[fileID] = entry
	}

	data, err := marshalExcalidrawEnvelope(elements, cleanAppStateForExport(s.AppState), files)
	if err != nil {
		return err
	}
	if err := client.WriteFileAtomic(outputPath, data, 0o600); err != nil {
		return err
	}
	result, _ := json.Marshal(map[string]any{"path": outputPath, "bytes": len(data), "elements": len(elements), "files": len(files), "baseVersion": s.BaseVersion})
	return f.EmitSuccess(result)
}

// excalidrawExportedAppStateKeys is the export allowlist from Excalidraw's
// APP_STATE_STORAGE_CONF (the keys whose `export` flag is true). Only these
// appState fields survive serializeAsJSON; everything else (theme, scroll, zoom,
// selection, name, exportBackground, ...) is stripped so the file stays portable.
var excalidrawExportedAppStateKeys = []string{"gridSize", "gridStep", "gridModeEnabled", "viewBackgroundColor"}

// cleanAppStateForExport keeps only the allowlisted appState keys actually present
// in the live snapshot, mirroring cleanAppStateForExport / _clearAppStateForStorage.
func cleanAppStateForExport(appState map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range excalidrawExportedAppStateKeys {
		if v, ok := appState[key]; ok {
			out[key] = v
		}
	}
	return out
}

// exportedAppStateKeys returns the allowlisted keys present in appState, sorted, for
// the dry-run preview (no values, just which fields the export would carry).
func exportedAppStateKeys(appState map[string]any) []string {
	keys := []string{}
	for _, key := range excalidrawExportedAppStateKeys {
		if _, ok := appState[key]; ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// clearElementsForExport mirrors Excalidraw clearElementsForExport: drop deleted
// elements and null out lastCommittedPoint on linear (arrow/line) elements, in the
// original z-order. Non-deleted, non-linear elements are returned unchanged.
func clearElementsForExport(elements []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(elements))
	for _, e := range elements {
		if e == nil || e["isDeleted"] == true {
			continue
		}
		if t, _ := e["type"].(string); t == "arrow" || t == "line" {
			clone := cloneMap(e)
			clone["lastCommittedPoint"] = nil
			out = append(out, clone)
			continue
		}
		out = append(out, e)
	}
	return out
}

// referencedFileIDs collects the fileId of every element that carries one, so only
// files still referenced by a surviving element are embedded (filterOutDeletedFiles).
func referencedFileIDs(elements []map[string]any) map[string]bool {
	ids := map[string]bool{}
	for _, e := range elements {
		if fileID, ok := e["fileId"].(string); ok && fileID != "" {
			ids[fileID] = true
		}
	}
	return ids
}

// marshalExcalidrawEnvelope emits the version-2 envelope with the same top-level
// key order Excalidraw's serializeAsJSON uses (type, version, source, elements,
// appState, files), 2-space indentation, and HTML escaping disabled so characters
// like < > & in element text or URLs are written literally as JSON.stringify does.
func marshalExcalidrawEnvelope(elements []map[string]any, appState, files map[string]any) ([]byte, error) {
	type envelope struct {
		Type     string           `json:"type"`
		Version  int              `json:"version"`
		Source   string           `json:"source"`
		Elements []map[string]any `json:"elements"`
		AppState map[string]any   `json:"appState"`
		Files    map[string]any   `json:"files"`
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(envelope{
		Type:     "excalidraw",
		Version:  2,
		Source:   "octo-cli",
		Elements: elements,
		AppState: appState,
		Files:    files,
	}); err != nil {
		return nil, err
	}
	// json.Encoder appends a trailing newline, matching the exporter's own append.
	return buf.Bytes(), nil
}

type portableAttachmentRef struct {
	fileID   string
	attachID string
	raw      map[string]any
}

func validatePortableAttachmentRefs(files map[string]any, referenced map[string]bool) ([]portableAttachmentRef, error) {
	if len(referenced) > maxPortableExportAttachments {
		return nil, fmt.Errorf("portable export references %d attachments; limit is %d", len(referenced), maxPortableExportAttachments)
	}
	ids := make([]string, 0, len(referenced))
	for fileID := range referenced {
		ids = append(ids, fileID)
	}
	sort.Strings(ids)
	refs := make([]portableAttachmentRef, 0, len(ids))
	for _, fileID := range ids {
		value, exists := files[fileID]
		if !exists {
			return nil, fmt.Errorf("portable attachment %q is referenced by an element but missing from scene files", fileID)
		}
		raw, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("portable attachment %q has invalid file metadata", fileID)
		}
		attachID, ok := raw["attachId"].(string)
		if !ok || strings.TrimSpace(attachID) == "" {
			return nil, fmt.Errorf("portable attachment %q is missing attachId", fileID)
		}
		refs = append(refs, portableAttachmentRef{fileID: fileID, attachID: attachID, raw: raw})
	}
	return refs, nil
}

type attachmentDataURL struct {
	dataURL  string
	mimeType string
	bytes    int64
}

func resolveAttachmentDataURL(cmd *cobra.Command, f *cmdutil.Factory, docID, attachID string, remainingBytes int64) (attachmentDataURL, error) {
	cli, err := f.Client()
	if err != nil {
		return attachmentDataURL{}, err
	}
	result, err := cli.Do(cmd.Context(), &client.Request{Method: http.MethodGet, Path: "/v1/bot/docs/" + url.PathEscape(docID) + "/attachments/" + url.PathEscape(attachID), SuppressSpaceHeader: true})
	if err != nil {
		return attachmentDataURL{}, err
	}
	var payload map[string]any
	if err := json.Unmarshal(result, &payload); err != nil {
		return attachmentDataURL{}, err
	}
	signedURL := firstString(payload, "url", "downloadUrl", "download_url")
	if signedURL == "" {
		if data, ok := payload["data"].(map[string]any); ok {
			signedURL = firstString(data, "url", "downloadUrl", "download_url")
		}
	}
	if signedURL == "" {
		return attachmentDataURL{}, errors.New("attachment resolve response omitted a download URL")
	}
	parsed, err := url.Parse(signedURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return attachmentDataURL{}, errors.New("attachment resolve returned an invalid download URL")
	}
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, signedURL, nil)
	if err != nil {
		return attachmentDataURL{}, err
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return attachmentDataURL{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return attachmentDataURL{}, fmt.Errorf("attachment download returned HTTP %d", resp.StatusCode)
	}
	if remainingBytes <= 0 {
		return attachmentDataURL{}, fmt.Errorf("cumulative attachment data exceeds the %d MiB portable export limit", maxPortableExportBytes>>20)
	}
	readLimit := int64(maxPortableAttachmentBytes)
	if remainingBytes < readLimit {
		readLimit = remainingBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, readLimit+1))
	if err != nil {
		return attachmentDataURL{}, err
	}
	if int64(len(body)) > readLimit {
		if remainingBytes < maxPortableAttachmentBytes {
			return attachmentDataURL{}, fmt.Errorf("cumulative attachment data exceeds the %d MiB portable export limit", maxPortableExportBytes>>20)
		}
		return attachmentDataURL{}, fmt.Errorf("attachment exceeds the %d MiB portable export limit", maxPortableAttachmentBytes>>20)
	}
	mime := firstString(payload, "mime", "mimeType")
	if mime == "" {
		mime = strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	}
	if mime != "image/png" && mime != "image/jpeg" && mime != "image/gif" && mime != "image/svg+xml" {
		return attachmentDataURL{}, fmt.Errorf("unsupported portable attachment MIME %q", mime)
	}
	return attachmentDataURL{dataURL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(body), mimeType: mime, bytes: int64(len(body))}, nil
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := m[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}
