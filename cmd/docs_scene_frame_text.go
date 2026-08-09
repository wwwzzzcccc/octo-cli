package cmd

import (
	"errors"
	"fmt"
	"math"
	"reflect"

	"github.com/spf13/cobra"

	"github.com/Mininglamp-OSS/octo-cli/internal/cmdutil"
	"github.com/Mininglamp-OSS/octo-cli/internal/fracindex"
)

// registerSceneFrameTextCmds exposes structural operations as atomic scene
// mutations rather than requiring callers to hand-edit reciprocal references.
func registerSceneFrameTextCmds(element *cobra.Command, f *cmdutil.Factory) {
	frameCreate := &cobra.Command{Use: "frame-create <docId>", Short: "Create a frame element", Args: cobra.ExactArgs(1)}
	fo := &sceneFlags{typeName: "frame", strokeColor: "#1b1b1f", backgroundColor: "transparent", fillStyle: "solid", strokeWidth: 1, roughness: 1, opacity: 100, fontSize: 20}
	frameCreate.Flags().StringVar(&fo.id, "id", "", "element id (generated when omitted)")
	frameCreate.Flags().Float64Var(&fo.x, "x", 0, "x coordinate")
	frameCreate.Flags().Float64Var(&fo.y, "y", 0, "y coordinate")
	frameCreate.Flags().Float64Var(&fo.width, "width", 100, "width")
	frameCreate.Flags().Float64Var(&fo.height, "height", 100, "height")
	frameCreate.RunE = func(cmd *cobra.Command, args []string) error {
		if err := validateCreateFlags(fo); err != nil {
			return err
		}
		return runSceneCreate(cmd, f, args[0], fo)
	}

	frameChange := func(name, short string, add bool) *cobra.Command {
		var ids []string
		c := &cobra.Command{Use: name + " <docId> <frameId>", Short: short, Args: cobra.ExactArgs(2)}
		c.Flags().StringArrayVar(&ids, "id", nil, "child element id (repeatable)")
		c.RunE = func(cmd *cobra.Command, args []string) error {
			if err := validateSelection(ids, 1); err != nil {
				return err
			}
			return runFrameChange(cmd, f, args[0], args[1], ids, add)
		}
		return c
	}
	frameAdd := frameChange("frame-add", "Atomically add elements to a frame", true)
	frameRemove := frameChange("frame-remove", "Atomically remove elements from a frame", false)

	var unframeIDs []string
	unframe := &cobra.Command{Use: "unframe <docId>", Short: "Atomically remove elements from their current frames", Args: cobra.ExactArgs(1)}
	unframe.Flags().StringArrayVar(&unframeIDs, "id", nil, "element id (repeatable)")
	unframe.RunE = func(cmd *cobra.Command, args []string) error {
		if err := validateSelection(unframeIDs, 1); err != nil {
			return err
		}
		return runFrameChange(cmd, f, args[0], "", unframeIDs, false)
	}

	bindText := &cobra.Command{Use: "bind-text <docId> <textId> <containerId>", Short: "Bind text to a container; plain lines become visually identical headless arrows", Args: cobra.ExactArgs(3)}
	bindText.RunE = func(cmd *cobra.Command, args []string) error {
		return runTextBinding(cmd, f, args[0], args[1], args[2], false)
	}
	unbindText := &cobra.Command{Use: "unbind-text <docId> <textId>", Short: "Atomically unbind text from its container", Args: cobra.ExactArgs(2)}
	unbindText.RunE = func(cmd *cobra.Command, args []string) error {
		return runTextBinding(cmd, f, args[0], args[1], "", true)
	}

	element.AddCommand(frameCreate, frameAdd, frameRemove, unframe, bindText, unbindText)
}

func runFrameChange(cmd *cobra.Command, f *cmdutil.Factory, docID, frameID string, ids []string, add bool) error {
	s, err := getScene(cmd, f, docID)
	if err != nil {
		return err
	}
	if _, err = buildStructuralGraph(s.Elements); err != nil {
		return err
	}
	if add || frameID != "" {
		frame := findElement(s.Elements, frameID)
		if frame == nil || frame["isDeleted"] == true || frame["type"] != "frame" {
			return fmt.Errorf("live frame %q not found", frameID)
		}
	}
	selected, err := resolveSelection(s.Elements, ids)
	if err != nil {
		return err
	}
	changed := make([]map[string]any, 0, len(selected))
	for _, original := range selected {
		if original["id"] == frameID {
			return errors.New("a frame cannot contain itself")
		}
		clone := cloneMap(original)
		current, err := structuralRef(original, "frameId")
		if err != nil {
			return err
		}
		if add {
			if current == frameID {
				return fmt.Errorf("element %q is already in frame %q", original["id"], frameID)
			}
			if current != "" {
				return fmt.Errorf("element %q is already in frame %q; remove it first", original["id"], current)
			}
			clone["frameId"] = frameID
		} else {
			if current == "" {
				return fmt.Errorf("element %q is not in a frame", original["id"])
			}
			if frameID != "" && current != frameID {
				return fmt.Errorf("element %q is in frame %q, not %q", original["id"], current, frameID)
			}
			clone["frameId"] = nil
		}
		changed = append(changed, clone)
	}
	return patchStructuralChanges(cmd, f, docID, s, changed)
}

func runTextBinding(cmd *cobra.Command, f *cmdutil.Factory, docID, textID, containerID string, unbind bool) error {
	s, err := getScene(cmd, f, docID)
	if err != nil {
		return err
	}
	if _, err = buildStructuralGraph(s.Elements); err != nil {
		return err
	}
	text := findElement(s.Elements, textID)
	if text == nil || text["isDeleted"] == true || text["type"] != "text" {
		return fmt.Errorf("live text %q not found", textID)
	}
	textClone := cloneMap(text)
	current, err := structuralRef(text, "containerId")
	if err != nil {
		return err
	}
	var container map[string]any
	if unbind {
		if current == "" {
			return fmt.Errorf("text %q is not bound", textID)
		}
		container = findElement(s.Elements, current)
		if container == nil || container["isDeleted"] == true {
			return fmt.Errorf("live container %q not found", current)
		}
		// The patched Web renderer derives linear-label display geometry at
		// runtime from browser text metrics and path layout. The Go CLI cannot
		// reproduce that position exactly. Clearing the binding while retaining
		// stale persisted x/y would make the label jump, so fail before PATCH.
		if container["type"] == "arrow" {
			return fmt.Errorf("cannot unbind text %q from linear container %q without browser-resolved display geometry", textID, current)
		}
	} else {
		if current != "" {
			return fmt.Errorf("text %q is already bound to %q; unbind it first", textID, current)
		}
		container = findElement(s.Elements, containerID)
		if container == nil || container["isDeleted"] == true {
			return fmt.Errorf("live container %q not found", containerID)
		}
		kind, _ := container["type"].(string)
		if !isValidTextContainer(kind) && kind != "line" {
			return fmt.Errorf("element %q of type %q cannot contain text", containerID, kind)
		}
	}
	containerClone := cloneMap(container)
	if unbind {
		if err := removeReciprocalText(containerClone, textID); err != nil {
			return err
		}
		textClone["containerId"] = nil
		textClone["autoResize"] = true
	} else {
		if containerClone["type"] == "line" {
			if err := convertLineToHeadlessArrow(containerClone); err != nil {
				return fmt.Errorf("convert line %q for text binding: %w", containerID, err)
			}
			cmd.PrintErrf("note: converted line %q to a visually identical arrow without arrowheads so bound text %q follows its geometry\n", containerID, textID)
		}
		if err := addReciprocalText(containerClone, textID); err != nil {
			return err
		}
		textClone["containerId"] = containerID
		// Text created by double-clicking a Web container remains auto-sized. Keep
		// that contract so the selection/caret proxy uses the measured text bounds
		// instead of a stale caller-supplied box.
		textClone["autoResize"] = true
		textClone["textAlign"] = "center"
		textClone["verticalAlign"] = "middle"
		textClone["angle"] = containerClone["angle"]
		if containerClone["type"] != "arrow" {
			if err := positionBoundText(textClone, containerClone); err != nil {
				return err
			}
		}
		// Excalidraw inserts bound text immediately above its container. Without
		// this, a previously-created container can paint over the text, making it
		// look unselectable even though the reciprocal binding is valid.
		if err := placeTextImmediatelyAfterContainer(textClone, containerClone, s.Elements); err != nil {
			return err
		}
	}
	return patchStructuralChanges(cmd, f, docID, s, []map[string]any{textClone, containerClone})
}

// convertLineToHeadlessArrow preserves every line geometry/style field while
// changing only the semantic type required by Excalidraw's native bound-text
// machinery. Null arrowheads keep the element visually indistinguishable from
// a plain line; movement, rotation, path edits, rendering, and export then use
// the same reciprocal binding contract as every other labelled arrow.
func convertLineToHeadlessArrow(line map[string]any) error {
	for _, field := range []string{"startArrowhead", "endArrowhead", "startBinding", "endBinding"} {
		if value, exists := line[field]; exists && value != nil {
			return fmt.Errorf("line has non-null %s", field)
		}
	}
	if elbowed, exists := line["elbowed"]; exists {
		value, ok := elbowed.(bool)
		if !ok {
			return errors.New("line has malformed elbowed state")
		}
		if value {
			return errors.New("line is elbowed")
		}
	}
	if polygon, exists := line["polygon"]; exists {
		value, ok := polygon.(bool)
		if !ok || value {
			return errors.New("line has unsupported polygon state")
		}
	}
	// These fields encode arrow-only routing state. Reject their presence even
	// when the value is null: silently dropping them would turn malformed or
	// future imported routing data into a different element.
	for _, field := range []string{"fixedSegments", "fixedPoint", "startIsSpecial", "endIsSpecial"} {
		if _, exists := line[field]; exists {
			return fmt.Errorf("line has arrow-only routing field %s", field)
		}
	}
	line["type"] = "arrow"
	line["startArrowhead"] = nil
	line["endArrowhead"] = nil
	line["elbowed"] = false
	delete(line, "polygon")
	return nil
}

const boundTextPadding = 5.0

// positionBoundText computes persisted geometry only for closed shape
// containers. Linear labels are positioned dynamically by the patched Web
// renderer using browser-only path and font measurements; the CLI must not
// invent x/y values that look authoritative but differ from the live canvas.
func positionBoundText(text, container map[string]any) error {
	x, ok := finiteNumber(text["width"])
	if !ok || x < 0 {
		return errors.New("bound text width must be a finite non-negative number")
	}
	textWidth := x
	textHeight, ok := finiteNumber(text["height"])
	if !ok || textHeight < 0 {
		return errors.New("bound text height must be a finite non-negative number")
	}
	kind, _ := container["type"].(string)
	if kind == "arrow" {
		return errors.New("linear bound-text position requires browser-resolved geometry")
	}
	cx, ok := finiteNumber(container["x"])
	if !ok {
		return errors.New("container x must be finite")
	}
	cy, ok := finiteNumber(container["y"])
	if !ok {
		return errors.New("container y must be finite")
	}
	cw, ok := finiteNumber(container["width"])
	if !ok || cw < 0 {
		return errors.New("container width must be a finite non-negative number")
	}
	ch, ok := finiteNumber(container["height"])
	if !ok || ch < 0 {
		return errors.New("container height must be a finite non-negative number")
	}
	offsetX, offsetY := boundTextPadding, boundTextPadding
	maxWidth, maxHeight := cw-2*boundTextPadding, ch-2*boundTextPadding
	switch kind {
	case "ellipse":
		offsetX += cw / 2 * (1 - math.Sqrt2/2)
		offsetY += ch / 2 * (1 - math.Sqrt2/2)
		maxWidth = math.Round(cw/2*math.Sqrt2) - 2*boundTextPadding
		maxHeight = math.Round(ch/2*math.Sqrt2) - 2*boundTextPadding
	case "diamond":
		offsetX += cw / 4
		offsetY += ch / 4
		maxWidth = math.Round(cw/2) - 2*boundTextPadding
		maxHeight = math.Round(ch/2) - 2*boundTextPadding
	}
	text["x"] = cx + offsetX + (maxWidth-textWidth)/2
	text["y"] = cy + offsetY + (maxHeight-textHeight)/2
	return nil
}

func placeTextImmediatelyAfterContainer(text, container map[string]any, elements []map[string]any) error {
	containerIndex, ok := container["index"].(string)
	if !ok || containerIndex == "" {
		return errors.New("container index must be a nonempty string")
	}
	var upper *string
	for _, element := range elements {
		if element["isDeleted"] == true || element["id"] == text["id"] || element["id"] == container["id"] {
			continue
		}
		index, ok := element["index"].(string)
		if !ok || index <= containerIndex {
			continue
		}
		if upper == nil || index < *upper {
			candidate := index
			upper = &candidate
		}
	}
	generated, err := fracindex.GenerateKeyBetween(&containerIndex, upper)
	if err != nil {
		return fmt.Errorf("place bound text above container: %w", err)
	}
	text["index"] = generated
	return nil
}
func addReciprocalText(container map[string]any, textID string) error {
	raw := container["boundElements"]
	if raw == nil {
		container["boundElements"] = []any{map[string]any{"id": textID, "type": "text"}}
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return errors.New("container boundElements must be null or an array")
	}
	for _, v := range arr {
		m, ok := v.(map[string]any)
		if !ok {
			return errors.New("container boundElements is malformed")
		}
		if m["type"] == "text" {
			return fmt.Errorf("container already binds text %q", m["id"])
		}
	}
	container["boundElements"] = append(arr, map[string]any{"id": textID, "type": "text"})
	return nil
}
func removeReciprocalText(container map[string]any, textID string) error {
	arr, ok := container["boundElements"].([]any)
	if !ok {
		return fmt.Errorf("container does not reciprocally list text %q", textID)
	}
	out := make([]any, 0, len(arr))
	found := false
	for _, v := range arr {
		m, ok := v.(map[string]any)
		if !ok {
			return errors.New("container boundElements is malformed")
		}
		if m["id"] == textID && m["type"] == "text" {
			if found {
				return errors.New("duplicate text binding")
			}
			found = true
			continue
		}
		out = append(out, v)
	}
	if !found {
		return fmt.Errorf("container does not reciprocally list text %q", textID)
	}
	if len(out) == 0 {
		container["boundElements"] = nil
	} else {
		container["boundElements"] = out
	}
	return nil
}

func patchStructuralChanges(cmd *cobra.Command, f *cmdutil.Factory, docID string, s *sceneSnapshot, changed []map[string]any) error {
	patch := make([]any, 0, len(changed))
	for _, clone := range changed {
		original := findElement(s.Elements, clone["id"].(string))
		if reflect.DeepEqual(original, clone) {
			return fmt.Errorf("element %q would not change", clone["id"])
		}
		if err := bumpElement(clone); err != nil {
			return err
		}
		if err := validateFinalElement(clone); err != nil {
			return err
		}
		patch = append(patch, clone)
	}
	if _, err := buildStructuralGraph(mergeChangedElements(s.Elements, changed)); err != nil {
		return fmt.Errorf("mutation would produce an invalid structural graph: %w", err)
	}
	return patchScene(cmd, f, docID, s.BaseVersion, map[string]any{"elements": patch})
}
