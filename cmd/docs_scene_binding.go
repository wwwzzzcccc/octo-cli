package cmd

import (
	"errors"
	"fmt"
	"math"
	"reflect"

	"github.com/spf13/cobra"

	"github.com/Mininglamp-OSS/octo-cli/internal/cmdutil"
)

type bindingFlags struct {
	endpoint, elementID string
	focus, gap          float64
}

func registerSceneBindingCmds(element *cobra.Command, f *cmdutil.Factory) {
	bind := &cobra.Command{Use: "bind <docId> <arrowId>", Short: "Atomically bind one arrow endpoint to an element", Args: cobra.ExactArgs(2)}
	bo := &bindingFlags{}
	bind.Flags().StringVar(&bo.endpoint, "endpoint", "", "arrow endpoint: start | end (required)")
	bind.Flags().StringVar(&bo.elementID, "element-id", "", "target element id (required)")
	bind.Flags().Float64Var(&bo.focus, "focus", 0, "binding focus from -1 to 1 (required)")
	bind.Flags().Float64Var(&bo.gap, "gap", 0, "non-negative binding gap (required)")
	for _, flag := range []string{"endpoint", "element-id", "focus", "gap"} {
		_ = bind.MarkFlagRequired(flag)
	}
	bind.RunE = func(cmd *cobra.Command, args []string) error {
		if err := validateBindingFlags(bo); err != nil {
			return err
		}
		return runBindingMutation(cmd, f, args[0], args[1], bo, false)
	}

	unbind := &cobra.Command{Use: "unbind <docId> <arrowId>", Short: "Atomically clear one arrow endpoint and its reciprocal reference", Args: cobra.ExactArgs(2)}
	uo := &bindingFlags{}
	unbind.Flags().StringVar(&uo.endpoint, "endpoint", "", "arrow endpoint: start | end (required)")
	_ = unbind.MarkFlagRequired("endpoint")
	unbind.RunE = func(cmd *cobra.Command, args []string) error {
		if !oneOf(uo.endpoint, "start", "end") {
			return errors.New("--endpoint must be start or end")
		}
		return runBindingMutation(cmd, f, args[0], args[1], uo, true)
	}
	element.AddCommand(bind, unbind)
}

func validateBindingFlags(o *bindingFlags) error {
	if !oneOf(o.endpoint, "start", "end") {
		return errors.New("--endpoint must be start or end")
	}
	if o.elementID == "" {
		return errors.New("--element-id must not be empty")
	}
	if !isFinite(o.focus) || o.focus < -1 || o.focus > 1 {
		return errors.New("--focus must be finite and between -1 and 1")
	}
	if !isFinite(o.gap) || o.gap < 0 || o.gap > 1e7 {
		return errors.New("--gap must be finite, non-negative, and at most 10000000")
	}
	return nil
}

func runBindingMutation(cmd *cobra.Command, f *cmdutil.Factory, docID, arrowID string, o *bindingFlags, unbind bool) error {
	s, err := getScene(cmd, f, docID)
	if err != nil {
		return err
	}
	// Validate the complete structural graph before touching it. This catches
	// malformed, dangling, contradictory, duplicate, and wrong-type references.
	if _, err := buildStructuralGraph(s.Elements); err != nil {
		return err
	}
	arrow := findElement(s.Elements, arrowID)
	if arrow == nil || arrow["isDeleted"] == true {
		return fmt.Errorf("live arrow %q not found", arrowID)
	}
	if arrow["type"] != "arrow" {
		return fmt.Errorf("element %q is not an arrow", arrowID)
	}
	if !unbind {
		if err := rejectOneSidedArrowOwners(arrow, s.Elements); err != nil {
			return err
		}
	}
	field := o.endpoint + "Binding"
	arrowClone := cloneMap(arrow)
	changed := []map[string]any{arrowClone}
	if unbind {
		targetID, err := strictEndpointTarget(arrow, field, s.Elements)
		if err != nil {
			return err
		}
		if targetID == "" {
			return fmt.Errorf("arrow %q %s is not bound", arrowID, o.endpoint)
		}
		target := findElement(s.Elements, targetID)
		targetClone := cloneMap(target)
		if err := removeReciprocalArrow(targetClone, arrowID); err != nil {
			return err
		}
		arrowClone[field] = nil
		changed = append(changed, targetClone)
	} else {
		if existing, err := strictEndpointTarget(arrow, field, s.Elements); err != nil {
			return err
		} else if existing != "" {
			return fmt.Errorf("arrow %q %s is already bound to %q; unbind it first", arrowID, o.endpoint, existing)
		}
		target := findElement(s.Elements, o.elementID)
		if target == nil || target["isDeleted"] == true {
			return fmt.Errorf("target element %q is not live", o.elementID)
		}
		if _, err := validateBindingTarget(target, arrowID); err != nil {
			return err
		}
		otherField := "startBinding"
		if field == otherField {
			otherField = "endBinding"
		}
		if other, err := strictEndpointTarget(arrow, otherField, s.Elements); err != nil {
			return err
		} else if other == o.elementID {
			return fmt.Errorf("arrow %q already binds target %q at its other endpoint", arrowID, o.elementID)
		}
		targetClone := cloneMap(target)
		if err := addReciprocalArrow(targetClone, arrowID); err != nil {
			return err
		}
		arrowClone[field] = map[string]any{"elementId": o.elementID, "focus": o.focus, "gap": o.gap}
		changed = append(changed, targetClone)
	}
	patch := make([]any, 0, len(changed))
	for _, clone := range changed {
		original := findElement(s.Elements, clone["id"].(string))
		if reflect.DeepEqual(clone, original) {
			return errors.New("binding mutation would not change the scene")
		}
		if err := bumpElement(clone); err != nil {
			return err
		}
		if err := validateFinalElement(clone); err != nil {
			return err
		}
		patch = append(patch, clone)
	}
	finalElements := mergeChangedElements(s.Elements, changed)
	if _, err := buildStructuralGraph(finalElements); err != nil {
		return fmt.Errorf("binding mutation would produce an invalid structural graph: %w", err)
	}
	return patchScene(cmd, f, docID, s.BaseVersion, map[string]any{"elements": patch})
}

func rejectOneSidedArrowOwners(arrow map[string]any, all []map[string]any) error {
	arrowID := arrow["id"].(string)
	endpoints := map[string]bool{}
	for _, field := range []string{"startBinding", "endBinding"} {
		if raw, ok := arrow[field].(map[string]any); ok {
			if id, ok := raw["elementId"].(string); ok && id != "" {
				endpoints[id] = true
			}
		}
	}
	for _, owner := range all {
		if owner["isDeleted"] == true {
			continue
		}
		entries, _ := owner["boundElements"].([]any)
		for _, raw := range entries {
			entry, _ := raw.(map[string]any)
			if entry["type"] == "arrow" && entry["id"] == arrowID && !endpoints[owner["id"].(string)] {
				return fmt.Errorf("owner %q lists arrow %q without a reciprocal endpoint binding; refusing to mutate one-sided state", owner["id"], arrowID)
			}
		}
	}
	return nil
}

func mergeChangedElements(all, changed []map[string]any) []map[string]any {
	byID := make(map[string]map[string]any, len(changed))
	for _, e := range changed {
		byID[e["id"].(string)] = e
	}
	out := make([]map[string]any, len(all))
	for i, e := range all {
		if id, ok := e["id"].(string); ok {
			if replacement := byID[id]; replacement != nil {
				out[i] = replacement
				continue
			}
		}
		out[i] = e
	}
	return out
}

func strictEndpointTarget(arrow map[string]any, field string, all []map[string]any) (string, error) {
	byID := map[string]map[string]any{}
	for _, e := range all {
		if e["isDeleted"] != true {
			byID[e["id"].(string)] = e
		}
	}
	target, err := arrowBindingTarget(arrow, field, byID, arrow["id"].(string))
	if err != nil || target == "" {
		return target, err
	}
	m := arrow[field].(map[string]any)
	if focus, ok := finiteNumber(m["focus"]); !ok || focus < -1 || focus > 1 {
		return "", fmt.Errorf("arrow %q %s focus must be finite and between -1 and 1", arrow["id"], field)
	}
	if gap, ok := finiteNumber(m["gap"]); !ok || gap < 0 || gap > 1e7 || math.IsNaN(gap) {
		return "", fmt.Errorf("arrow %q %s gap must be finite, non-negative, and at most 10000000", arrow["id"], field)
	}
	return target, nil
}

func validateBindingTarget(target map[string]any, arrowID string) (string, error) {
	id := target["id"].(string)
	if id == arrowID {
		return "", errors.New("an arrow cannot bind to itself")
	}
	kind, _ := target["type"].(string)
	if !isBindableTarget(kind) {
		return "", fmt.Errorf("target %q has non-bindable type %q", id, kind)
	}
	if kind == "text" {
		container, err := structuralRef(target, "containerId")
		if err != nil {
			return "", err
		}
		if container != "" {
			return "", fmt.Errorf("text %q is bound to container %q; bind the container instead", id, container)
		}
	}
	return id, nil
}

func addReciprocalArrow(target map[string]any, arrowID string) error {
	raw := target["boundElements"]
	if raw == nil {
		target["boundElements"] = []any{map[string]any{"id": arrowID, "type": "arrow"}}
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return errors.New("target boundElements must be null or an array")
	}
	for _, rawEntry := range arr {
		entry := rawEntry.(map[string]any)
		if entry["id"] == arrowID {
			return fmt.Errorf("target already lists arrow %q; refusing to overwrite conflicting one-sided state", arrowID)
		}
	}
	target["boundElements"] = append(arr, map[string]any{"id": arrowID, "type": "arrow"})
	return nil
}

func removeReciprocalArrow(target map[string]any, arrowID string) error {
	arr, ok := target["boundElements"].([]any)
	if !ok {
		return fmt.Errorf("target %q does not reciprocally list arrow %q", target["id"], arrowID)
	}
	out := make([]any, 0, len(arr))
	found := false
	for _, raw := range arr {
		entry := raw.(map[string]any)
		if entry["id"] == arrowID {
			if entry["type"] != "arrow" || found {
				return fmt.Errorf("target %q has a malformed reciprocal arrow reference", target["id"])
			}
			found = true
			continue
		}
		out = append(out, raw)
	}
	if !found {
		return fmt.Errorf("target %q does not reciprocally list arrow %q", target["id"], arrowID)
	}
	if len(out) == 0 {
		target["boundElements"] = nil
	} else {
		target["boundElements"] = out
	}
	return nil
}
