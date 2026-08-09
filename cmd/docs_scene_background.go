package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Mininglamp-OSS/octo-cli/internal/cmdutil"
	"github.com/Mininglamp-OSS/octo-cli/internal/output"
)

// defaultViewBackgroundColor mirrors the frontend constant
// DEFAULT_VIEW_BACKGROUND_COLOR (octo-web board collab/schema.ts) and the
// Excalidraw engine default. `background reset` writes this explicit value —
// not transparent/null — so every collaborator converges on the same color, in
// step with the frontend color picker's onClear handler.
const defaultViewBackgroundColor = "#ffffff"

// viewBackgroundColorKey is the appState field the frontend collab layer
// persists into the authoritative Y.Doc (VIEW_BACKGROUND_COLOR_KEY).
const viewBackgroundColorKey = "viewBackgroundColor"

// normalizeCanvasColor trims surrounding whitespace only. The value is stored
// verbatim (no case folding), matching how element strokeColor/backgroundColor
// are stored and how the frontend persists CSS color strings.
func normalizeCanvasColor(color string) string {
	return strings.TrimSpace(color)
}

// isValidCanvasColor is a bounded, fail-closed validator for a canvas
// background color. It reuses validColor — the same length-bounded CSS-color
// rule (hex 3/4/6/8-digit, rgb()/rgba()/hsl()/hsla(), or a color keyword such
// as `transparent`) the element style commands enforce — so a color authored on
// the Web board round-trips unchanged and no unbounded string reaches the wire.
// The backend re-validates with the same rule set; the CLI never trusts it.
func isValidCanvasColor(color string) bool {
	return validColor(color)
}

// currentViewBackgroundColor reads appState.viewBackgroundColor from a scene
// snapshot, falling back to the default when the key is unset or not a string —
// matching the frontend, where an absent key renders as the default color.
func currentViewBackgroundColor(s *sceneSnapshot) string {
	if s == nil || s.AppState == nil {
		return defaultViewBackgroundColor
	}
	if v, ok := s.AppState[viewBackgroundColorKey].(string); ok && v != "" {
		return v
	}
	return defaultViewBackgroundColor
}

func registerSceneBackgroundCmds(scene *cobra.Command, f *cmdutil.Factory) {
	background := &cobra.Command{
		Use:   "background",
		Short: "Get, set, or reset the whiteboard canvas background color",
		Long: `Read and write the authoritative canvas background color
(appState.viewBackgroundColor) of a doc_type 'board' scene.

The color is stored in the same Y.Doc appState the live collaborative editor
uses, so a change made here appears in ` + "`docs scene get`" + `, survives reopen and
collaboration, and (when the backend honors it) is reflected in SVG/PNG export.
Writes are guarded by optimistic concurrency: the base-version token from the
scene read is sent as If-Match and a stale write is rejected with 412.`,
	}

	set := &cobra.Command{
		Use:   "set <docId> <color>",
		Short: "Set the canvas background color",
		Long: `Set appState.viewBackgroundColor to <color>.

Accepted forms (bounded, max 32 chars): hex #RGB/#RGBA/#RRGGBB/#RRGGBBAA,
rgb()/rgba()/hsl()/hsla(), or a color keyword such as transparent. Any other
value is rejected before a request is sent.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSceneBackgroundSet(cmd, f, args[0], args[1])
		},
	}

	reset := &cobra.Command{
		Use:   "reset <docId>",
		Short: "Reset the canvas background color to the default (#ffffff)",
		Long: `Reset appState.viewBackgroundColor to the default ` + defaultViewBackgroundColor + `.

This writes the explicit default color (not transparent/null), matching the
frontend color picker's clear behavior so every collaborator converges.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSceneBackgroundSet(cmd, f, args[0], defaultViewBackgroundColor)
		},
	}

	get := &cobra.Command{
		Use:   "get <docId>",
		Short: "Show the current canvas background color",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSceneBackgroundGet(cmd, f, args[0])
		},
	}

	background.AddCommand(set, reset, get)
	scene.AddCommand(background)
}

func runSceneBackgroundSet(cmd *cobra.Command, f *cmdutil.Factory, docID, color string) error {
	normalized := normalizeCanvasColor(color)
	if !isValidCanvasColor(normalized) {
		return emitBackgroundValidation(f,
			fmt.Sprintf("invalid canvas color %q", color),
			"use a bounded CSS color (<= 32 chars): hex #RGB/#RGBA/#RRGGBB/#RRGGBBAA, a functional rgb()/rgba()/hsl()/hsla(), or a color keyword such as transparent")
	}
	s, err := getScene(cmd, f, docID)
	if err != nil {
		_ = f.EmitError(err) //nolint:errcheck // envelope on stderr; RunE returns the same error
		return err
	}
	patch := map[string]any{"appState": map[string]any{viewBackgroundColorKey: normalized}}
	return patchScene(cmd, f, docID, s.BaseVersion, patch)
}

func runSceneBackgroundGet(cmd *cobra.Command, f *cmdutil.Factory, docID string) error {
	s, err := getScene(cmd, f, docID)
	if err != nil {
		_ = f.EmitError(err) //nolint:errcheck // envelope on stderr; RunE returns the same error
		return err
	}
	color := currentViewBackgroundColor(s)
	out := map[string]any{
		"docId":                docID,
		viewBackgroundColorKey: color,
		"isDefault":            color == defaultViewBackgroundColor,
		"baseVersion":          s.BaseVersion,
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return err
	}
	return f.EmitSuccess(encoded)
}

func emitBackgroundValidation(f *cmdutil.Factory, message, hint string) error {
	err := output.ErrValidation(message, hint)
	_ = f.EmitError(err) //nolint:errcheck // envelope on stderr; RunE returns the same error
	return err
}
