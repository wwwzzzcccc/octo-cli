package cmd

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/spf13/cobra"

	"github.com/Mininglamp-OSS/octo-cli/internal/client"
	"github.com/Mininglamp-OSS/octo-cli/internal/cmdutil"
	"github.com/Mininglamp-OSS/octo-cli/internal/fracindex"
)

type sceneSnapshot struct {
	Elements    []map[string]any `json:"elements"`
	Files       map[string]any   `json:"files"`
	AppState    map[string]any   `json:"appState"`
	BaseVersion string           `json:"baseVersion"`
}

type sceneFlags struct {
	typeName, id, text, data, position, relativeTo, embedURL string
	x, y, width, height, baseline, angle                     float64
	databaseRimRatio                                         float64
	dx, dy, rotateDeg, scale                                 float64
	strokeColor, backgroundColor, fillStyle                  string
	strokeWidth, roughness, opacity, fontSize                int
	// schema-v3 friendly text + native-shape + extended style
	nativeShapeKind, preset       string  // create-friendly Web toolbar shape preset
	fontFamily                    int     // resolved stable Web Board font id
	fontFamilyInput               string  // friendly id/name accepted by --font-family
	textAlign, verticalAlign      string  // left|center|right ; top|middle|bottom
	lineHeight                    float64 // finite positive, bounded
	bold, italic, underline       string  // tri-state: "", "true", "false" (customData)
	strokeStyle, roundness        string  // solid|dashed|dotted ; sharp|round
	startArrowhead, endArrowhead  string  // Excalidraw arrowhead names or "none"
	runs                          string  // textRuns JSON array, inline or @file/@-
	points, fixedSegments         string  // linear geometry JSON, inline or @file/@-
	pressures, lastCommittedPoint string  // freedraw geometry JSON, inline or @file/@-
	simulatePressure              bool
	arrowType                     string // arrow only: sharp|round|elbow
	parsedPoints, parsedSegments  []any
	// multi-selection commands (group/ungroup/*-many)
	ids     []string // repeated --id selection
	groupID string   // --group-id for group/ungroup
}

func registerDocsSceneSemanticCmd(root *cobra.Command, f *cmdutil.Factory) {
	scene := commandAt(root, "docs", "scene")
	if scene == nil {
		return
	}
	element := commandAt(scene, "element")
	if element == nil {
		element = &cobra.Command{Use: "element", Short: "Create and mutate board elements"}
		scene.AddCommand(element)
	}
	// docs.scene.element.image is registry-visible for schema discovery, but its
	// raw local-file body requires the safe handwritten implementation below.
	if generated := commandAt(element, "image"); generated != nil {
		element.RemoveCommand(generated)
	}
	registerSceneImageCmd(element, f)
	registerDocsSceneMermaidCmd(scene, f)
	registerSceneBackgroundCmds(scene, f)
	registerSceneFindCmd(scene, f)

	add := func(name, use, short string, args int, bind func(*cobra.Command, *sceneFlags), run func(*cobra.Command, *sceneFlags, []string) error) {
		flags := &sceneFlags{}
		c := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(args)}
		if bind != nil {
			bind(c, flags)
		}
		c.RunE = func(cmd *cobra.Command, argv []string) error { return run(cmd, flags, argv) }
		element.AddCommand(c)
	}
	add("create", "create <docId>", "Create an Excalidraw element", 1, bindCreateFlags, func(c *cobra.Command, o *sceneFlags, a []string) error {
		if err := validateCreateInput(c, o); err != nil {
			return err
		}
		if o.typeName == "freedraw" {
			input, err := loadFreedrawInputs(c, f, o)
			if err != nil {
				return err
			}
			if input.lastPoint != nil {
				return errors.New("--last-committed-point must be null when creating a completed freedraw element")
			}
			o.parsedPoints, o.parsedSegments = input.points, input.pressures
			return runSceneCreate(c, f, a[0], o)
		}
		points, segments, err := loadLinearInputs(c, f, o)
		if err != nil {
			return err
		}
		// A Web toolbar selection configures the tool; the pointer drag then
		// materializes its geometry. For friendly presets without explicit points,
		// reproduce that drag result deterministically from width/height. Explicit
		// --points always remains caller-authoritative.
		if o.preset == "curved-arrow" && points == nil {
			points = webCurvedLinearPoints(o.width, o.height)
			o.parsedPoints = points
			_ = c.Flags().Set("points", mustJSON(points))
		}
		if o.arrowType == "elbow" {
			candidate := points
			if candidate == nil {
				candidate = webElbowLinearPoints(o.width, o.height)
				o.parsedPoints = candidate
				_ = c.Flags().Set("points", mustJSON(candidate))
			}
			if err := validateElbowPoints(candidate); err != nil {
				return err
			}
		}
		if o.parsedPoints == nil {
			o.parsedPoints = points
		}
		o.parsedSegments = segments
		return runSceneCreate(c, f, a[0], o)
	})
	add("update", "update <docId> <elementId>", "Update customData, link, or locked", 2, func(c *cobra.Command, o *sceneFlags) {
		c.Flags().StringVar(&o.data, "data", "", "JSON object containing customData, link, and/or locked")
		c.Flags().Float64Var(&o.databaseRimRatio, "database-rim-ratio", 0.2, "database native shape rim ratio (0.06..0.4; deep-merges customData)")
	}, func(c *cobra.Command, o *sceneFlags, a []string) error {
		fields := map[string]any{}
		if c.Flags().Changed("data") {
			var err error
			fields, err = parseUpdateJSON(o.data)
			if err != nil {
				return err
			}
		}
		if !c.Flags().Changed("data") && !c.Flags().Changed("database-rim-ratio") {
			return errors.New("set --data and/or --database-rim-ratio")
		}
		if c.Flags().Changed("database-rim-ratio") && (!isFinite(o.databaseRimRatio) || o.databaseRimRatio < 0.06 || o.databaseRimRatio > 0.4) {
			return errors.New("--database-rim-ratio must be finite and within 0.06..0.4")
		}
		return runSceneMutation(c, f, a[0], a[1], func(e map[string]any, _ *sceneSnapshot) error {
			mergeFields(e, fields)
			if c.Flags().Changed("database-rim-ratio") {
				cd, _ := e["customData"].(map[string]any)
				if e["type"] != "rectangle" || cd == nil || cd["nativeShapeKind"] != "database" {
					return errors.New("--database-rim-ratio requires a rectangle with customData.nativeShapeKind=database")
				}
				return deepMergeCustomData(e, map[string]any{"databaseRimRatio": o.databaseRimRatio})
			}
			return nil
		})
	})
	add("delete", "delete <docId> <elementId>", "Soft-delete an element", 2, nil, func(c *cobra.Command, o *sceneFlags, a []string) error { return runSceneDelete(c, f, a[0], a[1]) })
	add("transform", "transform <docId> <elementId>", "Change element geometry", 2, bindTransformFlags, func(c *cobra.Command, o *sceneFlags, a []string) error {
		if err := validateTransformInput(c, o); err != nil {
			return err
		}
		return runSceneMutation(c, f, a[0], a[1], func(e map[string]any, _ *sceneSnapshot) error { return applyChangedNumbers(c, e, o) })
	})
	add("style", "style <docId> <elementId>", "Change element appearance", 2, bindStyleFlags, func(c *cobra.Command, o *sceneFlags, a []string) error {
		if err := validateStyleInput(c, o); err != nil {
			return err
		}
		return runSceneMutation(c, f, a[0], a[1], func(e map[string]any, _ *sceneSnapshot) error { return applyChangedStyle(c, e, o) })
	})
	add("text", "text <docId> <elementId>", "Update text content, base typography, and textRuns", 2, bindTextFlags, func(c *cobra.Command, o *sceneFlags, a []string) error {
		if err := validateTextInput(c, o); err != nil {
			return err
		}
		runs, err := loadRunsInput(c, f, o)
		if err != nil {
			return err
		}
		return runSceneMutation(c, f, a[0], a[1], func(e map[string]any, _ *sceneSnapshot) error { return applyTextMutation(c, e, o, runs) })
	})
	add("linear", "linear <docId> <elementId>", "Update line or arrow points and routing", 2, bindLinearFlags, func(c *cobra.Command, o *sceneFlags, a []string) error {
		if err := validateLinearInput(c, o); err != nil {
			return err
		}
		points, segments, err := loadLinearInputs(c, f, o)
		if err != nil {
			return err
		}
		return runSceneMutation(c, f, a[0], a[1], func(e map[string]any, _ *sceneSnapshot) error {
			return applyLinearMutation(c, e, o, points, segments)
		})
	})
	add("freedraw", "freedraw <docId> <elementId>", "Update freedraw points and pressure data", 2, bindFreedrawFlags, func(c *cobra.Command, o *sceneFlags, a []string) error {
		input, err := loadFreedrawInputs(c, f, o)
		if err != nil {
			return err
		}
		return runSceneMutation(c, f, a[0], a[1], func(e map[string]any, _ *sceneSnapshot) error {
			return applyFreedrawMutation(c, e, input)
		})
	})
	add("layer", "layer <docId> <elementId>", "Move an element in z-order", 2, func(c *cobra.Command, o *sceneFlags) {
		c.Flags().StringVar(&o.position, "position", "front", "front | back | before | after")
		c.Flags().StringVar(&o.relativeTo, "relative-to", "", "reference element id for before/after")
	}, func(c *cobra.Command, o *sceneFlags, a []string) error {
		if err := validateLayerInput(o); err != nil {
			return err
		}
		if o.relativeTo == a[1] {
			return errors.New("--relative-to cannot reference the element being moved")
		}
		return runSceneMutation(c, f, a[0], a[1], func(e map[string]any, s *sceneSnapshot) error {
			return applyLayer(e, s.Elements, o.position, o.relativeTo)
		})
	})
	registerSceneMultiCmds(element, f)
	registerSceneBindingCmds(element, f)
	registerSceneFrameTextCmds(element, f)
}

func commandAt(root *cobra.Command, names ...string) *cobra.Command {
	cur := root
	for _, n := range names {
		var next *cobra.Command
		for _, c := range cur.Commands() {
			if c.Name() == n {
				next = c
				break
			}
		}
		if next == nil {
			return nil
		}
		cur = next
	}
	return cur
}

const defaultBoardFontFamily = 2001

var boardFontAliases = map[string]int{
	"arial": 2001, "times-new-roman": 2002, "tahoma": 2003, "verdana": 2004,
	"simsun": 2005, "宋体": 2005, "simhei": 2006, "黑体": 2006,
	"kaiti": 2007, "楷体": 2007, "fangsong": 2008, "仿宋": 2008,
	"nsimsun": 2009, "新宋体": 2009, "stxinwei": 2010, "华文新魏": 2010,
	"stxingkai": 2011, "华文行楷": 2011, "stliti": 2012, "华文隶书": 2012,
	"pingfang-sc": 2013, "苹方": 2013, "hiragino-sans-gb": 2014, "冬青黑体": 2014,
	"stxihei": 2015, "华文细黑": 2015, "yuanti-sc": 2016, "圆体": 2016,
	"hannotate-sc": 2017, "手札体": 2017, "hanzipen-sc": 2018, "翩翩体": 2018,
	"wawati-sc": 2019, "娃娃体": 2019, "georgia": 2020, "palatino": 2021,
	"courier-new": 2022, "trebuchet-ms": 2023, "comic-sans-ms": 2024,
	"impact": 2025, "calibri": 2026,
}

func resolveBoardFontFamily(value string) (int, error) {
	value = strings.TrimSpace(value)
	if id, ok := boardFontAliases[strings.ToLower(value)]; ok {
		return id, nil
	}
	var id int
	if _, err := fmt.Sscanf(value, "%d", &id); err == nil && fmt.Sprint(id) == value && id >= 2001 && id <= 2026 {
		return id, nil
	}
	return 0, errors.New("--font-family must be a Web Board font id 2001..2026 or supported name (for example arial, simsun, pingfang-sc, courier-new)")
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// Mirrors Web's __octoCurvedLinearPoints exactly.
func webCurvedLinearPoints(dx, dy float64) []any {
	length := math.Hypot(dx, dy)
	offset, normalX, normalY := 0.0, 0.0, 0.0
	if length > 0 {
		offset = math.Min(48, math.Max(16, length*0.2))
		normalX, normalY = -dy/length, dx/length
	}
	return []any{[]any{0.0, 0.0}, []any{dx/2 + normalX*offset, dy/2 + normalY*offset}, []any{dx, dy}}
}

// Web elbow routing persists a four-point orthogonal route after a diagonal
// drag. The midpoint column is deterministic and keeps both bends editable.
func webElbowLinearPoints(dx, dy float64) []any {
	midX := dx / 2
	return []any{[]any{0.0, 0.0}, []any{midX, 0.0}, []any{midX, dy}, []any{dx, dy}}
}

func bindCreateFlags(c *cobra.Command, o *sceneFlags) {
	c.Flags().StringVar(&o.typeName, "type", "rectangle", "rectangle | ellipse | diamond | text | arrow | line | freedraw")
	c.Flags().StringVar(&o.preset, "preset", "", "Web toolbar shape/line preset (21 shapes + 4 lines; inverted-triangle compatibility alias)")
	c.Flags().StringVar(&o.id, "id", "", "element id (generated when omitted)")
	c.Flags().StringVar(&o.text, "text", "", "text content")
	c.Flags().StringVar(&o.embedURL, "url", "", "web URL for an embeddable element (http/https)")
	c.Flags().Float64Var(&o.x, "x", 0, "x coordinate")
	c.Flags().Float64Var(&o.y, "y", 0, "y coordinate")
	c.Flags().Float64Var(&o.width, "width", 100, "width")
	c.Flags().Float64Var(&o.height, "height", 100, "height")
	c.Flags().Float64Var(&o.baseline, "baseline", 0, "text baseline (required for text)")
	c.Flags().StringVar(&o.nativeShapeKind, "native-shape-kind", "", "rectangle only: one of the 19 Web native shape kinds")
	c.Flags().Float64Var(&o.databaseRimRatio, "database-rim-ratio", 0.2, "database native shape rim ratio (0.06..0.4)")
	bindLinearFlags(c, o)
	c.Flags().StringVar(&o.pressures, "pressures", "", "freedraw pressure samples as a JSON array, or @file / @-")
	c.Flags().BoolVar(&o.simulatePressure, "simulate-pressure", false, "freedraw: simulate pen pressure")
	c.Flags().StringVar(&o.lastCommittedPoint, "last-committed-point", "", "freedraw: JSON [x,y] or null, or @file / @-")
	bindStyleFlags(c, o)
}
func bindLinearFlags(c *cobra.Command, o *sceneFlags) {
	c.Flags().StringVar(&o.points, "points", "", "line/arrow points as JSON [[x,y],...], or @file / @- for stdin")
	c.Flags().StringVar(&o.arrowType, "arrow-type", "", "arrow only: sharp | round | elbow")
	c.Flags().StringVar(&o.fixedSegments, "fixed-segments", "", "elbow arrow fixed segments as JSON, or @file / @-")
}
func bindFreedrawFlags(c *cobra.Command, o *sceneFlags) {
	c.Flags().StringVar(&o.points, "points", "", "freedraw points as JSON [[x,y],...], or @file / @- for stdin")
	c.Flags().StringVar(&o.pressures, "pressures", "", "pressure samples as a JSON array, or @file / @-")
	c.Flags().BoolVar(&o.simulatePressure, "simulate-pressure", false, "simulate pen pressure")
	c.Flags().StringVar(&o.lastCommittedPoint, "last-committed-point", "", "JSON [x,y] or null, or @file / @-")
}
func bindTransformFlags(c *cobra.Command, o *sceneFlags) {
	c.Flags().Float64Var(&o.x, "x", 0, "absolute x coordinate")
	c.Flags().Float64Var(&o.y, "y", 0, "absolute y coordinate")
	c.Flags().Float64Var(&o.dx, "dx", 0, "relative x movement")
	c.Flags().Float64Var(&o.dy, "dy", 0, "relative y movement")
	c.Flags().Float64Var(&o.width, "width", 0, "absolute width")
	c.Flags().Float64Var(&o.height, "height", 0, "absolute height")
	c.Flags().Float64Var(&o.scale, "scale", 0, "positive proportional scale factor")
	c.Flags().Float64Var(&o.angle, "angle", 0, "absolute angle in radians")
	c.Flags().Float64Var(&o.rotateDeg, "rotate-deg", 0, "relative clockwise rotation in degrees")
}
func bindStyleFlags(c *cobra.Command, o *sceneFlags) {
	c.Flags().StringVar(&o.strokeColor, "stroke-color", "#1b1b1f", "stroke color")
	c.Flags().StringVar(&o.backgroundColor, "background-color", "transparent", "background color")
	c.Flags().StringVar(&o.fillStyle, "fill-style", "solid", "fill style")
	c.Flags().IntVar(&o.strokeWidth, "stroke-width", 1, "stroke width")
	c.Flags().IntVar(&o.roughness, "roughness", 1, "roughness")
	c.Flags().IntVar(&o.opacity, "opacity", 100, "opacity")
	c.Flags().IntVar(&o.fontSize, "font-size", 20, "font size")
	c.Flags().StringVar(&o.strokeStyle, "stroke-style", "solid", "solid | dashed | dotted")
	c.Flags().StringVar(&o.roundness, "roundness", "", "sharp | round (rectangle/diamond/line/arrow)")
	c.Flags().StringVar(&o.startArrowhead, "start-arrowhead", "", "arrow/line only: none | arrow | bar | dot | triangle | triangle_outline | diamond | diamond_outline")
	c.Flags().StringVar(&o.endArrowhead, "end-arrowhead", "", "arrow/line only: none | arrow | bar | dot | triangle | triangle_outline | diamond | diamond_outline")
	c.Flags().StringVar(&o.fontFamilyInput, "font-family", "arial", "text only: Web Board font id or name (2001-2026; e.g. arial, simsun, pingfang-sc)")
	c.Flags().StringVar(&o.textAlign, "text-align", "left", "text only: left | center | right")
	c.Flags().StringVar(&o.verticalAlign, "vertical-align", "top", "text only: top | middle | bottom")
	c.Flags().Float64Var(&o.lineHeight, "line-height", 1.25, "text only: line height (finite positive, <= 10)")
	c.Flags().StringVar(&o.bold, "bold", "", "text only: true | false (stored in customData)")
	c.Flags().StringVar(&o.italic, "italic", "", "text only: true | false (stored in customData)")
	c.Flags().StringVar(&o.underline, "underline", "", "text only: true | false (stored in customData)")
}
func bindTextFlags(c *cobra.Command, o *sceneFlags) {
	c.Flags().StringVar(&o.text, "text", "", "replacement text content (also sets originalText)")
	c.Flags().IntVar(&o.fontSize, "font-size", 20, "font size")
	c.Flags().StringVar(&o.fontFamilyInput, "font-family", "arial", "Web Board font id or name (2001-2026; e.g. arial, simsun, pingfang-sc)")
	c.Flags().StringVar(&o.textAlign, "text-align", "left", "left | center | right")
	c.Flags().StringVar(&o.verticalAlign, "vertical-align", "top", "top | middle | bottom")
	c.Flags().Float64Var(&o.lineHeight, "line-height", 1.25, "line height (finite positive, <= 10)")
	c.Flags().StringVar(&o.runs, "runs", "", "textRuns as a JSON array, or @file / @- for stdin")
}

func runSceneCreate(cmd *cobra.Command, f *cmdutil.Factory, docID string, o *sceneFlags) error {
	s, err := getScene(cmd, f, docID)
	if err != nil {
		return err
	}
	if o.id == "" {
		o.id = randomID()
	}
	if findElement(s.Elements, o.id) != nil {
		return fmt.Errorf("element %q already exists", o.id)
	}
	var lower *string
	for _, existing := range s.Elements {
		if existing["isDeleted"] == true {
			continue
		}
		v, err := elementIndex(existing)
		if err != nil {
			return err
		}
		if lower == nil || v > *lower {
			value := v
			lower = &value
		}
	}
	index, err := fracindex.GenerateKeyBetween(lower, nil)
	if err != nil {
		return err
	}
	e, err := baseElement(cmd, o, index)
	if err != nil {
		return err
	}
	if err := validateFinalElement(e); err != nil {
		return err
	}
	return patchScene(cmd, f, docID, s.BaseVersion, map[string]any{"elements": []any{e}})
}

// changed reports whether a flag was explicitly set. It tolerates a nil command
// (used by unit tests that build a base element without a cobra invocation).
func changed(c *cobra.Command, flag string) bool { return c != nil && c.Flags().Changed(flag) }

func baseElement(c *cobra.Command, o *sceneFlags, index string) (map[string]any, error) {
	nonce := newNonce()
	e := map[string]any{"id": o.id, "type": o.typeName, "x": o.x, "y": o.y, "width": o.width, "height": o.height, "angle": 0, "strokeColor": o.strokeColor, "backgroundColor": o.backgroundColor, "fillStyle": o.fillStyle, "strokeWidth": o.strokeWidth, "strokeStyle": o.strokeStyle, "roughness": o.roughness, "opacity": o.opacity, "groupIds": []any{}, "frameId": nil, "index": index, "roundness": nil, "seed": nonce, "version": 1, "versionNonce": nonce, "isDeleted": false, "boundElements": nil, "updated": time.Now().UnixMilli(), "link": nil, "locked": false}
	if changed(c, "roundness") {
		r, err := roundnessValue(e, o.roundness)
		if err != nil {
			return nil, err
		}
		e["roundness"] = r
	}
	switch o.typeName {
	case "text":
		e["text"], e["originalText"], e["fontSize"], e["fontFamily"], e["textAlign"], e["verticalAlign"], e["baseline"], e["lineHeight"], e["containerId"], e["autoResize"] = o.text, o.text, o.fontSize, o.fontFamily, o.textAlign, o.verticalAlign, o.baseline, o.lineHeight, nil, true
		if _, err := boldItalicUnderline(c, e, o); err != nil {
			return nil, err
		}
	case "arrow", "line":
		e["points"], e["lastCommittedPoint"], e["startBinding"], e["endBinding"] = []any{[]any{0, 0}, []any{o.width, o.height}}, nil, nil, nil
		e["elbowed"] = false
		if o.typeName == "arrow" {
			e["startArrowhead"], e["endArrowhead"] = nil, "arrow"
		} else {
			e["startArrowhead"], e["endArrowhead"], e["polygon"] = nil, nil, false
		}
		if changed(c, "points") || changed(c, "arrow-type") || changed(c, "fixed-segments") {
			if err := applyLinearMutation(c, e, o, o.parsedPoints, o.parsedSegments); err != nil {
				return nil, err
			}
		}
		for _, flag := range []string{"start-arrowhead", "end-arrowhead"} {
			if changed(c, flag) {
				if err := applyArrowhead(e, flag, o); err != nil {
					return nil, err
				}
			}
		}
	case "frame":
		e["name"] = ""
	case "embeddable":
		e["link"] = o.embedURL
		// insertEmbeddableElement forces transparent stroke/fill and sizes the
		// element to getEmbedLink's intrinsic footprint. Reproduce that default
		// while leaving an explicit --stroke-color/--background-color/--width/
		// --height authoritative.
		if !changed(c, "stroke-color") {
			e["strokeColor"] = "transparent"
		}
		if !changed(c, "background-color") {
			e["backgroundColor"] = "transparent"
		}
		if !changed(c, "width") && !changed(c, "height") {
			w, h := embeddableIntrinsicSize(o.embedURL)
			e["width"], e["height"] = w, h
		}
	case "freedraw":
		e["points"] = o.parsedPoints
		e["pressures"] = o.parsedSegments
		e["simulatePressure"] = o.simulatePressure
		e["lastCommittedPoint"] = nil
		if err := normalizeLocalPoints(e); err != nil {
			return nil, err
		}
		updateLinearBounds(e)
	}
	if o.typeName == "rectangle" && changed(c, "native-shape-kind") {
		if err := deepMergeCustomData(e, map[string]any{"nativeShapeKind": o.nativeShapeKind}); err != nil {
			return nil, err
		}
	}
	if changed(c, "database-rim-ratio") {
		if err := deepMergeCustomData(e, map[string]any{"databaseRimRatio": o.databaseRimRatio}); err != nil {
			return nil, err
		}
	}
	return e, nil
}

func runSceneMutation(cmd *cobra.Command, f *cmdutil.Factory, docID, id string, mutate func(map[string]any, *sceneSnapshot) error) error {
	s, err := getScene(cmd, f, docID)
	if err != nil {
		return err
	}
	e := findElement(s.Elements, id)
	if e == nil {
		return fmt.Errorf("element %q not found", id)
	}
	if e["isDeleted"] == true {
		return fmt.Errorf("element %q is deleted and cannot be mutated", id)
	}
	clone := cloneMap(e)
	if err := mutate(clone, s); err != nil {
		return err
	}
	if reflect.DeepEqual(clone, e) {
		return errors.New("mutation would not change the element")
	}
	if err := bumpElement(clone); err != nil {
		return err
	}
	if err := validateFinalElement(clone); err != nil {
		return err
	}
	return patchScene(cmd, f, docID, s.BaseVersion, map[string]any{"elements": []any{clone}})
}

func runSceneDelete(cmd *cobra.Command, f *cmdutil.Factory, docID, id string) error {
	s, err := getScene(cmd, f, docID)
	if err != nil {
		return err
	}
	e := findElement(s.Elements, id)
	if e == nil {
		return fmt.Errorf("element %q not found", id)
	}
	if e["isDeleted"] == true {
		return fmt.Errorf("element %q is already deleted", id)
	}
	clone := cloneMap(e)
	clone["isDeleted"] = true
	if err := bumpElement(clone); err != nil {
		return err
	}
	if err := validateFinalElement(clone); err != nil {
		return err
	}
	return patchScene(cmd, f, docID, s.BaseVersion, map[string]any{"elements": []any{clone}})
}

func getScene(cmd *cobra.Command, f *cmdutil.Factory, docID string) (*sceneSnapshot, error) {
	path, err := scenePath(docID)
	if err != nil {
		return nil, err
	}
	cfg, err := f.Config()
	if err != nil {
		return nil, err
	}
	cred, err := f.Credential()
	if err != nil {
		return nil, err
	}
	readClient := client.New(cfg, cred, client.Options{NoRetry: f.Globals.NoRetry, Timeout: f.Globals.Timeout, ErrOut: f.ErrOut()})
	raw, err := readClient.Do(cmd.Context(), &client.Request{Method: http.MethodGet, Path: path, SuppressSpaceHeader: true})
	if err != nil {
		return nil, err
	}
	var s sceneSnapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("decode scene: %w", err)
	}
	if s.BaseVersion == "" {
		return nil, errors.New("scene response has no baseVersion")
	}
	if err := checkDuplicateLiveIDs(s.Elements); err != nil {
		return nil, err
	}
	return &s, nil
}

// checkDuplicateLiveIDs rejects a scene whose live (non-deleted) elements are not
// each validly and uniquely identified. Every live element must carry a nonempty
// string id: a missing, non-string, or empty id is malformed and rejected here
// (never silently skipped), since downstream code indexes elements by id and a
// mutation would otherwise target the wrong element or none. Two live elements
// sharing an id is likewise rejected — findElement returns the first match, so a
// duplicate would let a mutation silently target one while a second identical id
// survives. Either way the scene is corrupt and must not be read-modify-written
// blindly. Tombstoned elements follow the existing contract and are exempt.
func checkDuplicateLiveIDs(elements []map[string]any) error {
	seen := make(map[string]bool, len(elements))
	for _, e := range elements {
		if e["isDeleted"] == true {
			continue
		}
		id, ok := e["id"].(string)
		if !ok || id == "" {
			return errors.New("scene contains a live element with a missing, non-string, or empty id")
		}
		if seen[id] {
			return fmt.Errorf("scene contains duplicate live element id %q", id)
		}
		seen[id] = true
	}
	return nil
}
func patchScene(cmd *cobra.Command, f *cmdutil.Factory, docID, baseVersion string, patch map[string]any) error {
	path, err := scenePath(docID)
	if err != nil {
		return err
	}
	if f.Globals.DryRun {
		raw, err := json.Marshal(map[string]any{"dry_run": true, "method": http.MethodPatch, "path": path, "headers": map[string]string{"If-Match": baseVersion}, "body": patch})
		if err != nil {
			return fmt.Errorf("marshal dry-run request: %w", err)
		}
		return f.EmitSuccess(raw)
	}
	cli, err := f.Client()
	if err != nil {
		return err
	}
	raw, err := cli.Do(cmd.Context(), &client.Request{Method: http.MethodPatch, Path: path, Headers: map[string]string{"If-Match": baseVersion}, Body: patch, SuppressSpaceHeader: true})
	if err != nil {
		return err
	}
	return f.EmitSuccess(raw)
}
func opaquePathSegment(value, name string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("%s must not be empty", name)
	}
	if value == "." || value == ".." {
		return "", fmt.Errorf("%s must not be . or ..", name)
	}
	return url.PathEscape(value), nil
}

func scenePath(docID string) (string, error) {
	segment, err := opaquePathSegment(docID, "docId")
	if err != nil {
		return "", err
	}
	return "/v1/bot/docs/" + segment + "/scene", nil
}
func findElement(elements []map[string]any, id string) map[string]any {
	for _, e := range elements {
		if e["id"] == id {
			return e
		}
	}
	return nil
}
func cloneMap(in map[string]any) map[string]any {
	raw, _ := json.Marshal(in)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}
func bumpElement(e map[string]any) error {
	v, ok := positiveInteger(e["version"])
	if !ok {
		return errors.New("existing element version must be a positive integer")
	}
	old, ok := nonNegativeInteger(e["versionNonce"])
	if !ok {
		return errors.New("existing element versionNonce must be a non-negative integer")
	}
	e["version"] = v + 1
	nonce := newNonce()
	if nonce == old {
		nonce = (nonce + 1) & 0x7fffffff
	}
	e["versionNonce"] = nonce
	e["updated"] = time.Now().UnixMilli()
	return nil
}
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		x, e := n.Float64()
		return x, e == nil
	}
	return 0, false
}
func newNonce() int {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 1
	}
	return int(binary.BigEndian.Uint32(b[:]) & 0x7fffffff)
}
func randomID() string { var b [8]byte; _, _ = rand.Read(b[:]); return fmt.Sprintf("%x", b[:]) }
func oneOf(s string, values ...string) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}

var nativeShapeKinds = []string{
	"square", "database", "notched-dovetail", "chevron", "parallelogram", "trapezoid",
	"speech-bubble", "speech-bubble-rounded", "triangle", "inverted-triangle", "circle",
	"right-triangle", "star", "hexagon", "pentagon", "octagon", "left-arrow",
	"right-arrow", "bidirectional-arrow",
}

func isNativeShapeKind(value string) bool { return oneOf(value, nativeShapeKinds...) }
func parseUpdateJSON(value string) (map[string]any, error) {
	var fields map[string]any
	if err := json.Unmarshal([]byte(value), &fields); err != nil {
		return nil, fmt.Errorf("--data: %w", err)
	}
	if fields == nil || len(fields) == 0 {
		return nil, errors.New("--data must be a non-empty JSON object")
	}
	allowed := map[string]bool{"customData": true, "link": true, "locked": true}
	for k := range fields {
		if !allowed[k] {
			return nil, fmt.Errorf("--data cannot replace structural or managed field %q; allowed fields: customData, link, locked", k)
		}
	}
	if v, ok := fields["customData"]; ok && v != nil {
		if _, ok := v.(map[string]any); !ok {
			return nil, errors.New("--data customData must be an object or null")
		}
	}
	if v, ok := fields["link"]; ok && v != nil {
		if _, ok := v.(string); !ok {
			return nil, errors.New("--data link must be a string or null")
		}
	}
	if v, ok := fields["locked"]; ok {
		if _, ok := v.(bool); !ok {
			return nil, errors.New("--data locked must be boolean")
		}
	}
	return fields, nil
}
func mergeFields(dst, fields map[string]any) {
	for k, v := range fields {
		dst[k] = v
	}
}
func applyChangedNumbers(c *cobra.Command, e map[string]any, o *sceneFlags) error {
	vals := map[string]float64{"x": o.x, "y": o.y, "width": o.width, "height": o.height, "angle": o.angle}
	changed := false
	for k, v := range vals {
		if c.Flags().Changed(k) {
			if !isFinite(v) {
				return fmt.Errorf("--%s must be finite", k)
			}
			if (k == "width" || k == "height") && v < 0 {
				return fmt.Errorf("--%s must be non-negative", k)
			}
			changed = true
		}
	}
	for _, pair := range []struct {
		name  string
		value float64
	}{{"dx", o.dx}, {"dy", o.dy}, {"rotate-deg", o.rotateDeg}, {"scale", o.scale}} {
		if c.Flags().Changed(pair.name) {
			if !isFinite(pair.value) {
				return fmt.Errorf("--%s must be finite", pair.name)
			}
			changed = true
		}
	}
	if c.Flags().Changed("x") && c.Flags().Changed("dx") {
		return errors.New("--x and --dx are mutually exclusive")
	}
	if c.Flags().Changed("y") && c.Flags().Changed("dy") {
		return errors.New("--y and --dy are mutually exclusive")
	}
	if c.Flags().Changed("angle") && c.Flags().Changed("rotate-deg") {
		return errors.New("--angle and --rotate-deg are mutually exclusive")
	}
	if c.Flags().Changed("scale") && (c.Flags().Changed("width") || c.Flags().Changed("height")) {
		return errors.New("--scale cannot be combined with --width or --height")
	}
	if c.Flags().Changed("scale") && o.scale <= 0 {
		return errors.New("--scale must be greater than zero")
	}
	if !changed {
		return errors.New("set at least one transform flag")
	}
	numbers := map[string]float64{}
	for _, field := range []string{"x", "y", "width", "height", "angle"} {
		n, ok := finiteNumber(e[field])
		if !ok {
			if !c.Flags().Changed("dx") && !c.Flags().Changed("dy") && !c.Flags().Changed("rotate-deg") && !c.Flags().Changed("scale") {
				continue
			}
			return fmt.Errorf("element %s must be finite for relative/proportional transforms", field)
		}
		numbers[field] = n
	}
	if c.Flags().Changed("dx") {
		e["x"] = numbers["x"] + o.dx
	}
	if c.Flags().Changed("dy") {
		e["y"] = numbers["y"] + o.dy
	}
	if c.Flags().Changed("rotate-deg") {
		e["angle"] = numbers["angle"] + o.rotateDeg*math.Pi/180
	}
	scaling := c.Flags().Changed("scale")
	if scaling {
		o.width = numbers["width"] * o.scale
		o.height = numbers["height"] * o.scale
		if e["type"] == "text" {
			if err := scaleTextTypography(e, o.scale); err != nil {
				return err
			}
		}
		_ = c.Flags().Set("width", fmt.Sprint(o.width))
		_ = c.Flags().Set("height", fmt.Sprint(o.height))
		vals["width"], vals["height"] = o.width, o.height
		// Width/height are implementation details of --scale, not user flags.
		// Restore Changed after this element so transform-many can reuse the
		// same command and calculate each member from its own dimensions.
		defer func() {
			c.Flags().Lookup("width").Changed = false
			c.Flags().Lookup("height").Changed = false
		}()
	}
	kind, _ := e["type"].(string)
	if kind == "line" || kind == "arrow" {
		if err := resizeLinearPoints(c, e, o); err != nil {
			return err
		}
	}
	for k, v := range vals {
		if c.Flags().Changed(k) {
			e[k] = v
		}
	}
	return nil
}

// scaleTextTypography keeps Excalidraw's derived text geometry in step with a
// proportional element resize. lineHeight is intentionally not multiplied: in
// the whiteboard schema it is the unitless ratio used by Excalidraw (normally
// around 1.25), while fontSize and baseline are scene-space measurements.
func scaleTextTypography(e map[string]any, factor float64) error {
	fontSize, ok := finiteNumber(e["fontSize"])
	if !ok || fontSize <= 0 {
		return errors.New("cannot scale text with invalid fontSize")
	}
	baseline, ok := finiteNumber(e["baseline"])
	if !ok || baseline < 0 {
		return errors.New("cannot scale text with invalid baseline")
	}
	e["fontSize"] = fontSize * factor
	e["baseline"] = baseline * factor

	// Rich-text run font sizes use the same scene-space unit as the element's
	// base fontSize. Preserve all other customData and run fields verbatim.
	if customData, ok := e["customData"].(map[string]any); ok {
		if runs, ok := customData["textRuns"].([]any); ok {
			for i, raw := range runs {
				run, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("cannot scale malformed customData.textRuns[%d]", i)
				}
				if rawSize, exists := run["fontSize"]; exists {
					size, ok := finiteNumber(rawSize)
					if !ok || size <= 0 {
						return fmt.Errorf("cannot scale customData.textRuns[%d] with invalid fontSize", i)
					}
					run["fontSize"] = size * factor
				}
			}
		}
	}
	return nil
}

func resizeLinearPoints(c *cobra.Command, e map[string]any, o *sceneFlags) error {
	if !c.Flags().Changed("width") && !c.Flags().Changed("height") {
		return nil
	}
	for _, field := range []string{"startBinding", "endBinding", "boundElements", "elbow", "fixedPoint", "fixedSegments"} {
		if v, ok := e[field]; ok && v != nil {
			return fmt.Errorf("cannot resize a linear element with %s", field)
		}
	}
	if v, ok := e["elbowed"]; ok && v != false {
		return errors.New("cannot resize an elbowed linear element")
	}
	points, ok := e["points"].([]any)
	if !ok || len(points) != 2 {
		return errors.New("cannot resize a linear element unless it has exactly two points")
	}
	parsed := make([][]float64, 2)
	for i, raw := range points {
		point, ok := raw.([]any)
		if !ok || len(point) != 2 {
			return errors.New("cannot resize a linear element with malformed points")
		}
		x, xok := number(point[0])
		y, yok := number(point[1])
		if !xok || !yok || !isFinite(x) || !isFinite(y) {
			return errors.New("cannot resize a linear element with non-finite points")
		}
		parsed[i] = []float64{x, y}
	}
	oldW, wok := finiteNumber(e["width"])
	oldH, hok := finiteNumber(e["height"])
	if !wok || !hok || oldW < 0 || oldH < 0 {
		return errors.New("cannot resize a linear element with invalid width or height")
	}
	newW, newH := oldW, oldH
	if c.Flags().Changed("width") {
		newW = o.width
	}
	if c.Flags().Changed("height") {
		newH = o.height
	}
	scale := func(value, oldSize, newSize float64, endpoint bool) (float64, error) {
		if oldSize == 0 {
			if value != 0 {
				return 0, errors.New("cannot resize non-zero linear coordinates from a zero-sized axis")
			}
			// A newly-created two-point line is [[0,0],[width,height]]. On a
			// zero-sized axis only the endpoint carries the new extent.
			if endpoint {
				return newSize, nil
			}
			return 0, nil
		}
		return value * newSize / oldSize, nil
	}
	out := make([]any, 2)
	for i, point := range parsed {
		x, err := scale(point[0], oldW, newW, i == 1)
		if err != nil {
			return err
		}
		y, err := scale(point[1], oldH, newH, i == 1)
		if err != nil {
			return err
		}
		out[i] = []any{x, y}
	}
	e["points"] = out
	return nil
}

func applyChangedStyle(c *cobra.Command, e map[string]any, o *sceneFlags) error {
	if err := validateChangedStyle(c, o); err != nil {
		return err
	}
	stringsV := map[string]string{"stroke-color": o.strokeColor, "background-color": o.backgroundColor, "fill-style": o.fillStyle, "stroke-style": o.strokeStyle}
	ints := map[string]int{"stroke-width": o.strokeWidth, "roughness": o.roughness, "opacity": o.opacity}
	for flag, v := range stringsV {
		if c.Flags().Changed(flag) {
			e[camel(flag)] = v
		}
	}
	for flag, v := range ints {
		if c.Flags().Changed(flag) {
			e[camel(flag)] = v
		}
	}
	if c.Flags().Changed("roundness") {
		r, err := roundnessValue(e, o.roundness)
		if err != nil {
			return err
		}
		e["roundness"] = r
	}
	for _, flag := range []string{"start-arrowhead", "end-arrowhead"} {
		if c.Flags().Changed(flag) {
			if err := applyArrowhead(e, flag, o); err != nil {
				return err
			}
		}
	}
	if err := applyTextTypography(c, e, o); err != nil {
		return err
	}
	if _, err := boldItalicUnderline(c, e, o); err != nil {
		return err
	}
	return nil
}

// applyTextTypography applies any changed base-typography flags (font-size,
// font-family, text-align, vertical-align, line-height) to a text element. It is
// shared by `style` and `text`; the flags are rejected loudly on a non-text
// target rather than silently ignored.
func applyTextTypography(c *cobra.Command, e map[string]any, o *sceneFlags) error {
	touched := false
	for _, flag := range []string{"font-size", "font-family", "text-align", "vertical-align", "line-height"} {
		if c.Flags().Changed(flag) {
			touched = true
		}
	}
	if !touched {
		return nil
	}
	if e["type"] != "text" {
		return errors.New("--font-size, --font-family, --text-align, --vertical-align, and --line-height are only valid for text elements")
	}
	if c.Flags().Changed("font-size") {
		if err := resizeTextForFontSize(e, o.fontSize); err != nil {
			return err
		}
		e["fontSize"] = o.fontSize
	}
	if c.Flags().Changed("font-family") {
		e["fontFamily"] = o.fontFamily
	}
	if c.Flags().Changed("text-align") {
		e["textAlign"] = o.textAlign
	}
	if c.Flags().Changed("vertical-align") {
		e["verticalAlign"] = o.verticalAlign
	}
	if c.Flags().Changed("line-height") {
		e["lineHeight"] = o.lineHeight
	}
	return nil
}

// boldItalicUnderline deep-merges the tri-state bold/italic/underline flags into
// the text element's customData, preserving unrelated keys. It reports whether it
// wrote anything.
func boldItalicUnderline(c *cobra.Command, e map[string]any, o *sceneFlags) (bool, error) {
	kv := map[string]any{}
	for _, spec := range []struct{ flag, val string }{{"bold", o.bold}, {"italic", o.italic}, {"underline", o.underline}} {
		if changed(c, spec.flag) {
			kv[spec.flag] = spec.val == "true"
		}
	}
	if len(kv) == 0 {
		return false, nil
	}
	if e["type"] != "text" {
		return false, errors.New("--bold, --italic, and --underline are only valid for text elements")
	}
	if err := deepMergeCustomData(e, kv); err != nil {
		return false, err
	}
	return true, nil
}

// roundnessValue maps the friendly sharp|round selector to the Excalidraw
// contract: sharp -> null, round -> {type:3} (ADAPTIVE_RADIUS) for
// rectangle/diamond and {type:2} (PROPORTIONAL_RADIUS) for line/arrow. Ellipse
// and text have no roundness and are rejected.
func roundnessValue(e map[string]any, mode string) (any, error) {
	kind, _ := e["type"].(string)
	switch mode {
	case "sharp":
		return nil, nil
	case "round":
		switch kind {
		case "rectangle", "diamond":
			return map[string]any{"type": 3}, nil
		case "line", "arrow":
			return map[string]any{"type": 2}, nil
		default:
			return nil, fmt.Errorf("--roundness is not supported for %s elements", kind)
		}
	default:
		return nil, errors.New("--roundness must be sharp or round")
	}
}

var validArrowheads = []string{"none", "arrow", "bar", "dot", "triangle", "triangle_outline", "diamond", "diamond_outline"}

func applyArrowhead(e map[string]any, flag string, o *sceneFlags) error {
	kind, _ := e["type"].(string)
	if kind != "arrow" && kind != "line" {
		return fmt.Errorf("--%s is only valid for arrow or line elements", flag)
	}
	val, field := o.startArrowhead, "startArrowhead"
	if flag == "end-arrowhead" {
		val, field = o.endArrowhead, "endArrowhead"
	}
	if val == "none" {
		e[field] = nil
	} else {
		e[field] = val
	}
	return nil
}

// deepMergeCustomData merges kv into the element's customData, recursively
// merging nested objects and overwriting only the named leaf keys, so unrelated
// customData survives.
func deepMergeCustomData(e map[string]any, kv map[string]any) error {
	var cur map[string]any
	if raw, ok := e["customData"]; ok && raw != nil {
		m, ok := raw.(map[string]any)
		if !ok {
			return errors.New("existing customData is not an object; cannot merge")
		}
		cur = cloneMap(m)
	} else {
		cur = map[string]any{}
	}
	deepMerge(cur, kv)
	e["customData"] = cur
	return nil
}

func deepMerge(dst, src map[string]any) {
	for k, v := range src {
		if sv, ok := v.(map[string]any); ok {
			if dv, ok := dst[k].(map[string]any); ok {
				deepMerge(dv, sv)
				continue
			}
		}
		dst[k] = v
	}
}

// applyTextMutation implements `element text`: it rewrites content, base
// typography, and/or textRuns on a text element while preserving unrelated
// fields (including unrelated customData).
func applyTextMutation(c *cobra.Command, e map[string]any, o *sceneFlags, runs []any) error {
	if e["type"] != "text" {
		return errors.New("element is not a text element; textRuns and text content require a text target")
	}
	if c.Flags().Changed("text") {
		e["text"], e["originalText"] = o.text, o.text
	}
	if err := applyTextTypography(c, e, o); err != nil {
		return err
	}
	if c.Flags().Changed("runs") {
		text, _ := e["text"].(string)
		normalized, err := normalizeTextRuns(runs, utf16Len(text))
		if err != nil {
			return err
		}
		if err := setCustomDataTextRuns(e, normalized); err != nil {
			return err
		}
	}
	return nil
}

// setCustomDataTextRuns writes the normalized rich-text runs to
// customData.textRuns (schema v3, packages/whiteboard-schema/src/customData.ts),
// deep-copying the existing customData so unrelated keys survive. An empty run
// slice (from `--runs []`) removes only customData.textRuns, and customData itself
// is dropped entirely when it becomes empty — matching the backend's
// normalizeCustomData, which deletes an emptied customData object. The canonical
// element never carries a top-level textRuns field.
func setCustomDataTextRuns(e map[string]any, runs []any) error {
	var cur map[string]any
	if raw, ok := e["customData"]; ok && raw != nil {
		m, ok := raw.(map[string]any)
		if !ok {
			return errors.New("existing customData is not an object; cannot set textRuns")
		}
		cur = cloneMap(m)
	} else {
		cur = map[string]any{}
	}
	if len(runs) == 0 {
		delete(cur, "textRuns")
	} else {
		cur["textRuns"] = runs
	}
	if len(cur) == 0 {
		delete(e, "customData")
	} else {
		e["customData"] = cur
	}
	return nil
}

func resizeTextForFontSize(e map[string]any, fontSize int) error {
	if e["type"] != "text" {
		return nil
	}
	text, ok := e["text"].(string)
	if !ok || strings.ContainsAny(text, "\r\n") || e["autoResize"] != true || e["containerId"] != nil || e["boundElements"] != nil {
		return errors.New("font-size changes require standalone auto-resizing single-line text without bindings")
	}
	oldFont, ok := finiteNumber(e["fontSize"])
	if !ok || oldFont <= 0 {
		return errors.New("cannot change font size on text with invalid existing fontSize")
	}
	width, wok := finiteNumber(e["width"])
	height, hok := finiteNumber(e["height"])
	baseline, bok := finiteNumber(e["baseline"])
	if !wok || !hok || !bok || width < 0 || height < 0 || baseline < 0 {
		return errors.New("cannot change font size on text with invalid width, height, or baseline")
	}
	ratio := float64(fontSize) / oldFont
	e["width"], e["height"], e["baseline"] = width*ratio, height*ratio, baseline*ratio
	return nil
}

func camel(s string) string {
	p := strings.Split(s, "-")
	for i := 1; i < len(p); i++ {
		p[i] = strings.ToUpper(p[i][:1]) + p[i][1:]
	}
	return strings.Join(p, "")
}
func isFinite(v float64) bool            { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func finiteNumber(v any) (float64, bool) { n, ok := number(v); return n, ok && isFinite(n) }
func positiveInteger(v any) (int, bool) {
	n, ok := finiteNumber(v)
	return int(n), ok && n >= 1 && n == math.Trunc(n)
}
func nonNegativeInteger(v any) (int, bool) {
	n, ok := finiteNumber(v)
	return int(n), ok && n >= 0 && n == math.Trunc(n)
}

// utf16Len returns the number of UTF-16 code units in s. textRuns ranges are
// expressed in UTF-16 offsets (matching the browser/Excalidraw text model), so a
// non-BMP code point such as an emoji counts as two.
func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

func integerValue(v any) (int, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	case float64:
		if math.IsInf(n, 0) || math.IsNaN(n) || n != math.Trunc(n) {
			return 0, false
		}
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

// validColor mirrors isValidTextColor in the shared whiteboard-schema
// (packages/whiteboard-schema/src/customData.ts): a conservative, length-bounded
// CSS color — a #-prefixed hex triple/quad (3/4/6/8 hex digits), a functional
// rgb()/rgba()/hsl()/hsla() notation, or a bare color keyword (which also covers
// "transparent"). The length cap keeps a run from smuggling an unbounded string
// onto the wire; the accepted forms match the schema verbatim so a run authored on
// the Web board round-trips through the CLI unchanged.
const colorMaxLength = 32

var (
	hexColorRe        = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3,4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)
	functionalColorRe = regexp.MustCompile(`^(?:rgb|rgba|hsl|hsla)\([0-9.,%\s/]+\)$`)
	keywordColorRe    = regexp.MustCompile(`^[a-zA-Z]+$`)
)

func validColor(s string) bool {
	if len(s) == 0 || len(s) > colorMaxLength {
		return false
	}
	return hexColorRe.MatchString(s) || functionalColorRe.MatchString(s) || keywordColorRe.MatchString(s)
}

// loadRunsInput reads and structurally validates the --runs argument (inline JSON
// array or @file / @-). Clamping/sorting/merging happen later in
// normalizeTextRuns, which needs the final text length. Returns nil when --runs
// was not set.
func loadRunsInput(c *cobra.Command, f *cmdutil.Factory, o *sceneFlags) ([]any, error) {
	if !c.Flags().Changed("runs") {
		return nil, nil
	}
	raw, err := cmdutil.ParseInput(f, o.runs)
	if err != nil {
		return nil, fmt.Errorf("--runs: %w", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("--runs must not be empty")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var arr []any
	if err := dec.Decode(&arr); err != nil {
		return nil, fmt.Errorf("--runs must be a JSON array: %w", err)
	}
	if arr == nil {
		return nil, errors.New("--runs must be a JSON array (use [] to clear textRuns)")
	}
	for i, item := range arr {
		if err := validateRunStructure(item); err != nil {
			return nil, fmt.Errorf("--runs[%d]: %w", i, err)
		}
	}
	return arr, nil
}

func loadLinearInputs(c *cobra.Command, f *cmdutil.Factory, o *sceneFlags) ([]any, []any, error) {
	var points, segments []any
	var err error
	if c.Flags().Changed("points") {
		points, err = loadJSONArrayInput(f, "--points", o.points, false)
		if err != nil {
			return nil, nil, err
		}
		if err := validateLinearPoints(points); err != nil {
			return nil, nil, fmt.Errorf("--points: %w", err)
		}
	}
	if c.Flags().Changed("fixed-segments") {
		segments, err = loadJSONArrayInput(f, "--fixed-segments", o.fixedSegments, true)
		if err != nil {
			return nil, nil, err
		}
		if points == nil && len(segments) > 0 {
			return nil, nil, errors.New("--fixed-segments with entries requires --points in the same command so segment indexes can be validated")
		}
		if err := validateFixedSegments(segments, points); err != nil {
			return nil, nil, fmt.Errorf("--fixed-segments: %w", err)
		}
	}
	return points, segments, nil
}

func loadJSONArrayInput(f *cmdutil.Factory, flag, spec string, allowEmpty bool) ([]any, error) {
	raw, err := cmdutil.ParseInput(f, spec)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", flag, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("%s must not be empty", flag)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var arr []any
	if err := dec.Decode(&arr); err != nil {
		return nil, fmt.Errorf("%s must be a JSON array: %w", flag, err)
	}
	if arr == nil || (!allowEmpty && len(arr) == 0) {
		return nil, fmt.Errorf("%s must be a non-empty JSON array", flag)
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("%s must contain exactly one JSON value", flag)
	}
	return arr, nil
}

func validateLinearPoints(points []any) error {
	if len(points) < 2 {
		return errors.New("must contain at least two points")
	}
	for i, raw := range points {
		p, ok := raw.([]any)
		if !ok || len(p) != 2 {
			return fmt.Errorf("point %d must be [x,y]", i)
		}
		for _, v := range p {
			if n, ok := finiteNumber(v); !ok || math.Abs(n) > 1e7 {
				return fmt.Errorf("point %d coordinates must be finite and within ±10000000", i)
			}
		}
	}
	return nil
}

func validateFixedSegments(segments, points []any) error {
	seen := map[int]bool{}
	for i, raw := range segments {
		m, ok := raw.(map[string]any)
		if !ok || len(m) != 3 {
			return fmt.Errorf("segment %d must be an object with exactly start, end, and index", i)
		}
		for k := range m {
			if !oneOf(k, "start", "end", "index") {
				return fmt.Errorf("segment %d has unknown field %q", i, k)
			}
		}
		idx, ok := integerValue(m["index"])
		if !ok || idx < 1 || (points != nil && idx >= len(points)) || seen[idx] {
			return fmt.Errorf("segment %d index must be a unique integer in [1, point-count-1]", i)
		}
		seen[idx] = true
		for _, field := range []string{"start", "end"} {
			if err := validateLinearPoints([]any{m[field], m[field]}); err != nil {
				return fmt.Errorf("segment %d %s must be a finite [x,y] point", i, field)
			}
		}
		start := m["start"].([]any)
		end := m["end"].([]any)
		sx, _ := finiteNumber(start[0])
		sy, _ := finiteNumber(start[1])
		ex, _ := finiteNumber(end[0])
		ey, _ := finiteNumber(end[1])
		if (sx == ex) == (sy == ey) {
			return fmt.Errorf("segment %d must be non-zero and axis-aligned", i)
		}
		if points != nil {
			if !samePoint(start, points[idx-1]) || !samePoint(end, points[idx]) {
				return fmt.Errorf("segment %d start/end must equal points[index-1] and points[index]", i)
			}
		}
	}
	return nil
}

func samePoint(a, b any) bool {
	ap, aok := a.([]any)
	bp, bok := b.([]any)
	if !aok || !bok || len(ap) != 2 || len(bp) != 2 {
		return false
	}
	ax, axok := finiteNumber(ap[0])
	ay, ayok := finiteNumber(ap[1])
	bx, bxok := finiteNumber(bp[0])
	by, byok := finiteNumber(bp[1])
	return axok && ayok && bxok && byok && ax == bx && ay == by
}

func validateLinearInput(c *cobra.Command, o *sceneFlags) error {
	changedAny := false
	for _, flag := range []string{"points", "arrow-type", "fixed-segments"} {
		changedAny = changedAny || c.Flags().Changed(flag)
	}
	if !changedAny {
		return errors.New("set at least one of --points, --arrow-type, or --fixed-segments")
	}
	if c.Flags().Changed("arrow-type") && !oneOf(o.arrowType, "sharp", "round", "elbow") {
		return errors.New("--arrow-type must be sharp, round, or elbow")
	}
	if c.Flags().Changed("fixed-segments") && o.arrowType != "elbow" {
		return errors.New("--fixed-segments requires --arrow-type elbow in the same command")
	}
	return nil
}

func applyLinearMutation(c *cobra.Command, e map[string]any, o *sceneFlags, points, segments []any) error {
	kind, _ := e["type"].(string)
	if kind != "line" && kind != "arrow" {
		return errors.New("--points, --arrow-type, and --fixed-segments require a line or arrow target")
	}
	if kind != "arrow" && (c.Flags().Changed("arrow-type") || c.Flags().Changed("fixed-segments")) {
		return errors.New("--arrow-type and --fixed-segments require an arrow target")
	}
	if c.Flags().Changed("points") {
		e["points"] = points
	}
	if c.Flags().Changed("arrow-type") {
		switch o.arrowType {
		case "sharp":
			e["roundness"], e["elbowed"] = nil, false
			delete(e, "fixedSegments")
		case "round":
			e["roundness"], e["elbowed"] = map[string]any{"type": 2}, false
			delete(e, "fixedSegments")
		case "elbow":
			e["roundness"], e["elbowed"] = nil, true
			if c.Flags().Changed("fixed-segments") {
				e["fixedSegments"] = segments
			} else if v, ok := e["fixedSegments"]; !ok || v == nil {
				e["fixedSegments"] = []any{}
			}
		}
	}
	if e["elbowed"] == true {
		p := points
		if p == nil {
			p, _ = e["points"].([]any)
		}
		if err := validateElbowPoints(p); err != nil {
			return err
		}
		if raw, ok := e["fixedSegments"].([]any); ok {
			if err := validateFixedSegments(raw, p); err != nil {
				return fmt.Errorf("fixedSegments: %w", err)
			}
		}
	}
	if c.Flags().Changed("points") {
		if err := normalizeLocalPoints(e); err != nil {
			return err
		}
	}
	updateLinearBounds(e)
	return nil
}

// normalizeLocalPoints preserves world geometry while enforcing Excalidraw's
// canonical linear/freedraw invariant that points[0] is [0,0]. The element's
// x/y become the old first point's world position. All element-local companion
// coordinates are translated by the same delta.
func normalizeLocalPoints(e map[string]any) error {
	points, ok := e["points"].([]any)
	if !ok || len(points) == 0 {
		return errors.New("points must be a non-empty array")
	}
	first, ok := points[0].([]any)
	if !ok || len(first) != 2 {
		return errors.New("points[0] must be [x,y]")
	}
	dx, okX := finiteNumber(first[0])
	dy, okY := finiteNumber(first[1])
	x, xOK := finiteNumber(e["x"])
	y, yOK := finiteNumber(e["y"])
	// Historical test fixtures and imported partial elements may omit x/y; the
	// rest of the semantic layer has always treated those as the scene origin.
	if _, exists := e["x"]; !exists {
		x, xOK = 0, true
	}
	if _, exists := e["y"]; !exists {
		y, yOK = 0, true
	}
	if !okX || !okY || !xOK || !yOK {
		return errors.New("cannot normalize points with non-finite x/y")
	}
	if dx == 0 && dy == 0 {
		return nil
	}
	translate := func(raw any) ([]any, error) {
		p, ok := raw.([]any)
		if !ok || len(p) != 2 {
			return nil, errors.New("local coordinate must be [x,y]")
		}
		px, pxOK := finiteNumber(p[0])
		py, pyOK := finiteNumber(p[1])
		if !pxOK || !pyOK {
			return nil, errors.New("local coordinate must be finite")
		}
		return []any{px - dx, py - dy}, nil
	}
	translated := make([]any, len(points))
	for i, raw := range points {
		p, err := translate(raw)
		if err != nil {
			return err
		}
		translated[i] = p
	}
	e["points"] = translated
	e["x"], e["y"] = x+dx, y+dy
	if last, exists := e["lastCommittedPoint"]; exists && last != nil {
		p, err := translate(last)
		if err != nil {
			return fmt.Errorf("lastCommittedPoint: %w", err)
		}
		e["lastCommittedPoint"] = p
	}
	if raw, exists := e["fixedSegments"]; exists && raw != nil {
		segments, ok := raw.([]any)
		if !ok {
			return errors.New("fixedSegments must be an array")
		}
		out := make([]any, len(segments))
		for i, item := range segments {
			segment, ok := item.(map[string]any)
			if !ok {
				return errors.New("fixedSegments entries must be objects")
			}
			clone := cloneMap(segment)
			for _, field := range []string{"start", "end"} {
				p, err := translate(clone[field])
				if err != nil {
					return fmt.Errorf("fixedSegments[%d].%s: %w", i, field, err)
				}
				clone[field] = p
			}
			out[i] = clone
		}
		e["fixedSegments"] = out
	}
	return nil
}

func validateElbowPoints(points []any) error {
	if err := validateLinearPoints(points); err != nil {
		return fmt.Errorf("elbow arrow points: %w", err)
	}
	for i := 1; i < len(points); i++ {
		a, b := points[i-1].([]any), points[i].([]any)
		ax, _ := finiteNumber(a[0])
		ay, _ := finiteNumber(a[1])
		bx, _ := finiteNumber(b[0])
		by, _ := finiteNumber(b[1])
		if (ax == bx) == (ay == by) {
			return fmt.Errorf("elbow arrow segment %d must be non-zero and axis-aligned", i)
		}
	}
	return nil
}

func updateLinearBounds(e map[string]any) {
	points, _ := e["points"].([]any)
	if len(points) == 0 {
		return
	}
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, raw := range points {
		p := raw.([]any)
		x, _ := finiteNumber(p[0])
		y, _ := finiteNumber(p[1])
		minX = math.Min(minX, x)
		minY = math.Min(minY, y)
		maxX = math.Max(maxX, x)
		maxY = math.Max(maxY, y)
	}
	e["width"], e["height"] = maxX-minX, maxY-minY
}

// validateCustomDataTextRuns enforces the schema-v3 textRuns contract on an
// element's customData: when a textRuns key is present (and non-null) it must be
// an array, and every entry must satisfy the same per-run contract as `--runs`
// input (validateRunStructure). This is the final wire guard for runs the CLI
// itself just wrote as well as for runs read back from an existing scene.
func validateCustomDataTextRuns(cd map[string]any) error {
	tr, exists := cd["textRuns"]
	if !exists || tr == nil {
		return nil
	}
	arr, ok := tr.([]any)
	if !ok {
		return errors.New("element customData.textRuns must be an array")
	}
	for i, item := range arr {
		if err := validateRunStructure(item); err != nil {
			return fmt.Errorf("element customData.textRuns[%d]: %w", i, err)
		}
	}
	return nil
}

func validateRunStructure(item any) error {
	m, ok := item.(map[string]any)
	if !ok {
		return errors.New("each run must be a JSON object")
	}
	allowed := map[string]bool{"start": true, "end": true, "fontFamily": true, "fontSize": true, "color": true, "bold": true, "italic": true, "underline": true}
	for k := range m {
		if !allowed[k] {
			return fmt.Errorf("unknown run field %q", k)
		}
	}
	start, ok := integerValue(m["start"])
	if !ok || start < 0 {
		return errors.New("run start must be a non-negative integer")
	}
	end, ok := integerValue(m["end"])
	if !ok {
		return errors.New("run end must be an integer")
	}
	if end <= start {
		return errors.New("run end must be greater than start (half-open [start,end) range)")
	}
	if v, ok := m["fontFamily"]; ok {
		if n, ok := finiteNumber(v); !ok || n <= 0 {
			return errors.New("run fontFamily must be a positive finite number")
		}
	}
	if v, ok := m["fontSize"]; ok {
		if n, ok := finiteNumber(v); !ok || n <= 0 {
			return errors.New("run fontSize must be a positive finite number")
		}
	}
	if v, ok := m["color"]; ok {
		if s, ok := v.(string); !ok || !validColor(s) {
			return errors.New("run color must be a bounded CSS color (<= 32 chars: hex #rgb/#rgba/#rrggbb/#rrggbbaa, a functional rgb()/rgba()/hsl()/hsla(), or a color keyword such as transparent)")
		}
	}
	for _, b := range []string{"bold", "italic", "underline"} {
		if v, ok := m[b]; ok {
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("run %s must be a boolean", b)
			}
		}
	}
	return nil
}

// styleKeys mirrors STYLE_KEYS in packages/whiteboard-schema/src/customData.ts:
// the ordered set of per-character style properties a run may carry.
var styleKeys = []string{"fontFamily", "fontSize", "color", "bold", "italic", "underline"}

// isStyleValue mirrors isStyleValue in the shared schema: fontFamily/fontSize are
// finite positive numbers (NOT integer-only — the CLI's friendly --font-family
// flag stays integer, but wire runs accept any canonical positive finite number),
// color is a conservative bounded CSS color, and bold/italic/underline are
// booleans. Anything else is not a valid style value for that key.
func isStyleValue(key string, v any) bool {
	switch key {
	case "fontFamily", "fontSize":
		n, ok := finiteNumber(v)
		return ok && n > 0
	case "color":
		s, ok := v.(string)
		return ok && validColor(s)
	default: // bold, italic, underline
		_, ok := v.(bool)
		return ok
	}
}

// readStyle mirrors readStyle in the shared schema: it collects ONLY the valid
// style properties off a candidate run, ignoring malformed ones, and stores each
// with a stable Go type (float64 numbers, string color, bool flags) so runs with
// identical styling compare equal (reflect.DeepEqual) for adjacent merging.
func readStyle(m map[string]any) map[string]any {
	s := map[string]any{}
	for _, key := range styleKeys {
		v, ok := m[key]
		if !ok || !isStyleValue(key, v) {
			continue
		}
		switch key {
		case "fontFamily", "fontSize":
			n, _ := finiteNumber(v)
			s[key] = n
		case "color":
			s[key], _ = v.(string)
		default:
			s[key], _ = v.(bool)
		}
	}
	return s
}

// clampOffset clamps a UTF-16 offset into [0, length] (matching clampOffset in the
// shared schema).
func clampOffset(offset, length int) int {
	if offset < 0 {
		return 0
	}
	if offset > length {
		return length
	}
	return offset
}

// normalizeTextRuns applies the schema-v3 textRuns contract with the SAME
// boundary-sweep algorithm as normalizeTextRuns in
// packages/whiteboard-schema/src/customData.ts:
//   - sanitize each run: drop non-object entries and non-integer start/end;
//   - clamp offsets to [0, textLen] in UTF-16 code units;
//   - read only valid style properties (isStyleValue); drop empty
//     (start >= end) or styleless runs;
//   - collect the unique sorted boundaries of every surviving run;
//   - for each adjacent boundary pair, compose the style property-by-property from
//     the runs that fully cover it, in input order, so LATER runs win only for the
//     properties they actually specify (an earlier run's other properties survive);
//   - append the composed span, dropping styleless spans and merging into the
//     previous span when adjacent and identically styled.
//
// Feeding the canonical output back in is a no-op (idempotent). An empty input
// clears textRuns. A non-empty input whose runs all fail to survive cleaning
// (fully out of range, or wholly styleless) is a fail-loud error — the established
// CLI boundary for --runs input that would apply nothing; canonical valid output,
// being non-empty, in-range and styled, always survives and never triggers it.
func normalizeTextRuns(runs []any, textLen int) ([]any, error) {
	length := textLen
	if length < 0 {
		length = 0
	}
	type cleanedRun struct {
		start, end int
		style      map[string]any
	}
	var cleaned []cleanedRun
	for _, item := range runs {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		start, sok := integerValue(m["start"])
		end, eok := integerValue(m["end"])
		if !sok || !eok {
			continue
		}
		start = clampOffset(start, length)
		end = clampOffset(end, length)
		style := readStyle(m)
		if start < end && len(style) > 0 {
			cleaned = append(cleaned, cleanedRun{start, end, style})
		}
	}
	if len(runs) > 0 && len(cleaned) == 0 {
		return nil, errors.New("no run falls within the text range")
	}
	if len(cleaned) == 0 {
		return []any{}, nil
	}

	seen := map[int]struct{}{}
	var boundaries []int
	for _, r := range cleaned {
		for _, b := range []int{r.start, r.end} {
			if _, dup := seen[b]; !dup {
				seen[b] = struct{}{}
				boundaries = append(boundaries, b)
			}
		}
	}
	sort.Ints(boundaries)

	type styledRun struct {
		start, end int
		style      map[string]any
	}
	var result []styledRun
	for i := 0; i+1 < len(boundaries); i++ {
		start, end := boundaries[i], boundaries[i+1]
		style := map[string]any{}
		// Later runs win per property; unspecified properties remain composable.
		for _, r := range cleaned {
			if r.start <= start && r.end >= end {
				for k, v := range r.style {
					style[k] = v
				}
			}
		}
		if start >= end || len(style) == 0 {
			continue
		}
		if n := len(result); n > 0 && result[n-1].end == start && reflect.DeepEqual(result[n-1].style, style) {
			result[n-1].end = end
			continue
		}
		result = append(result, styledRun{start, end, style})
	}

	out := make([]any, 0, len(result))
	for _, r := range result {
		run := map[string]any{"start": r.start, "end": r.end}
		for k, v := range r.style {
			run[k] = v
		}
		out = append(out, run)
	}
	return out, nil
}

func validateCreateFlags(o *sceneFlags) error {
	for name, value := range map[string]float64{"x": o.x, "y": o.y, "width": o.width, "height": o.height} {
		if !isFinite(value) {
			return fmt.Errorf("--%s must be finite", name)
		}
	}
	if o.width < 0 || o.height < 0 {
		return errors.New("--width and --height must be non-negative")
	}
	if err := validateStyleValues(o); err != nil {
		return err
	}
	return nil
}

func validateCreateInput(c *cobra.Command, o *sceneFlags) error {
	if c.Flags().Changed("preset") {
		if c.Flags().Changed("type") || c.Flags().Changed("native-shape-kind") || c.Flags().Changed("roundness") || c.Flags().Changed("arrow-type") {
			return errors.New("--preset cannot be combined with --type, --native-shape-kind, --roundness, or --arrow-type")
		}
		switch o.preset {
		case "rectangle": // compatibility alias for the toolbar primary
			o.typeName = "rectangle"
		case "rounded-rectangle":
			o.typeName, o.roundness = "rectangle", "round"
			_ = c.Flags().Set("roundness", "round")
		case "diamond", "ellipse":
			o.typeName = o.preset
		case "square", "circle", "triangle", "inverted-triangle", "parallelogram", "database", "notched-dovetail", "chevron", "trapezoid", "speech-bubble", "speech-bubble-rounded", "right-triangle", "star", "hexagon", "pentagon", "octagon", "left-arrow", "right-arrow", "bidirectional-arrow":
			o.typeName, o.nativeShapeKind = "rectangle", o.preset
			_ = c.Flags().Set("native-shape-kind", o.preset)
		case "curved-arrow":
			o.typeName, o.arrowType = "arrow", "round"
			_ = c.Flags().Set("arrow-type", "round")
		case "elbow-arrow":
			o.typeName, o.arrowType = "arrow", "elbow"
			_ = c.Flags().Set("arrow-type", "elbow")
		case "straight-arrow":
			o.typeName = "arrow"
		case "straight-line":
			o.typeName = "line"
		default:
			return errors.New("--preset must be one of the 21 Web shape flyout slots, 4 line flyout slots, or the rectangle/inverted-triangle compatibility aliases")
		}
	}
	if !oneOf(o.typeName, "rectangle", "ellipse", "diamond", "text", "arrow", "line", "freedraw", "embeddable") {
		return errors.New("--type must be rectangle, ellipse, diamond, text, arrow, line, freedraw, or embeddable")
	}
	if o.typeName == "embeddable" {
		if !c.Flags().Changed("url") {
			return errors.New("--url is required for --type embeddable")
		}
		normalized, ok := validateEmbeddableURL(o.embedURL)
		if !ok {
			return errors.New("--url must be an http/https URL whose host is one of the Excalidraw embeddable providers (youtube.com, youtu.be, vimeo.com, player.vimeo.com, figma.com, link.excalidraw.com, gist.github.com, twitter.com, x.com, *.simplepdf.eu, stackblitz.com, val.town, giphy.com, reddit.com); the Web board refuses to embed any other host")
		}
		o.embedURL = normalized
	} else if c.Flags().Changed("url") {
		return errors.New("--url is only valid for --type embeddable")
	}
	// Text-only flags must not be silently ignored on other shapes: reject them
	// loudly when explicitly set against a non-text --type.
	if o.typeName != "text" {
		for _, flag := range []string{"text", "baseline", "font-size", "font-family", "text-align", "vertical-align", "line-height", "bold", "italic", "underline"} {
			if c.Flags().Changed(flag) {
				return fmt.Errorf("--%s is only valid for --type text", flag)
			}
		}
	}
	if o.typeName != "arrow" && o.typeName != "line" {
		for _, flag := range []string{"start-arrowhead", "end-arrowhead"} {
			if c.Flags().Changed(flag) {
				return fmt.Errorf("--%s is only valid for --type arrow or line", flag)
			}
		}
	}
	if o.typeName != "arrow" && o.typeName != "line" && o.typeName != "freedraw" && c.Flags().Changed("points") {
		return errors.New("--points is only valid for --type arrow, line, or freedraw")
	}
	if o.typeName != "freedraw" {
		for _, flag := range []string{"pressures", "simulate-pressure", "last-committed-point"} {
			if c.Flags().Changed(flag) {
				return fmt.Errorf("--%s is only valid for --type freedraw", flag)
			}
		}
	}
	if o.typeName == "freedraw" {
		if !c.Flags().Changed("points") {
			return errors.New("--points is required for --type freedraw")
		}
	}
	if o.typeName != "arrow" {
		for _, flag := range []string{"arrow-type", "fixed-segments"} {
			if c.Flags().Changed(flag) {
				return fmt.Errorf("--%s is only valid for --type arrow", flag)
			}
		}
	}
	if o.typeName != "freedraw" && (c.Flags().Changed("points") || c.Flags().Changed("arrow-type") || c.Flags().Changed("fixed-segments")) {
		if err := validateLinearInput(c, o); err != nil {
			return err
		}
		if c.Flags().Changed("arrow-type") && c.Flags().Changed("roundness") {
			return errors.New("--arrow-type and --roundness cannot be used together")
		}
	}
	if c.Flags().Changed("native-shape-kind") {
		if o.typeName != "rectangle" {
			return errors.New("--native-shape-kind is only valid for --type rectangle")
		}
		if !isNativeShapeKind(o.nativeShapeKind) {
			return errors.New("--native-shape-kind must be one of the 19 Web native shape kinds")
		}
	}
	if c.Flags().Changed("database-rim-ratio") {
		if !isFinite(o.databaseRimRatio) || o.databaseRimRatio < 0.06 || o.databaseRimRatio > 0.4 {
			return errors.New("--database-rim-ratio must be finite and within 0.06..0.4")
		}
		if o.typeName != "rectangle" || o.nativeShapeKind != "database" {
			return errors.New("--database-rim-ratio requires --preset database or --type rectangle --native-shape-kind database")
		}
	}
	if c.Flags().Changed("roundness") {
		if !oneOf(o.roundness, "sharp", "round") {
			return errors.New("--roundness must be sharp or round")
		}
		if !oneOf(o.typeName, "rectangle", "diamond", "line", "arrow") {
			return fmt.Errorf("--roundness is not supported for --type %s", o.typeName)
		}
	}
	if err := validateStrokeStyleFlag(c, o); err != nil {
		return err
	}
	if err := validateArrowheadFlags(c, o); err != nil {
		return err
	}
	if err := validateTypographyFlags(c, o); err != nil {
		return err
	}
	if err := validateTriStateFlags(c, o); err != nil {
		return err
	}
	if err := validateCreateFlags(o); err != nil {
		return err
	}
	if o.typeName == "text" {
		if !c.Flags().Changed("text") || o.text == "" {
			return errors.New("--text is required and must not be empty for text")
		}
		for _, flag := range []string{"width", "height", "baseline"} {
			if !c.Flags().Changed(flag) {
				return fmt.Errorf("--%s is required for text; the CLI cannot reliably reproduce Excalidraw text measurement", flag)
			}
		}
		if o.baseline < 0 || !isFinite(o.baseline) {
			return errors.New("--baseline must be a finite non-negative number")
		}
	}
	return nil
}

func validateTransformInput(c *cobra.Command, o *sceneFlags) error {
	changed := false
	for k, v := range map[string]float64{"x": o.x, "y": o.y, "dx": o.dx, "dy": o.dy, "width": o.width, "height": o.height, "scale": o.scale, "angle": o.angle, "rotate-deg": o.rotateDeg} {
		if c.Flags().Changed(k) {
			changed = true
			if !isFinite(v) {
				return fmt.Errorf("--%s must be finite", k)
			}
			if (k == "width" || k == "height") && v < 0 {
				return fmt.Errorf("--%s must be non-negative", k)
			}
		}
	}
	if !changed {
		return errors.New("set at least one transform flag")
	}
	if c.Flags().Changed("x") && c.Flags().Changed("dx") {
		return errors.New("--x and --dx are mutually exclusive")
	}
	if c.Flags().Changed("y") && c.Flags().Changed("dy") {
		return errors.New("--y and --dy are mutually exclusive")
	}
	if c.Flags().Changed("angle") && c.Flags().Changed("rotate-deg") {
		return errors.New("--angle and --rotate-deg are mutually exclusive")
	}
	if c.Flags().Changed("scale") && (o.scale <= 0 || c.Flags().Changed("width") || c.Flags().Changed("height")) {
		return errors.New("--scale must be positive and cannot be combined with --width or --height")
	}
	return nil
}

func validateStyleInput(c *cobra.Command, o *sceneFlags) error {
	changed := false
	for _, name := range []string{"stroke-color", "background-color", "fill-style", "stroke-width", "roughness", "opacity", "font-size", "stroke-style", "roundness", "start-arrowhead", "end-arrowhead", "font-family", "text-align", "vertical-align", "line-height", "bold", "italic", "underline"} {
		changed = changed || c.Flags().Changed(name)
	}
	if !changed {
		return errors.New("set at least one style flag")
	}
	return validateChangedStyle(c, o)
}

func validateTextInput(c *cobra.Command, o *sceneFlags) error {
	changed := false
	for _, name := range []string{"text", "font-size", "font-family", "text-align", "vertical-align", "line-height", "runs"} {
		changed = changed || c.Flags().Changed(name)
	}
	if !changed {
		return errors.New("set at least one of --text, --font-size, --font-family, --text-align, --vertical-align, --line-height, or --runs")
	}
	if c.Flags().Changed("text") && o.text == "" {
		return errors.New("--text must not be empty")
	}
	return validateTypographyFlags(c, o)
}

func validateLayerInput(o *sceneFlags) error {
	if !oneOf(o.position, "front", "back", "before", "after") {
		return errors.New("--position must be front, back, before, or after")
	}
	if (o.position == "before" || o.position == "after") && o.relativeTo == "" {
		return errors.New("--relative-to is required for before/after")
	}
	if (o.position == "front" || o.position == "back") && o.relativeTo != "" {
		return errors.New("--relative-to is only valid for before/after")
	}
	return nil
}

func validateChangedStyle(c *cobra.Command, o *sceneFlags) error {
	if c.Flags().Changed("stroke-color") && strings.TrimSpace(o.strokeColor) == "" {
		return errors.New("--stroke-color must not be empty")
	}
	if c.Flags().Changed("background-color") && strings.TrimSpace(o.backgroundColor) == "" {
		return errors.New("--background-color must not be empty")
	}
	if c.Flags().Changed("fill-style") && !oneOf(o.fillStyle, "solid", "hachure", "cross-hatch", "zigzag") {
		return errors.New("--fill-style must be solid, hachure, cross-hatch, or zigzag")
	}
	if c.Flags().Changed("stroke-width") && o.strokeWidth < 0 {
		return errors.New("--stroke-width must be non-negative")
	}
	if c.Flags().Changed("roughness") && (o.roughness < 0 || o.roughness > 2) {
		return errors.New("--roughness must be 0, 1, or 2")
	}
	if c.Flags().Changed("opacity") && (o.opacity < 0 || o.opacity > 100) {
		return errors.New("--opacity must be between 0 and 100")
	}
	if c.Flags().Changed("roundness") && !oneOf(o.roundness, "sharp", "round") {
		return errors.New("--roundness must be sharp or round")
	}
	if err := validateStrokeStyleFlag(c, o); err != nil {
		return err
	}
	if err := validateArrowheadFlags(c, o); err != nil {
		return err
	}
	if err := validateTypographyFlags(c, o); err != nil {
		return err
	}
	return validateTriStateFlags(c, o)
}

func validateStrokeStyleFlag(c *cobra.Command, o *sceneFlags) error {
	if c.Flags().Changed("stroke-style") && !oneOf(o.strokeStyle, "solid", "dashed", "dotted") {
		return errors.New("--stroke-style must be solid, dashed, or dotted")
	}
	return nil
}

func validateArrowheadFlags(c *cobra.Command, o *sceneFlags) error {
	for flag, val := range map[string]string{"start-arrowhead": o.startArrowhead, "end-arrowhead": o.endArrowhead} {
		if c.Flags().Changed(flag) && !oneOf(val, validArrowheads...) {
			return fmt.Errorf("--%s must be one of: %s", flag, strings.Join(validArrowheads, ", "))
		}
	}
	return nil
}

func validateTypographyFlags(c *cobra.Command, o *sceneFlags) error {
	if c.Flags().Changed("font-size") && o.fontSize <= 0 {
		return errors.New("--font-size must be greater than zero")
	}
	if c.Flags().Changed("font-family") || o.fontFamily == 0 {
		fontFamily, err := resolveBoardFontFamily(o.fontFamilyInput)
		if err != nil {
			return err
		}
		o.fontFamily = fontFamily
	}
	if c.Flags().Changed("text-align") && !oneOf(o.textAlign, "left", "center", "right") {
		return errors.New("--text-align must be left, center, or right")
	}
	if c.Flags().Changed("vertical-align") && !oneOf(o.verticalAlign, "top", "middle", "bottom") {
		return errors.New("--vertical-align must be top, middle, or bottom")
	}
	if c.Flags().Changed("line-height") && (!isFinite(o.lineHeight) || o.lineHeight <= 0 || o.lineHeight > 10) {
		return errors.New("--line-height must be a finite number greater than 0 and at most 10")
	}
	return nil
}

func validateTriStateFlags(c *cobra.Command, o *sceneFlags) error {
	for flag, val := range map[string]string{"bold": o.bold, "italic": o.italic, "underline": o.underline} {
		if c.Flags().Changed(flag) && !oneOf(val, "true", "false") {
			return fmt.Errorf("--%s must be true or false", flag)
		}
	}
	return nil
}

func validateStyleValues(o *sceneFlags) error {
	if strings.TrimSpace(o.strokeColor) == "" {
		return errors.New("--stroke-color must not be empty")
	}
	if strings.TrimSpace(o.backgroundColor) == "" {
		return errors.New("--background-color must not be empty")
	}
	if !oneOf(o.fillStyle, "solid", "hachure", "cross-hatch", "zigzag") {
		return errors.New("--fill-style must be solid, hachure, cross-hatch, or zigzag")
	}
	if o.strokeWidth < 0 {
		return errors.New("--stroke-width must be non-negative")
	}
	if o.roughness < 0 || o.roughness > 2 {
		return errors.New("--roughness must be 0, 1, or 2")
	}
	if o.opacity < 0 || o.opacity > 100 {
		return errors.New("--opacity must be between 0 and 100")
	}
	if o.fontSize <= 0 {
		return errors.New("--font-size must be greater than zero")
	}
	return nil
}

func validateFinalElement(e map[string]any) error {
	id, ok := e["id"].(string)
	if !ok || id == "" {
		return errors.New("element id must be a non-empty string")
	}
	kind, ok := e["type"].(string)
	if !ok || !oneOf(kind, "rectangle", "ellipse", "diamond", "arrow", "line", "freedraw", "text", "image", "frame", "embeddable") {
		return errors.New("element type is invalid")
	}
	index, ok := e["index"].(string)
	if !ok || fracindex.ValidateOrderKey(index) != nil {
		return errors.New("element index is invalid")
	}
	if _, ok := positiveInteger(e["version"]); !ok {
		return errors.New("element version must be a positive integer")
	}
	if _, ok := nonNegativeInteger(e["versionNonce"]); !ok {
		return errors.New("element versionNonce must be a non-negative integer")
	}
	for _, field := range []string{"x", "y", "width", "height", "angle", "strokeWidth", "fontSize", "opacity", "roughness"} {
		if raw, exists := e[field]; exists {
			if _, ok := finiteNumber(raw); !ok {
				return fmt.Errorf("element %s must be finite", field)
			}
		}
	}
	if width, exists := e["width"]; exists {
		n, _ := finiteNumber(width)
		if n < 0 {
			return errors.New("element width must be non-negative")
		}
	}
	if height, exists := e["height"]; exists {
		n, _ := finiteNumber(height)
		if n < 0 {
			return errors.New("element height must be non-negative")
		}
	}
	if opacity, exists := e["opacity"]; exists {
		n, _ := finiteNumber(opacity)
		if n < 0 || n > 100 {
			return errors.New("element opacity must be between 0 and 100")
		}
	}
	if deleted, exists := e["isDeleted"]; exists {
		if _, ok := deleted.(bool); !ok {
			return errors.New("element isDeleted must be boolean")
		}
	}
	if locked, exists := e["locked"]; exists {
		if _, ok := locked.(bool); !ok {
			return errors.New("element locked must be boolean")
		}
	}
	if link, exists := e["link"]; exists && link != nil {
		if _, ok := link.(string); !ok {
			return errors.New("element link must be a string or null")
		}
	}
	if custom, exists := e["customData"]; exists && custom != nil {
		cd, ok := custom.(map[string]any)
		if !ok {
			return errors.New("element customData must be an object or null")
		}
		if err := validateCustomDataTextRuns(cd); err != nil {
			return err
		}
		if raw, exists := cd["nativeShapeKind"]; exists {
			value, validString := raw.(string)
			if kind != "rectangle" || !validString || !isNativeShapeKind(value) {
				return errors.New("element customData.nativeShapeKind requires a rectangle and one of the 19 Web native shape kinds")
			}
		}
		if raw, exists := cd["databaseRimRatio"]; exists {
			ratio, validNumber := finiteNumber(raw)
			if kind != "rectangle" || cd["nativeShapeKind"] != "database" || !validNumber || ratio < 0.06 || ratio > 0.4 {
				return errors.New("element customData.databaseRimRatio requires a database native shape and a finite value within 0.06..0.4")
			}
		}
	}
	if ss, exists := e["strokeStyle"]; exists && ss != nil {
		if _, ok := ss.(string); !ok {
			return errors.New("element strokeStyle must be a string")
		}
	}
	if r, exists := e["roundness"]; exists && r != nil {
		if _, ok := r.(map[string]any); !ok {
			return errors.New("element roundness must be an object or null")
		}
	}
	// Schema v3 stores rich-text runs at customData.textRuns (validated above). A
	// top-level textRuns is legacy/corrupt state: refuse to emit it rather than
	// silently preserve a malformed dual location on the wire.
	if _, exists := e["textRuns"]; exists {
		return errors.New("element carries a top-level textRuns field; schema v3 stores rich-text runs at customData.textRuns — remove the legacy field (e.g. set runs via `docs scene element text --runs`) before mutating this element")
	}
	if rough, exists := e["roughness"]; exists {
		n, _ := finiteNumber(rough)
		if n < 0 || n > 2 || n != math.Trunc(n) {
			return errors.New("element roughness must be 0, 1, or 2")
		}
	}
	if stroke, exists := e["strokeWidth"]; exists {
		n, _ := finiteNumber(stroke)
		if n < 0 {
			return errors.New("element strokeWidth must be non-negative")
		}
	}
	if fill, exists := e["fillStyle"]; exists {
		v, ok := fill.(string)
		if !ok || !oneOf(v, "solid", "hachure", "cross-hatch", "zigzag") {
			return errors.New("element fillStyle is invalid")
		}
	}
	for _, field := range []string{"strokeColor", "backgroundColor"} {
		if raw, exists := e[field]; exists {
			v, ok := raw.(string)
			if !ok || strings.TrimSpace(v) == "" {
				return fmt.Errorf("element %s must be a non-empty string", field)
			}
		}
	}
	if kind == "line" || kind == "arrow" {
		points, ok := e["points"].([]any)
		if !ok || len(points) < 2 {
			return errors.New("linear element points must contain at least two points")
		}
		for _, raw := range points {
			point, ok := raw.([]any)
			if !ok || len(point) != 2 {
				return errors.New("each linear element point must contain exactly two coordinates")
			}
			if _, ok := finiteNumber(point[0]); !ok {
				return errors.New("linear element point x must be finite")
			}
			if _, ok := finiteNumber(point[1]); !ok {
				return errors.New("linear element point y must be finite")
			}
		}
		if kind == "line" && e["elbowed"] == true {
			return errors.New("only arrow elements may be elbowed")
		}
		if e["elbowed"] == true {
			if err := validateElbowPoints(points); err != nil {
				return err
			}
			rawSegments := e["fixedSegments"]
			if rawSegments == nil {
				return nil
			}
			segments, ok := rawSegments.([]any)
			if !ok {
				return errors.New("elbow arrow fixedSegments must be an array")
			}
			if err := validateFixedSegments(segments, points); err != nil {
				return fmt.Errorf("elbow arrow fixedSegments: %w", err)
			}
		} else if v, ok := e["fixedSegments"]; ok && v != nil {
			return errors.New("fixedSegments are valid only on an elbow arrow")
		}
	}
	if kind == "freedraw" {
		// Existing historical freedraw elements are accepted as opaque read-back
		// shapes by the shared final validator. Commands that create or mutate
		// freedraw data validate the full schema before reaching this point.
		if _, hasPoints := e["points"]; hasPoints {
			if err := validateFreedrawElement(e); err != nil {
				return err
			}
		}
	}
	if kind == "text" {
		if font, ok := finiteNumber(e["fontSize"]); !ok || font <= 0 {
			return errors.New("text fontSize must be greater than zero")
		}
	}
	return nil
}

func elementIndex(e map[string]any) (string, error) {
	s, ok := e["index"].(string)
	if !ok || fracindex.ValidateOrderKey(s) != nil {
		return "", fmt.Errorf("element %v has invalid index", e["id"])
	}
	return s, nil
}

func applyLayer(target map[string]any, elements []map[string]any, position, relative string) error {
	ordered := make([]map[string]any, 0, len(elements)-1)
	for _, e := range elements {
		if e["id"] != target["id"] && e["isDeleted"] != true {
			ordered = append(ordered, e)
		}
	}
	for _, e := range ordered {
		if _, err := elementIndex(e); err != nil {
			return err
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i]["index"].(string) < ordered[j]["index"].(string) })
	for i := 1; i < len(ordered); i++ {
		if ordered[i-1]["index"] == ordered[i]["index"] {
			return fmt.Errorf("elements %v and %v have duplicate index %q", ordered[i-1]["id"], ordered[i]["id"], ordered[i]["index"])
		}
	}
	var lower, upper *string
	key := elementIndex
	switch position {
	case "front":
		if len(ordered) > 0 {
			v, err := key(ordered[len(ordered)-1])
			if err != nil {
				return err
			}
			lower = &v
		}
	case "back":
		if len(ordered) > 0 {
			v, err := key(ordered[0])
			if err != nil {
				return err
			}
			upper = &v
		}
	case "before", "after":
		if relative == "" {
			return errors.New("--relative-to is required for before/after")
		}
		if relative == target["id"] {
			return errors.New("--relative-to cannot reference the element being moved")
		}
		idx := -1
		for i, e := range ordered {
			if e["id"] == relative {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("reference element %q not found", relative)
		}
		if position == "before" {
			upperKey, err := key(ordered[idx])
			if err != nil {
				return err
			}
			upper = &upperKey
			if idx > 0 {
				lowerKey, err := key(ordered[idx-1])
				if err != nil {
					return err
				}
				lower = &lowerKey
			}
		} else {
			lowerKey, err := key(ordered[idx])
			if err != nil {
				return err
			}
			lower = &lowerKey
			if idx+1 < len(ordered) {
				upperKey, err := key(ordered[idx+1])
				if err != nil {
					return err
				}
				upper = &upperKey
			}
		}
	default:
		return errors.New("--position must be front, back, before, or after")
	}
	generated, err := fracindex.GenerateKeyBetween(lower, upper)
	if err != nil {
		return err
	}
	target["index"] = generated
	return nil
}
