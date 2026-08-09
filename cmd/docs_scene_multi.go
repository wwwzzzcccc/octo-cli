package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Mininglamp-OSS/octo-cli/internal/cmdutil"
	"github.com/Mininglamp-OSS/octo-cli/internal/fracindex"
)

// registerSceneMultiCmds adds the multi-selection element commands. They share
// the same GET → validate-all → mutate-all → bump-each → single PATCH discipline
// as the single-element commands, but operate atomically over a set of ids
// supplied via repeated --id. The positional single-<elementId> UX of transform,
// style, and layer is left untouched; the batch forms are explicit *-many verbs.
func registerSceneMultiCmds(element *cobra.Command, f *cmdutil.Factory) {
	idsFlag := func(c *cobra.Command, o *sceneFlags, help string) {
		c.Flags().StringArrayVar(&o.ids, "id", nil, help)
	}

	// group
	group := func() *cobra.Command {
		o := &sceneFlags{}
		c := &cobra.Command{Use: "group <docId>", Short: "Group two or more elements under a new (or given) group id", Args: cobra.ExactArgs(1)}
		idsFlag(c, o, "element id to include (repeatable; at least two distinct live elements)")
		c.Flags().StringVar(&o.groupID, "group-id", "", "group id to apply (generated when omitted)")
		c.RunE = func(cmd *cobra.Command, a []string) error {
			if err := validateSelection(o.ids, 2); err != nil {
				return err
			}
			provided := cmd.Flags().Changed("group-id")
			if provided {
				if err := validateGroupID(o.groupID); err != nil {
					return err
				}
			}
			return runSceneMultiMutation(cmd, f, a[0], o.ids, func(sel []map[string]any, s *sceneSnapshot) error {
				return applyGroup(sel, s.Elements, o.groupID, provided)
			})
		}
		return c
	}()

	// ungroup
	ungroup := func() *cobra.Command {
		o := &sceneFlags{}
		c := &cobra.Command{Use: "ungroup <docId>", Short: "Remove a shared group from the selected elements", Args: cobra.ExactArgs(1)}
		idsFlag(c, o, "element id to ungroup (repeatable; at least one live element)")
		c.Flags().StringVar(&o.groupID, "group-id", "", "group id to remove (defaults to the single common group)")
		c.RunE = func(cmd *cobra.Command, a []string) error {
			if err := validateSelection(o.ids, 1); err != nil {
				return err
			}
			provided := cmd.Flags().Changed("group-id")
			if provided {
				if err := validateGroupID(o.groupID); err != nil {
					return err
				}
			}
			return runSceneMultiMutation(cmd, f, a[0], o.ids, func(sel []map[string]any, _ *sceneSnapshot) error {
				return applyUngroup(sel, o.groupID, provided)
			})
		}
		return c
	}()

	// transform-many
	transformMany := func() *cobra.Command {
		o := &sceneFlags{}
		c := &cobra.Command{Use: "transform-many <docId>", Short: "Apply one geometry change to every selected element", Args: cobra.ExactArgs(1)}
		idsFlag(c, o, "element id to transform (repeatable; at least one live element)")
		bindTransformFlags(c, o)
		c.RunE = func(cmd *cobra.Command, a []string) error {
			if err := validateSelection(o.ids, 1); err != nil {
				return err
			}
			if err := validateTransformInput(cmd, o); err != nil {
				return err
			}
			return runSceneMultiMutation(cmd, f, a[0], o.ids, func(sel []map[string]any, _ *sceneSnapshot) error {
				if err := prepareWholeSelectionTransform(cmd, sel, o); err != nil {
					return err
				}
				for _, e := range sel {
					if err := applyChangedNumbers(cmd, e, o); err != nil {
						return err
					}
				}
				return nil
			})
		}
		return c
	}()

	// style-many
	styleMany := func() *cobra.Command {
		o := &sceneFlags{}
		c := &cobra.Command{Use: "style-many <docId>", Short: "Apply one appearance change to every selected element", Args: cobra.ExactArgs(1)}
		idsFlag(c, o, "element id to style (repeatable; at least one live element)")
		bindStyleFlags(c, o)
		c.RunE = func(cmd *cobra.Command, a []string) error {
			if err := validateSelection(o.ids, 1); err != nil {
				return err
			}
			if err := validateStyleInput(cmd, o); err != nil {
				return err
			}
			return runSceneMultiMutation(cmd, f, a[0], o.ids, func(sel []map[string]any, _ *sceneSnapshot) error {
				for _, e := range sel {
					if err := applyChangedStyle(cmd, e, o); err != nil {
						return err
					}
				}
				return nil
			})
		}
		return c
	}()

	// layer-many
	layerMany := func() *cobra.Command {
		o := &sceneFlags{}
		c := &cobra.Command{Use: "layer-many <docId>", Short: "Move a set of elements together in z-order, preserving their relative order", Args: cobra.ExactArgs(1)}
		idsFlag(c, o, "element id to move (repeatable; at least one live element)")
		c.Flags().StringVar(&o.position, "position", "front", "front | back | before | after")
		c.Flags().StringVar(&o.relativeTo, "relative-to", "", "reference (anchor) element id for before/after")
		c.RunE = func(cmd *cobra.Command, a []string) error {
			if err := validateSelection(o.ids, 1); err != nil {
				return err
			}
			if err := validateLayerInput(o); err != nil {
				return err
			}
			// Reject an anchor that is itself part of the moving set before the GET.
			if o.position == "before" || o.position == "after" {
				for _, id := range o.ids {
					if id == o.relativeTo {
						return errors.New("--relative-to cannot reference a selected element")
					}
				}
			}
			return runSceneMultiMutation(cmd, f, a[0], o.ids, func(sel []map[string]any, s *sceneSnapshot) error {
				return applyLayerMany(sel, s.Elements, o.position, o.relativeTo)
			})
		}
		return c
	}()

	element.AddCommand(group, ungroup, transformMany, styleMany, layerMany)
}

// validateSelection enforces the shared pre-GET contract for a repeated --id
// selection: a minimum count and no empty or duplicate ids. Doing it before the
// GET keeps malformed input from issuing a request.
func validateSelection(ids []string, min int) error {
	if len(ids) < min {
		if min == 1 {
			return errors.New("at least one --id is required")
		}
		return fmt.Errorf("at least %d --id values are required", min)
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return errors.New("--id must not be empty")
		}
		if seen[id] {
			return fmt.Errorf("--id %q is repeated", id)
		}
		seen[id] = true
	}
	return nil
}

// runSceneMultiMutation is the multi-element analogue of runSceneMutation: one
// GET, all-or-nothing resolution of the selection, a fail-closed structural
// completeness check, an in-memory mutation over clones, a per-element version
// bump + final validation, and a single PATCH carrying the set under If-Match.
// Every selected element must materially change; a no-op or malformed member
// fails the whole batch before any PATCH, so a selection never half-applies.
func runSceneMultiMutation(cmd *cobra.Command, f *cmdutil.Factory, docID string, ids []string, mutate func([]map[string]any, *sceneSnapshot) error) error {
	s, err := getScene(cmd, f, docID)
	if err != nil {
		return err
	}
	selected, err := resolveSelection(s.Elements, ids)
	if err != nil {
		return err
	}
	if err := assertSelectionClosed(s.Elements, ids); err != nil {
		return err
	}
	clones := make([]map[string]any, len(selected))
	for i, e := range selected {
		clones[i] = cloneMap(e)
	}
	if err := mutate(clones, s); err != nil {
		if errors.Is(err, errLayerNoOp) {
			return emitLayerNoOp(f)
		}
		return err
	}
	changed := make([]any, 0, len(clones))
	for i, c := range clones {
		if reflect.DeepEqual(c, selected[i]) {
			return fmt.Errorf("element %q would not change; every selected element must be materially modified", selected[i]["id"])
		}
		if err := bumpElement(c); err != nil {
			return err
		}
		if err := validateFinalElement(c); err != nil {
			return err
		}
		changed = append(changed, c)
	}
	if len(changed) == 0 {
		return errors.New("mutation would not change any element")
	}
	return patchScene(cmd, f, docID, s.BaseVersion, map[string]any{"elements": changed})
}

// resolveSelection maps ids to their live scene elements, all-or-nothing. A
// missing id, a repeated id, or a tombstoned target is a hard error — the whole
// batch fails before any element is mutated. Duplicate live scene ids are already
// rejected by getScene.
func resolveSelection(elements []map[string]any, ids []string) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" {
			return nil, errors.New("--id must not be empty")
		}
		if seen[id] {
			return nil, fmt.Errorf("--id %q is repeated", id)
		}
		seen[id] = true
		e := findElement(elements, id)
		if e == nil {
			return nil, fmt.Errorf("element %q not found", id)
		}
		if e["isDeleted"] == true {
			return nil, fmt.Errorf("element %q is deleted and cannot be mutated", id)
		}
		out = append(out, e)
	}
	return out, nil
}

// assertSelectionClosed enforces the fail-closed structural contract shared by
// every multi-element command: the explicit selection must already contain every
// live member of each structural unit any selected element belongs to. Units are
// the connected components of the strictly-validated structural graph
// (buildStructuralGraph): shared group membership, frame containment, and
// container/bound-text binding.
//
// The selection is never auto-expanded: if a selected element's unit has a live
// member outside the selection, the whole batch fails before any mutation. This
// keeps groups, frames, and container/bound-text pairs from being silently split.
func assertSelectionClosed(all []map[string]any, ids []string) error {
	uf, err := buildStructuralGraph(all)
	if err != nil {
		return err
	}
	selected := make(map[string]bool, len(ids))
	for _, id := range ids {
		selected[id] = true
	}
	// For every selected element, every other live member of its unit must be
	// selected too. Report the first gap in a stable order (selection order,
	// then scene order) so the failure is deterministic.
	for _, id := range ids {
		root := uf.find(id)
		for _, e := range all {
			if e["isDeleted"] == true {
				continue
			}
			oid, ok := e["id"].(string)
			if !ok || oid == "" || selected[oid] {
				continue
			}
			if uf.find(oid) == root {
				return fmt.Errorf("element %q belongs to a structural unit (shared group, frame, or container-bound text) whose live member %q is not selected; add every member of the unit to --id or operate on it separately", id, oid)
			}
		}
	}
	return nil
}

// structuralRef reads an optional string reference field (frameId/containerId).
// Absent or JSON null yields ("", nil) — no reference. A present value must be a
// nonempty string; an empty string or a non-string value is malformed and
// rejected fail-closed.
func structuralRef(e map[string]any, field string) (string, error) {
	raw, ok := e[field]
	if !ok || raw == nil {
		return "", nil
	}
	s, ok := raw.(string)
	if !ok || s == "" {
		return "", fmt.Errorf("%s must be absent, null, or a nonempty string", field)
	}
	return s, nil
}

// buildStructuralGraph validates every live element's structural references and
// returns the disjoint-set of live elements induced by three relations: shared
// group membership, frame containment, and container/bound-text binding. It is
// the single source of structural semantics reused by both assertSelectionClosed
// (closure) and applyLayerMany (component-atomic z-order moves).
//
// Validation is strict and fail-closed (requirement 2): for every live element
//   - frameId/containerId is absent, null, or a nonempty string; a present ref
//     must resolve to a live element of a compatible type — a frameId must point
//     at a frame, and a containerId is carried only by text and must point at a
//     non-text container;
//   - boundElements is absent, null, or a []any of objects, each with a nonempty
//     string id and a supported type (text or arrow) whose ref resolves to a live
//     element of the matching type. The owner must be compatible with the ref it
//     lists: a type=text entry is allowed only on a valid text container
//     (rectangle/ellipse/diamond/arrow — the same helper as a containerId target),
//     and a type=arrow entry only on a valid arrow-binding target (isBindableTarget),
//     where a text owner is acceptable only when unbound (a bound label text may not
//     own an arrow ref);
//   - reciprocal container/bound-text declarations must agree — a text and its
//     container may each name the other, only one side need be present (union
//     from whichever side exists), but if both sides name a partner they must
//     name each other, a container binds at most one text, and a text is claimed
//     by at most one owner (even when it carries no containerId — two distinct
//     owners listing the same text is contradictory).
//
// Malformed, dangling, or contradictory references are rejected. startBinding/
// endBinding are arrow-only — a non-arrow live element carrying a non-null
// endpoint binding is rejected fail-closed. A live arrow has two endpoints, so at
// most two distinct owners may claim it via boundElements (even when the arrow's
// own endpoint bindings are absent/null); a third claimant is rejected. Only text
// bindings are unioned into a component; a boundElements arrow ref is validated
// but never unioned — an arrow bound to a shape is not part of its structural
// unit.
func buildStructuralGraph(all []map[string]any) (*unionFind, error) {
	// Every live element must be validly and uniquely identified before we index
	// it by id: a missing/non-string/empty id or a duplicate live id makes the
	// byID map lossy or ambiguous, and a bound reference could then resolve to the
	// wrong element (or silently to none). Reject fail-closed via the shared
	// validator rather than silently skipping a malformed live element.
	if err := checkDuplicateLiveIDs(all); err != nil {
		return nil, err
	}
	byID := make(map[string]map[string]any, len(all))
	live := make([]map[string]any, 0, len(all))
	for _, e := range all {
		if e["isDeleted"] == true {
			continue
		}
		id, ok := e["id"].(string)
		if !ok || id == "" {
			// Unreachable after checkDuplicateLiveIDs above, but keep the strict
			// guard so the live-id invariant cannot be bypassed if the shared call
			// is ever refactored away — a live element is never silently skipped.
			return nil, errors.New("live element has a missing, non-string, or empty id")
		}
		live = append(live, e)
		byID[id] = e
	}

	uf := newUnionFind()

	// 1. shared group membership: union every live element sharing a group id.
	groupRep := map[string]string{}
	for _, e := range live {
		gids, err := elementGroupIDs(e)
		if err != nil {
			return nil, fmt.Errorf("element %v: %w", e["id"], err)
		}
		id := e["id"].(string)
		for _, g := range gids {
			if rep, ok := groupRep[g]; ok {
				uf.union(rep, id)
			} else {
				groupRep[g] = id
			}
		}
	}

	// 2. frame containment: frameId must resolve to a live frame element, and the
	// frameId graph must be acyclic. A frame may nest inside another frame, so the
	// parent pointers form a chain that must terminate — a self-reference or any
	// longer ancestry cycle is corrupt and is rejected BEFORE any union (a cycle
	// would otherwise collapse into a single component silently). Each element has
	// at most one frameId, so the graph is functional and a walk that revisits a
	// node has found the whole cycle.
	frameParent := map[string]string{}
	for _, e := range live {
		id := e["id"].(string)
		f, err := structuralRef(e, "frameId")
		if err != nil {
			return nil, fmt.Errorf("element %q: %w", id, err)
		}
		if f == "" {
			continue
		}
		if f == id {
			return nil, fmt.Errorf("element %q references itself as its frameId", id)
		}
		target, ok := byID[f]
		if !ok {
			return nil, fmt.Errorf("element %q references frameId %q which is not a live element", id, f)
		}
		if target["type"] != "frame" {
			return nil, fmt.Errorf("element %q frameId %q does not reference a frame element", id, f)
		}
		frameParent[id] = f
	}
	// Walk each parent chain (in a deterministic order) and reject the first cycle.
	starts := make([]string, 0, len(frameParent))
	for id := range frameParent {
		starts = append(starts, id)
	}
	sort.Strings(starts)
	for _, start := range starts {
		seen := map[string]bool{start: true}
		for cur := start; ; {
			next, ok := frameParent[cur]
			if !ok {
				break
			}
			if seen[next] {
				return nil, fmt.Errorf("element %q is part of a frameId ancestry cycle", next)
			}
			seen[next] = true
			cur = next
		}
	}
	for id, f := range frameParent {
		uf.union(id, f)
	}

	// 3. container/bound-text binding, validated reciprocally.
	//   declaredContainer[textID] = containerID   (from the containerId side)
	//   boundText[containerID]    = textID         (from the boundElements side)
	//   textOwner[textID]         = ownerID        (reverse of boundText: which
	//     owner claimed a text via boundElements(type=text); catches two distinct
	//     owners binding the same text even when the text carries no containerId).
	declaredContainer := map[string]string{}
	boundText := map[string]string{}
	textOwner := map[string]string{}
	// containerArrows[arrowID] = containers whose boundElements list that arrow.
	// Recorded but never unioned; cross-checked against the arrow's own endpoint
	// bindings below so a container may not claim an arrow the arrow disowns, and
	// bounded to at most two distinct owners (a live arrow has two endpoints).
	containerArrows := map[string][]string{}
	for _, e := range live {
		id := e["id"].(string)
		c, err := structuralRef(e, "containerId")
		if err != nil {
			return nil, fmt.Errorf("element %q: %w", id, err)
		}
		if c != "" {
			if e["type"] != "text" {
				return nil, fmt.Errorf("element %q carries containerId but is not a text element", id)
			}
			target, ok := byID[c]
			if !ok {
				return nil, fmt.Errorf("element %q references containerId %q which is not a live element", id, c)
			}
			tk, _ := target["type"].(string)
			if !isValidTextContainer(tk) {
				return nil, fmt.Errorf("element %q containerId %q references a %q, which cannot contain bound text (only rectangle, ellipse, diamond, or arrow may)", id, c, tk)
			}
			declaredContainer[id] = c
			uf.union(id, c)
		}

		raw, ok := e["boundElements"]
		if !ok || raw == nil {
			continue
		}
		be, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("element %q boundElements must be absent, null, or an array", id)
		}
		// A single owner may not list the same bound id twice — even an exactly
		// identical duplicate entry is a corrupt binding list and is rejected.
		seenBound := make(map[string]bool, len(be))
		for i, entry := range be {
			m, ok := entry.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("element %q boundElements[%d] must be an object", id, i)
			}
			rid, ok := m["id"].(string)
			if !ok || rid == "" {
				return nil, fmt.Errorf("element %q boundElements[%d] must have a nonempty string id", id, i)
			}
			if seenBound[rid] {
				return nil, fmt.Errorf("element %q boundElements lists id %q more than once", id, rid)
			}
			seenBound[rid] = true
			t, ok := m["type"].(string)
			if !ok || !oneOf(t, "text", "arrow") {
				return nil, fmt.Errorf("element %q boundElements[%d] has unsupported type %v", id, i, m["type"])
			}
			target, ok := byID[rid]
			if !ok {
				return nil, fmt.Errorf("element %q boundElements[%d] references %q which is not a live element", id, i, rid)
			}
			if target["type"] != t {
				return nil, fmt.Errorf("element %q boundElements[%d] declares type %q but target %q is a %v", id, i, t, rid, target["type"])
			}
			if t == "arrow" {
				// The owner claims an arrow binds to it, so the owner must be a
				// valid arrow-binding target under the same endpoint-compatibility
				// contract as arrowBindingTarget (isBindableTarget). A text owner is
				// acceptable only when unbound — a bound label text may not own an
				// arrow ref; the arrow must bind the container, not its label.
				ownerType, _ := e["type"].(string)
				if !isBindableTarget(ownerType) {
					return nil, fmt.Errorf("element %q lists arrow %q in boundElements but its type %q is not a valid arrow-binding target", id, rid, ownerType)
				}
				if ownerType == "text" {
					cid, err := structuralRef(e, "containerId")
					if err != nil {
						return nil, fmt.Errorf("element %q: %w", id, err)
					}
					if cid != "" {
						return nil, fmt.Errorf("text %q is bound to container %q and must not own an arrow ref; bind the arrow to the container, not its label text", id, cid)
					}
				}
				// Arrows may legitimately be bound to a shape; validate the ref but
				// never union — the arrow is not part of the shape's structural unit.
				// Record the claim for the reciprocal endpoint consistency check.
				containerArrows[rid] = append(containerArrows[rid], id)
				continue
			}
			// t == "text": the owner must be a valid text container (the same helper
			// as a containerId target: rectangle/ellipse/diamond/arrow), and a text
			// may be claimed by at most one owner — even when it carries no
			// containerId, two distinct owners listing it is contradictory.
			ownerType, _ := e["type"].(string)
			if !isValidTextContainer(ownerType) {
				return nil, fmt.Errorf("element %q lists text %q in boundElements but is a %q, which cannot contain bound text (only rectangle, ellipse, diamond, or arrow may)", id, rid, ownerType)
			}
			if prev, ok := textOwner[rid]; ok && prev != id {
				return nil, fmt.Errorf("text %q is bound by more than one owner (%q and %q)", rid, prev, id)
			}
			textOwner[rid] = id
			if prev, ok := boundText[id]; ok && prev != rid {
				return nil, fmt.Errorf("element %q binds more than one text (%q and %q)", id, prev, rid)
			}
			boundText[id] = rid
			uf.union(id, rid)
		}
	}

	// Reciprocity. Two distinct texts may not claim the same container; and when
	// both sides of a binding are present they must name each other.
	claimants := map[string][]string{}
	for text, container := range declaredContainer {
		claimants[container] = append(claimants[container], text)
	}
	for container, texts := range claimants {
		if len(texts) > 1 {
			sort.Strings(texts)
			return nil, fmt.Errorf("container %q is claimed by more than one text (%s)", container, strings.Join(texts, ", "))
		}
	}
	for container, text := range boundText {
		if dc, ok := declaredContainer[text]; ok && dc != container {
			return nil, fmt.Errorf("text %q is bound by container %q but declares container %q", text, container, dc)
		}
	}
	for text, container := range declaredContainer {
		if bt, ok := boundText[container]; ok && bt != text {
			return nil, fmt.Errorf("text %q declares container %q which binds a different text %q", text, container, bt)
		}
	}

	// 4. arrow endpoint bindings. startBinding/endBinding are arrow-only: a
	// non-arrow live element carrying a non-null endpoint binding (any shape) is
	// corrupt and rejected fail-closed. A live arrow's startBinding/endBinding,
	// when present, must resolve to a live, non-arrow, bindable target and never to
	// the arrow itself. Arrows are validated but NEVER unioned — an arrow bound to
	// a shape is not part of that shape's structural unit. When both sides of a
	// container/arrow relation are present (the container lists the arrow AND the
	// arrow carries endpoint bindings), they must agree: every container that lists
	// arrow A must be one of A's endpoint targets. The reverse is not required — an
	// arrow may point at a container the container does not list back (a valid
	// one-sided relation).
	arrowEndpoints := map[string]map[string]bool{}
	for _, e := range live {
		id := e["id"].(string)
		if e["type"] != "arrow" {
			// startBinding/endBinding are arrow-only fields. A non-arrow live
			// element carrying a non-null endpoint binding — of ANY shape (a well-
			// formed object, a bare string, or otherwise malformed value) — is
			// corrupt and is rejected fail-closed before any PATCH, rather than
			// silently ignoring the stray field. Only arrow owners are inspected by
			// the strict endpoint validation below.
			for _, field := range []string{"startBinding", "endBinding"} {
				if v, ok := e[field]; ok && v != nil {
					return nil, fmt.Errorf("element %q is a %v but carries a non-null %s; startBinding/endBinding are valid only on arrow elements", id, e["type"], field)
				}
			}
			continue
		}
		eps := map[string]bool{}
		for _, field := range []string{"startBinding", "endBinding"} {
			target, err := arrowBindingTarget(e, field, byID, id)
			if err != nil {
				return nil, err
			}
			if target != "" {
				eps[target] = true
			}
		}
		if len(eps) > 0 {
			arrowEndpoints[id] = eps
		}
	}
	for arrowID, containers := range containerArrows {
		// A live arrow has at most two endpoints (startBinding/endBinding), so at
		// most two distinct owners may legitimately claim it via boundElements.
		// Reject a third-or-more owner even when the arrow carries no endpoint
		// bindings of its own (absent/null): the claim set is physically
		// impossible regardless of whether the arrow names anyone back. Owners are
		// already distinct here (each owner lists an arrow at most once, and live
		// ids are unique).
		if len(containers) > 2 {
			sorted := append([]string(nil), containers...)
			sort.Strings(sorted)
			return nil, fmt.Errorf("arrow %q is claimed by %d owners (%s) in boundElements, but a live arrow has at most two endpoints, so at most two owners may bind it", arrowID, len(containers), strings.Join(sorted, ", "))
		}
		eps, bound := arrowEndpoints[arrowID]
		if !bound {
			// One-sided: the container lists the arrow but the arrow carries no
			// endpoint bindings. Valid — nothing to reconcile.
			continue
		}
		for _, c := range containers {
			if !eps[c] {
				return nil, fmt.Errorf("container %q lists arrow %q in boundElements, but the arrow's endpoints do not bind back to it (contradictory reference)", c, arrowID)
			}
		}
	}

	return uf, nil
}

// isValidTextContainer reports whether an element of the given type may be the
// container of a bound text element (the target of a text's containerId). It
// mirrors Excalidraw's isValidTextContainer, restricted to the element types this
// project supports (see validateFinalElement): the closed shapes (rectangle,
// ellipse, diamond) and arrows (labelled arrows). A line, freedraw, image, frame,
// embeddable, another text, or an unknown type cannot hold bound text.
func isValidTextContainer(kind string) bool {
	switch kind {
	case "rectangle", "ellipse", "diamond", "arrow":
		return true
	}
	return false
}

// isBindableTarget reports whether an element of the given type may be the target
// of an arrow's start/end binding. It mirrors Excalidraw's isBindableElement,
// restricted to the element types this project supports (see validateFinalElement):
// the standard closed shapes (rectangle, ellipse, diamond), text, image, frame,
// and embeddable. Linear elements (arrow, line) and freedraw are not bindable
// targets — an arrow never binds to another arrow or a stroke.
func isBindableTarget(kind string) bool {
	switch kind {
	case "rectangle", "ellipse", "diamond", "text", "image", "frame", "embeddable":
		return true
	}
	return false
}

// arrowBindingTarget validates one endpoint binding (startBinding/endBinding) of a
// live arrow and returns the bound element id, or "" when the binding is absent or
// null. A present binding must be an object carrying a nonempty string elementId
// that resolves to a live, non-arrow, bindable target and is not the arrow itself.
// Malformed, dangling, self, or non-bindable references are rejected fail-closed.
func arrowBindingTarget(e map[string]any, field string, byID map[string]map[string]any, selfID string) (string, error) {
	raw, ok := e[field]
	if !ok || raw == nil {
		return "", nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return "", fmt.Errorf("arrow %q %s must be absent, null, or an object", selfID, field)
	}
	eid, ok := m["elementId"].(string)
	if !ok || eid == "" {
		return "", fmt.Errorf("arrow %q %s must carry a nonempty string elementId", selfID, field)
	}
	if eid == selfID {
		return "", fmt.Errorf("arrow %q %s binds to itself", selfID, field)
	}
	target, ok := byID[eid]
	if !ok {
		return "", fmt.Errorf("arrow %q %s references %q which is not a live element", selfID, field, eid)
	}
	tk, _ := target["type"].(string)
	if !isBindableTarget(tk) {
		return "", fmt.Errorf("arrow %q %s references %q whose type %q is not a bindable target", selfID, field, eid, tk)
	}
	// Unbound text is a legitimate arrow endpoint, but a text that is itself
	// bound inside a container (non-null containerId) is a label, not a free
	// binding target — the arrow must bind the container, not its label text.
	if tk == "text" {
		cid, err := structuralRef(target, "containerId")
		if err != nil {
			return "", fmt.Errorf("arrow %q %s references text %q with an invalid containerId: %w", selfID, field, eid, err)
		}
		if cid != "" {
			return "", fmt.Errorf("arrow %q %s references text %q which is bound to container %q; bind the container, not its label text", selfID, field, eid, cid)
		}
	}
	return eid, nil
}

// unionFind is a minimal string-keyed disjoint-set with path compression, used
// only to derive the structural components of a scene during a batch.
type unionFind struct{ parent map[string]string }

func newUnionFind() *unionFind { return &unionFind{parent: map[string]string{}} }

func (u *unionFind) find(x string) string {
	if _, ok := u.parent[x]; !ok {
		u.parent[x] = x
	}
	root := x
	for u.parent[root] != root {
		root = u.parent[root]
	}
	for u.parent[x] != root {
		u.parent[x], x = root, u.parent[x]
	}
	return root
}

func (u *unionFind) union(a, b string) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[ra] = rb
	}
}

// groupIDRe bounds an accepted group id to the nanoid-style charset Excalidraw
// uses (letters, digits, hyphen, underscore). It keeps a --group-id from
// smuggling whitespace or arbitrary bytes onto the wire.
var groupIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func validateGroupID(id string) error {
	if id == "" {
		return errors.New("--group-id must not be empty")
	}
	if len(id) > 255 {
		return errors.New("--group-id must be at most 255 characters")
	}
	if !groupIDRe.MatchString(id) {
		return errors.New("--group-id must contain only letters, digits, hyphen, or underscore")
	}
	return nil
}

// elementGroupIDs extracts and validates an element's groupIds: it must be an
// array of unique non-empty strings (innermost-first). A malformed groupIds
// value is a hard error so the batch fails loud rather than corrupting nesting.
func elementGroupIDs(e map[string]any) ([]string, error) {
	raw, ok := e["groupIds"]
	if !ok || raw == nil {
		return nil, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, errors.New("groupIds must be an array")
	}
	out := make([]string, 0, len(arr))
	seen := make(map[string]bool, len(arr))
	for _, v := range arr {
		s, ok := v.(string)
		if !ok || s == "" {
			return nil, errors.New("groupIds must contain only non-empty strings")
		}
		if seen[s] {
			return nil, fmt.Errorf("groupIds contains duplicate group %q", s)
		}
		seen[s] = true
		out = append(out, s)
	}
	return out, nil
}

// generateGroupID mints a fresh group id that collides with no existing element
// id and no group id already present anywhere in the scene.
func generateGroupID(elements []map[string]any) string {
	taken := map[string]bool{}
	for _, e := range elements {
		if id, ok := e["id"].(string); ok {
			taken[id] = true
		}
		if gids, err := elementGroupIDs(e); err == nil {
			for _, g := range gids {
				taken[g] = true
			}
		}
	}
	for {
		id := "grp_" + randomID()
		if !taken[id] {
			return id
		}
	}
}

// applyGroup appends the group id (outermost) to every selected element's
// groupIds, preserving existing nested groups. A supplied --group-id is rejected
// when it collides with any element id (reserved) or already occurs in any live
// element's groupIds (reusing it would silently merge the selection into an
// existing group); an omitted one is generated fresh.
func applyGroup(selected, all []map[string]any, groupID string, provided bool) error {
	perElem := make([][]string, len(selected))
	for i, e := range selected {
		gids, err := elementGroupIDs(e)
		if err != nil {
			return fmt.Errorf("element %v: %w", e["id"], err)
		}
		perElem[i] = gids
	}
	gid := groupID
	if provided {
		for _, e := range all {
			if e["id"] == gid {
				return fmt.Errorf("--group-id %q collides with an existing element id", gid)
			}
			if e["isDeleted"] == true {
				continue
			}
			gids, err := elementGroupIDs(e)
			if err != nil {
				return fmt.Errorf("element %v: %w", e["id"], err)
			}
			if containsString(gids, gid) {
				return fmt.Errorf("--group-id %q is already in use by an existing group; choose an unused id", gid)
			}
		}
	} else {
		gid = generateGroupID(all)
	}
	for i, e := range selected {
		e["groupIds"] = toAnyStrings(append(perElem[i], gid))
	}
	return nil
}

// applyUngroup removes one group from every selected element. With an explicit
// --group-id, that group must be present on every selected element. Otherwise the
// selection's common groups are computed and the outermost one is removed
// deterministically — the last common group in the innermost→outermost groupIds
// order — rather than failing on ambiguity. Sharing zero common groups is still a
// fail-loud error. Outer and nested groups survive.
func applyUngroup(selected []map[string]any, groupID string, provided bool) error {
	perElem := make([][]string, len(selected))
	for i, e := range selected {
		gids, err := elementGroupIDs(e)
		if err != nil {
			return fmt.Errorf("element %v: %w", e["id"], err)
		}
		perElem[i] = gids
	}
	var target string
	if provided {
		target = groupID
		for i, gids := range perElem {
			if !containsString(gids, target) {
				return fmt.Errorf("element %v is not a member of group %q", selected[i]["id"], target)
			}
		}
	} else {
		// intersectStrings preserves the order of its first argument, so common
		// stays in perElem[0]'s innermost→outermost order; its last entry is the
		// outermost common group.
		common := append([]string(nil), perElem[0]...)
		for _, gids := range perElem[1:] {
			common = intersectStrings(common, gids)
		}
		if len(common) == 0 {
			return errors.New("selected elements share no common group; pass --group-id to choose which group to remove")
		}
		target = common[len(common)-1]
	}
	for i, e := range selected {
		e["groupIds"] = toAnyStrings(removeString(perElem[i], target))
	}
	return nil
}

// applyLayerMany relocates the selected elements as one contiguous z-order run,
// preserving each structural component as an atomic block. It validates every
// live element's index and rejects duplicate/corrupt indices, refuses an anchor
// inside the selection, and generates a fresh contiguous fractional-index run in
// the gap chosen by position (front/back/before/after).
//
// The moving run is built by grouping the selection into its structural
// connected components (buildStructuralGraph — the same graph the closure check
// uses): each component's members keep their original relative index order, and
// components are ordered by their earliest member's original index. This
// flattens interleaved source components into contiguous runs so two groups that
// were interleaved in z-order come out one-after-the-other. A selection of
// independent singletons reduces to a plain original-index sort — unchanged
// behavior.
func applyLayerMany(selected, all []map[string]any, position, relative string) error {
	sel := make(map[string]bool, len(selected))
	for _, e := range selected {
		id, _ := e["id"].(string)
		sel[id] = true
	}
	if position == "before" || position == "after" {
		if relative == "" {
			return errors.New("--relative-to is required for before/after")
		}
		if sel[relative] {
			return errors.New("--relative-to cannot reference a selected element")
		}
	} else if relative != "" {
		return errors.New("--relative-to is only valid for before/after")
	}

	// Validate + duplicate-check every live element's index (all-or-nothing).
	live := make([]map[string]any, 0, len(all))
	for _, e := range all {
		if e["isDeleted"] == true {
			continue
		}
		if _, err := elementIndex(e); err != nil {
			return err
		}
		live = append(live, e)
	}
	sort.Slice(live, func(i, j int) bool { return live[i]["index"].(string) < live[j]["index"].(string) })
	for i := 1; i < len(live); i++ {
		if live[i-1]["index"] == live[i]["index"] {
			return fmt.Errorf("elements %v and %v have duplicate index %q", live[i-1]["id"], live[i]["id"], live[i]["index"])
		}
	}

	// Others = live elements not in the selection, already in z-order.
	others := make([]map[string]any, 0, len(live))
	for _, e := range live {
		if id, _ := e["id"].(string); !sel[id] {
			others = append(others, e)
		}
	}

	// The moving run is the selection flattened by structural component. The
	// selection is already closed (assertSelectionClosed ran before mutation), so
	// every component is fully contained in the selection.
	uf, err := buildStructuralGraph(all)
	if err != nil {
		return err
	}
	byRoot := map[string][]map[string]any{}
	for _, e := range selected {
		id, _ := e["id"].(string)
		root := uf.find(id)
		byRoot[root] = append(byRoot[root], e)
	}
	type component struct {
		members []map[string]any
		minIdx  string
	}
	comps := make([]component, 0, len(byRoot))
	for _, members := range byRoot {
		sort.SliceStable(members, func(i, j int) bool {
			return members[i]["index"].(string) < members[j]["index"].(string)
		})
		comps = append(comps, component{members: members, minIdx: members[0]["index"].(string)})
	}
	// Order components by their earliest member's original index — a total,
	// deterministic order because live indices are unique (duplicates rejected).
	sort.SliceStable(comps, func(i, j int) bool { return comps[i].minIdx < comps[j].minIdx })
	ordered := make([]map[string]any, 0, len(selected))
	for _, c := range comps {
		ordered = append(ordered, c.members...)
	}

	// insertPos is the index in `others` at which the flattened moving run is
	// spliced: others[:insertPos] stay below the run, others[insertPos:] above.
	// front/back append/prepend; before/after treat the anchor's FULL structural
	// component atomically — before splices at the boundary just below the anchor
	// component's earliest live member, after just above its latest member, so the
	// run is never inserted into the middle of the anchor's component even when the
	// component is noncontiguous in a corrupt historical z-order.
	var insertPos int
	switch position {
	case "front":
		insertPos = len(others)
	case "back":
		insertPos = 0
	case "before", "after":
		root := uf.find(relative)
		found := false
		earliest, latest := -1, -1
		for i, e := range others {
			id, _ := e["id"].(string)
			if id == relative {
				found = true
			}
			if uf.find(id) == root {
				if earliest == -1 {
					earliest = i
				}
				latest = i
			}
		}
		if !found {
			return fmt.Errorf("reference element %q not found", relative)
		}
		if position == "before" {
			insertPos = earliest
		} else {
			insertPos = latest + 1
		}
	default:
		return errors.New("--position must be front, back, before, or after")
	}

	// Semantic no-op: if splicing the flattened run into `others` at insertPos
	// reproduces the current live z-order exactly, the operation changes nothing.
	// Return the no-op sentinel so the caller emits success without a churn PATCH.
	if layerOrderUnchanged(live, others, ordered, insertPos) {
		return errLayerNoOp
	}

	var lower, upper *string
	if insertPos > 0 {
		l := others[insertPos-1]["index"].(string)
		lower = &l
	}
	if insertPos < len(others) {
		u := others[insertPos]["index"].(string)
		upper = &u
	}

	keys, err := fracindex.GenerateNKeysBetween(lower, upper, len(ordered))
	if err != nil {
		return err
	}
	for i, e := range ordered {
		e["index"] = keys[i]
	}
	return nil
}

// errLayerNoOp signals that a layer(-many) move would reproduce the current live
// z-order exactly. The multi-mutation runner treats it as a successful no-op:
// no element is rewritten and no PATCH is issued.
var errLayerNoOp = errors.New("layer order already satisfied; no change needed")

// emitLayerNoOp reports a successful no-op: the requested layer move would not
// change the live z-order, so no element was rewritten and no PATCH was issued.
// Under --dry-run it carries dry_run:true so the no-op envelope is consistent
// with the dry-run PATCH envelope emitted by patchScene.
func emitLayerNoOp(f *cmdutil.Factory) error {
	result := map[string]any{"noop": true, "reason": "layer order already satisfied; no change applied"}
	if f.Globals.DryRun {
		result["dry_run"] = true
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal no-op result: %w", err)
	}
	return f.EmitSuccess(raw)
}

// layerOrderUnchanged reports whether splicing the flattened moving run into
// `others` at insertPos yields the same live element id order as the current
// z-order (`live`, sorted by index). It compares ids only — the observable
// z-order — so a move that would merely re-mint fractional keys without changing
// order is recognized as a no-op.
func layerOrderUnchanged(live, others, ordered []map[string]any, insertPos int) bool {
	if len(others)+len(ordered) != len(live) {
		return false
	}
	at := func(i int) string {
		switch {
		case i < insertPos:
			return others[i]["id"].(string)
		case i < insertPos+len(ordered):
			return ordered[i-insertPos]["id"].(string)
		default:
			return others[i-len(ordered)]["id"].(string)
		}
	}
	for i := range live {
		if live[i]["id"].(string) != at(i) {
			return false
		}
	}
	return true
}

func toAnyStrings(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func containsString(ss []string, target string) bool {
	for _, s := range ss {
		if s == target {
			return true
		}
	}
	return false
}

func removeString(ss []string, target string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if s != target {
			out = append(out, s)
		}
	}
	return out
}

func intersectStrings(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, s := range b {
		set[s] = true
	}
	out := make([]string, 0, len(a))
	for _, s := range a {
		if set[s] {
			out = append(out, s)
		}
	}
	return out
}

// prepareWholeSelectionTransform makes proportional scaling a whole-selection
// operation: positions scale around the selection's top-left as well as each
// element's own dimensions. Per-element absolute width/height remain unchanged.
func prepareWholeSelectionTransform(c *cobra.Command, sel []map[string]any, o *sceneFlags) error {
	if !c.Flags().Changed("scale") || len(sel) < 2 {
		return nil
	}
	minX, minY := math.Inf(1), math.Inf(1)
	for _, e := range sel {
		x, xok := finiteNumber(e["x"])
		y, yok := finiteNumber(e["y"])
		if !xok || !yok {
			return fmt.Errorf("selection contains non-finite geometry")
		}
		if x < minX {
			minX = x
		}
		if y < minY {
			minY = y
		}
	}
	for _, e := range sel {
		x, _ := finiteNumber(e["x"])
		y, _ := finiteNumber(e["y"])
		e["x"] = minX + (x-minX)*o.scale
		e["y"] = minY + (y-minY)*o.scale
	}
	return nil
}
