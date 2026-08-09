package cmd

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Mininglamp-OSS/octo-cli/internal/client"
	"github.com/Mininglamp-OSS/octo-cli/internal/cmdutil"
)

const (
	maxBoardCommentIDs = 100
	maxBoardCoordinate = 1_000_000
)

type boardCommentFlags struct {
	body       string
	point      string
	elementIDs []string
}

func registerDocsBoardCommentCmd(root *cobra.Command, f *cmdutil.Factory) {
	comments := commandAt(root, "docs", "comments")
	if comments == nil {
		return
	}
	o := &boardCommentFlags{}
	add := &cobra.Command{
		Use:   "add-board <docId>",
		Short: "Add a board comment with point/element anchor flags",
		Long: `Add a root comment. For boards, pass exactly one of --point X,Y or
repeatable --element-id ID. Element anchors are resolved from the live scene;
single-element markers use its top-right corner and multi-element markers use
the common bounding-box centre, matching the Web board. For other document
anchor forms, use --data with the generic API command.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDocsBoardCommentAdd(cmd, f, args[0], o)
		},
	}
	add.Flags().StringVar(&o.body, "body", "", "comment text (required)")
	add.Flags().StringVar(&o.point, "point", "", "board point anchor as X,Y")
	add.Flags().StringSliceVar(&o.elementIDs, "element-id", nil, "board element anchor; repeat for multi-selection")
	_ = add.MarkFlagRequired("body")
	comments.AddCommand(add)
}

func runDocsBoardCommentAdd(cmd *cobra.Command, f *cmdutil.Factory, docID string, o *boardCommentFlags) error {
	if strings.TrimSpace(o.body) == "" {
		return errors.New("--body must not be empty")
	}
	if (o.point == "") == (len(o.elementIDs) == 0) {
		return errors.New("set exactly one of --point or --element-id")
	}
	var anchor map[string]any
	var label string
	if o.point != "" {
		x, y, err := parseBoardPoint(o.point)
		if err != nil {
			return err
		}
		anchor = map[string]any{"version": 1, "kind": "point", "x": x, "y": y}
		label = fmt.Sprintf("Canvas point · %.0f, %.0f", x, y)
	} else {
		if len(o.elementIDs) > maxBoardCommentIDs {
			return fmt.Errorf("at most %d --element-id values are allowed", maxBoardCommentIDs)
		}
		s, err := getScene(cmd, f, docID)
		if err != nil {
			return err
		}
		anchor, label, err = boardElementAnchor(s.Elements, o.elementIDs)
		if err != nil {
			return err
		}
	}
	encoded, err := encodeBoardAnchor(anchor)
	if err != nil {
		return err
	}
	body := map[string]any{"body": o.body, "anchorStart": encoded, "anchorEnd": encoded, "anchorText": label}
	if f.Globals != nil && f.Globals.DryRun {
		data, _ := json.Marshal(map[string]any{"dry_run": true, "method": http.MethodPost, "path": "/v1/bot/docs/" + url.PathEscape(docID) + "/comments", "body": body})
		return f.EmitSuccess(data)
	}
	cli, err := f.Client()
	if err != nil {
		return err
	}
	result, err := cli.Do(cmd.Context(), &client.Request{Method: http.MethodPost, Path: "/v1/bot/docs/" + url.PathEscape(docID) + "/comments", Body: body, SuppressSpaceHeader: true})
	if err != nil {
		return err
	}
	return f.EmitSuccess(result)
}

func parseBoardPoint(value string) (float64, float64, error) {
	parts := strings.Split(value, ",")
	if len(parts) != 2 {
		return 0, 0, errors.New("--point must be X,Y")
	}
	x, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, errors.New("--point X must be a number")
	}
	y, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, 0, errors.New("--point Y must be a number")
	}
	if !isFinite(x) || !isFinite(y) || math.Abs(x) > maxBoardCoordinate || math.Abs(y) > maxBoardCoordinate {
		return 0, 0, fmt.Errorf("--point coordinates must be finite and within ±%d", maxBoardCoordinate)
	}
	return x, y, nil
}

func boardElementAnchor(elements []map[string]any, requested []string) (map[string]any, string, error) {
	byID := map[string]map[string]any{}
	for _, e := range elements {
		if id, ok := e["id"].(string); ok && e["isDeleted"] != true {
			byID[id] = e
		}
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(requested))
	for _, raw := range requested {
		id := strings.TrimSpace(raw)
		if id == "" || len(id) > 256 || strings.IndexFunc(id, func(r rune) bool { return r <= 0x20 || (r >= 0x7f && r <= 0x9f) }) >= 0 {
			return nil, "", fmt.Errorf("invalid element id %q", raw)
		}
		e := byID[id]
		if e == nil {
			return nil, "", fmt.Errorf("element %q not found or deleted", id)
		}
		if cid, ok := e["containerId"].(string); ok && cid != "" && byID[cid] != nil {
			id, e = cid, byID[cid]
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, "", errors.New("at least one live element is required")
	}
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, id := range ids {
		bounds, err := boardElementBounds(byID[id], byID)
		if err != nil {
			return nil, "", fmt.Errorf("element %q has invalid geometry: %w", id, err)
		}
		minX, minY = math.Min(minX, bounds.minX), math.Min(minY, bounds.minY)
		maxX, maxY = math.Max(maxX, bounds.maxX), math.Max(maxY, bounds.maxY)
	}
	x, y := (minX+maxX)/2, (minY+maxY)/2
	if len(ids) == 1 {
		x, y = maxX, minY
	}
	anchor := map[string]any{"version": 1, "kind": "element", "elementId": ids[0], "x": x, "y": y}
	if len(ids) > 1 {
		anchor["elementIds"] = ids
	}
	label := fmt.Sprintf("Canvas element · %s", ids[0])
	if len(ids) > 1 {
		sorted := append([]string(nil), ids...)
		sort.Strings(sorted)
		label = fmt.Sprintf("Canvas selection · %d", len(sorted))
	}
	return anchor, label, nil
}

type boardBounds struct {
	minX, minY, maxX, maxY float64
}

type boardPoint struct{ x, y float64 }

// boardElementBounds mirrors the geometry used by Excalidraw getCommonBounds:
// box elements use their rotated outline, while linear/freedraw elements derive
// their outline from the authoritative local points instead of stale width/height.
func boardElementBounds(e map[string]any, elements map[string]map[string]any) (boardBounds, error) {
	x, err := finiteBoardNumber(e, "x", true)
	if err != nil {
		return boardBounds{}, err
	}
	y, err := finiteBoardNumber(e, "y", true)
	if err != nil {
		return boardBounds{}, err
	}
	angle, err := finiteBoardNumber(e, "angle", false)
	if err != nil {
		return boardBounds{}, err
	}
	typeName, _ := e["type"].(string)
	if typeName == "line" || typeName == "arrow" || typeName == "freedraw" {
		points, err := boardLocalPoints(e["points"])
		if err != nil {
			return boardBounds{}, err
		}
		local := boundsOfBoardPoints(points)
		cx, cy := (local.minX+local.maxX)/2, (local.minY+local.maxY)/2
		if typeName == "line" || typeName == "arrow" {
			return boardLinearElementBounds(e, elements, points, x, y, cx, cy, angle)
		}
		rotated := make([]boardPoint, 0, len(points))
		for _, point := range points {
			rx, ry := rotateBoardPoint(point.x, point.y, cx, cy, angle)
			rotated = append(rotated, boardPoint{x: x + rx, y: y + ry})
		}
		return boundsOfBoardPoints(rotated), nil
	}
	w, err := finiteBoardNumber(e, "width", true)
	if err != nil || w < 0 {
		return boardBounds{}, errors.New("width must be a finite non-negative number")
	}
	h, err := finiteBoardNumber(e, "height", true)
	if err != nil || h < 0 {
		return boardBounds{}, errors.New("height must be a finite non-negative number")
	}
	cx, cy := x+w/2, y+h/2
	if typeName == "ellipse" {
		cos, sin := math.Cos(angle), math.Sin(angle)
		ex := math.Hypot(w/2*cos, h/2*sin)
		ey := math.Hypot(h/2*cos, w/2*sin)
		return boardBounds{cx - ex, cy - ey, cx + ex, cy + ey}, nil
	}
	var points []boardPoint
	if typeName == "diamond" {
		points = []boardPoint{{cx, y}, {x + w, cy}, {cx, y + h}, {x, cy}}
	} else {
		points = []boardPoint{{x, y}, {x + w, y}, {x + w, y + h}, {x, y + h}}
	}
	for i := range points {
		points[i].x, points[i].y = rotateBoardPoint(points[i].x, points[i].y, cx, cy, angle)
	}
	return boundsOfBoardPoints(points), nil
}

func finiteBoardNumber(e map[string]any, key string, required bool) (float64, error) {
	value, ok := e[key]
	if !ok && !required {
		return 0, nil
	}
	n, ok := value.(float64)
	if !ok || !isFinite(n) || math.Abs(n) > maxBoardCoordinate*2 {
		return 0, fmt.Errorf("%s must be a finite number", key)
	}
	return n, nil
}

func boardLocalPoints(raw any) ([]boardPoint, error) {
	values, ok := raw.([]any)
	if !ok || len(values) == 0 {
		return nil, errors.New("points must contain at least one point")
	}
	points := make([]boardPoint, 0, len(values))
	for _, value := range values {
		pair, ok := value.([]any)
		if !ok || len(pair) < 2 {
			return nil, errors.New("points contains an invalid point")
		}
		px, xOK := pair[0].(float64)
		py, yOK := pair[1].(float64)
		if !xOK || !yOK || !isFinite(px) || !isFinite(py) || math.Abs(px) > maxBoardCoordinate*2 || math.Abs(py) > maxBoardCoordinate*2 {
			return nil, errors.New("points contains a non-finite or out-of-range point")
		}
		points = append(points, boardPoint{px, py})
	}
	return points, nil
}

type boardCubic struct{ p0, p1, p2, p3 boardPoint }

type roughRandom struct{ seed int32 }

func (r *roughRandom) next() float64 {
	r.seed = int32(uint32(r.seed*48271) & 0x7fffffff)
	return float64(r.seed) / 2147483648
}

// boardLinearElementBounds ports the RoughJS 4.6.4 linearPath path generation
// used by Excalidraw 0.18.1, then evaluates cubic extrema exactly. Arrowheads
// are intentionally excluded because getCommonBounds excludes them too.
func boardLinearElementBounds(e map[string]any, elements map[string]map[string]any, points []boardPoint, x, y, pointCX, pointCY, angle float64) (boardBounds, error) {
	if len(points) == 1 {
		rx, ry := rotateBoardPoint(x+points[0].x, y+points[0].y, x+pointCX, y+pointCY, angle)
		return boardBounds{rx, ry, rx, ry}, nil
	}
	seed := float64(0)
	if rawSeed, ok := e["seed"]; ok {
		var seedOK bool
		seed, seedOK = rawSeed.(float64)
		if !seedOK || !isFinite(seed) || seed < 0 || seed > math.MaxInt32 {
			return boardBounds{}, errors.New("seed must be a finite 32-bit integer")
		}
	}
	roughness, err := finiteBoardNumber(e, "roughness", false)
	if err != nil {
		return boardBounds{}, err
	}
	if _, ok := e["roughness"]; !ok {
		roughness = 1
	}
	strokeStyle, _ := e["strokeStyle"].(string)
	disableMultiStroke := strokeStyle != "" && strokeStyle != "solid"
	preserveVertices := roughness < 2
	local := boundsOfBoardPoints(points)
	maxSize, minSize := local.maxX-local.minX, local.maxY-local.minY
	if minSize > maxSize {
		minSize, maxSize = maxSize, minSize
	}
	adjusted := roughness
	if !((minSize >= 20 && maxSize >= 50) || maxSize >= 50) {
		divisor := 2.0
		if maxSize < 10 {
			divisor = 3
		}
		adjusted = math.Min(roughness/divisor, 2.5)
	}
	rng := roughRandom{seed: int32(seed)}
	cubics := make([]boardCubic, 0, (len(points)-1)*2)
	if e["roundness"] != nil {
		cubics = roughCurveCubics(points, adjusted, disableMultiStroke, &rng, int32(seed))
	} else {
		for i := 0; i+1 < len(points); i++ {
			cubics = append(cubics, roughLineCubics(points[i], points[i+1], adjusted, preserveVertices, disableMultiStroke, &rng)...)
		}
	}
	// getElementAbsoluteCoords runs before getLinearElementRotatedBounds. With
	// no pre-existing ShapeCache (the deterministic CLI case), Excalidraw uses
	// the raw points' center as the rotation pivot.
	bounds := boardBounds{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	transform := func(p boardPoint) boardPoint {
		rx, ry := rotateBoardPoint(x+p.x, y+p.y, x+pointCX, y+pointCY, angle)
		return boardPoint{rx, ry}
	}
	for _, cubic := range cubics {
		growBoardCubicBounds(&bounds, transform(cubic.p0), transform(cubic.p1), transform(cubic.p2), transform(cubic.p3))
	}
	if text := boundBoardText(e, elements); text != nil {
		midpointCubics := cubics
		if e["roundness"] != nil && len(midpointCubics) > len(points)-1 {
			// getControlPointsForBezierCurve reads shape[0], i.e. the first
			// RoughJS stroke. Bounds include both strokes, but midpoint lookup
			// must not choose a closer endpoint from the overlay stroke.
			midpointCubics = midpointCubics[:len(points)-1]
		}
		midpointCX, midpointCY := pointCX, pointCY
		if e["roundness"] != nil {
			curveBounds := boardBounds{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
			for _, cubic := range cubics {
				growBoardCubicBounds(&curveBounds, cubic.p0, cubic.p1, cubic.p2, cubic.p3)
			}
			midpointCX = (curveBounds.minX + curveBounds.maxX) / 2
			midpointCY = (curveBounds.minY + curveBounds.maxY) / 2
		}
		return boardBoundsWithBoundText(bounds, points, midpointCubics, e["roundness"] != nil, x, y, pointCX, pointCY, midpointCX, midpointCY, angle, text)
	}
	return bounds, nil
}

func boundBoardText(linear map[string]any, elements map[string]map[string]any) map[string]any {
	refs, _ := linear["boundElements"].([]any)
	for _, raw := range refs {
		ref, ok := raw.(map[string]any)
		if !ok || ref["type"] != "text" {
			continue
		}
		id, _ := ref["id"].(string)
		text := elements[id]
		if text != nil && text["isDeleted"] != true && text["type"] == "text" {
			return text
		}
	}
	return nil
}

// boardBoundsWithBoundText mirrors LinearElementEditor.getMinMaxXYWithBoundText.
// The text is positioned at the middle point/segment of the connector and its
// counter-rotated rectangle expands the connector bounds in the same quadrant-
// sensitive way as Excalidraw 0.18.1.
func boardBoundsWithBoundText(bounds boardBounds, points []boardPoint, curveCubics []boardCubic, rounded bool, x, y, localCX, localCY, midpointCX, midpointCY, angle float64, text map[string]any) (boardBounds, error) {
	w, err := finiteBoardNumber(text, "width", true)
	if err != nil || w < 0 {
		return boardBounds{}, errors.New("bound text width must be a finite non-negative number")
	}
	h, err := finiteBoardNumber(text, "height", true)
	if err != nil || h < 0 {
		return boardBounds{}, errors.New("bound text height must be a finite non-negative number")
	}
	global := func(p boardPoint) boardPoint {
		rx, ry := rotateBoardPoint(x+p.x, y+p.y, x+localCX, y+localCY, angle)
		return boardPoint{rx, ry}
	}
	globalPoints := make([]boardPoint, len(points))
	for i, point := range points {
		globalPoints[i] = global(point)
	}
	var midpoint boardPoint
	if len(points)%2 == 1 {
		midpoint = globalPoints[len(points)/2]
	} else {
		i := len(points)/2 - 1
		a, b := globalPoints[i], globalPoints[i+1]
		midpoint = boardPoint{(a.x + b.x) / 2, (a.y + b.y) / 2}
		if len(points) > 2 && rounded {
			if localMidpoint, ok := roundedBoardSegmentMidpoint(curveCubics, points[i+1]); ok {
				rx, ry := rotateBoardPoint(x+localMidpoint.x, y+localMidpoint.y, x+midpointCX, y+midpointCY, angle)
				midpoint = boardPoint{rx, ry}
			}
		}
	}
	// Octo's Excalidraw patch keeps connector labels outside the line. It moves
	// the centered label along the middle segment's deterministic normal by its
	// projected half-extent plus BOUND_TEXT_PADDING (5px).
	pointBounds := boundsOfBoardPoints(globalPoints)
	normalX := midpoint.x - (pointBounds.minX+pointBounds.maxX)/2
	normalY := midpoint.y - (pointBounds.minY+pointBounds.maxY)/2
	normalLength := math.Hypot(normalX, normalY)
	if normalLength < .5 {
		middle := len(globalPoints) / 2
		start := globalPoints[maxInt(0, middle-1)]
		endIndex := middle
		if len(globalPoints)%2 == 1 {
			endIndex = minInt(len(globalPoints)-1, middle+1)
		}
		end := globalPoints[endIndex]
		tangentX, tangentY := end.x-start.x, end.y-start.y
		normalX, normalY = -tangentY, tangentX
		normalLength = math.Hypot(normalX, normalY)
		if normalLength == 0 {
			normalLength = 1
		}
		normalX, normalY = normalX/normalLength, normalY/normalLength
		if math.Abs(tangentX) >= math.Abs(tangentY) {
			if normalY > 0 {
				normalX, normalY = -normalX, -normalY
			}
		} else if normalX > 0 {
			normalX, normalY = -normalX, -normalY
		}
	} else {
		normalX, normalY = normalX/normalLength, normalY/normalLength
	}
	clearance := math.Abs(normalX)*w/2 + math.Abs(normalY)*h/2 + 5
	midpoint.x += normalX * clearance
	midpoint.y += normalY * clearance
	textX1, textY1 := midpoint.x-w/2, midpoint.y-h/2
	textX2, textY2 := textX1+w, textY1+h
	cx, cy := (bounds.minX+bounds.maxX)/2, (bounds.minY+bounds.maxY)/2
	rotate := func(px, py, radians float64) boardPoint {
		rx, ry := rotateBoardPoint(px, py, cx, cy, radians)
		return boardPoint{rx, ry}
	}
	topLeft := rotate(bounds.minX, bounds.minY, angle)
	topRight := rotate(bounds.maxX, bounds.minY, angle)
	ctl := rotate(textX1, textY1, -angle)
	ctr := rotate(textX2, textY1, -angle)
	cbl := rotate(textX1, textY2, -angle)
	cbr := rotate(textX2, textY2, -angle)
	if topLeft.x < topRight.x && topLeft.y >= topRight.y {
		bounds.minX = math.Min(bounds.minX, cbl.x)
		bounds.maxX = math.Max(bounds.maxX, math.Max(ctr.x, cbr.x))
		bounds.minY = math.Min(bounds.minY, ctl.y)
		bounds.maxY = math.Max(bounds.maxY, cbr.y)
	} else if topLeft.x >= topRight.x && topLeft.y > topRight.y {
		bounds.minX = math.Min(bounds.minX, cbr.x)
		bounds.maxX = math.Max(bounds.maxX, math.Max(ctl.x, ctr.x))
		bounds.minY = math.Min(bounds.minY, cbl.y)
		bounds.maxY = math.Max(bounds.maxY, ctr.y)
	} else if topLeft.x >= topRight.x {
		bounds.minX = math.Min(bounds.minX, ctr.x)
		bounds.maxX = math.Max(bounds.maxX, cbl.x)
		bounds.minY = math.Min(bounds.minY, cbr.y)
		bounds.maxY = math.Max(bounds.maxY, ctl.y)
	} else if topLeft.y <= topRight.y {
		bounds.minX = math.Min(bounds.minX, math.Min(ctr.x, ctl.x))
		bounds.maxX = math.Max(bounds.maxX, cbr.x)
		bounds.minY = math.Min(bounds.minY, ctr.y)
		bounds.maxY = math.Max(bounds.maxY, cbl.y)
	}
	return bounds, nil
}

// roundedBoardSegmentMidpoint mirrors getControlPointsForBezierCurve +
// mapIntervalToBezierT(..., 0.5). Excalidraw samples the selected RoughJS cubic
// backwards in t steps of .05, approximates arc length, then maps half that
// length back to the same reversed t parameter.
func roundedBoardSegmentMidpoint(cubics []boardCubic, endPoint boardPoint) (boardPoint, bool) {
	minDistance := math.Inf(1)
	var selected boardCubic
	found := false
	for _, cubic := range cubics {
		distance := math.Hypot(cubic.p3.x-endPoint.x, cubic.p3.y-endPoint.y)
		if distance < minDistance {
			minDistance = distance
			selected = cubic
			found = true
		}
	}
	if !found {
		return boardPoint{}, false
	}
	points := make([]boardPoint, 0, 20)
	for step := 20; step > 0; step-- {
		points = append(points, boardCubicPoint(selected, float64(step)/20))
	}
	arcLengths := make([]float64, len(points))
	for i := 0; i+1 < len(points); i++ {
		arcLengths[i+1] = arcLengths[i] + math.Hypot(points[i+1].x-points[i].x, points[i+1].y-points[i].y)
	}
	if len(arcLengths) < 2 || arcLengths[len(arcLengths)-1] == 0 {
		return boardCubicPoint(selected, .5), true
	}
	target := arcLengths[len(arcLengths)-1] / 2
	low, high, index := 0, len(arcLengths)-1, 0
	for low < high {
		index = low + (high-low)/2
		if arcLengths[index] < target {
			low = index + 1
		} else {
			high = index
		}
	}
	if arcLengths[index] > target {
		index--
	}
	pointsCount := len(arcLengths) - 1
	var t float64
	if arcLengths[index] == target {
		t = float64(index) / float64(pointsCount)
	} else {
		t = 1 - (float64(index)+(target-arcLengths[index])/(arcLengths[index+1]-arcLengths[index]))/float64(pointsCount)
	}
	return boardCubicPoint(selected, t), true
}

func boardCubicPoint(cubic boardCubic, t float64) boardPoint {
	// Preserve Excalidraw's getBezierXY parameter orientation: p3 carries
	// (1-t)^3 and p0 carries t^3.
	u := 1 - t
	return boardPoint{
		x: u*u*u*cubic.p3.x + 3*t*u*u*cubic.p2.x + 3*t*t*u*cubic.p1.x + t*t*t*cubic.p0.x,
		y: u*u*u*cubic.p3.y + 3*t*u*u*cubic.p2.y + 3*t*t*u*cubic.p1.y + t*t*t*cubic.p0.y,
	}
}

func roughCurveCubics(points []boardPoint, roughness float64, single bool, rng *roughRandom, seed int32) []boardCubic {
	curveWithOffset := func(offset float64, random *roughRandom) []boardCubic {
		perturbed := make([]boardPoint, 0, len(points)+2)
		randomOffset := func() float64 { return roughness * (random.next()*2*offset - offset) }
		first := boardPoint{points[0].x + randomOffset(), points[0].y + randomOffset()}
		perturbed = append(perturbed, first, boardPoint{points[0].x + randomOffset(), points[0].y + randomOffset()})
		for i := 1; i < len(points); i++ {
			perturbed = append(perturbed, boardPoint{points[i].x + randomOffset(), points[i].y + randomOffset()})
			if i == len(points)-1 {
				perturbed = append(perturbed, boardPoint{points[i].x + randomOffset(), points[i].y + randomOffset()})
			}
		}
		out := make([]boardCubic, 0, len(perturbed)-3)
		for i := 1; i+2 < len(perturbed); i++ {
			p0, next, prev, after := perturbed[i], perturbed[i+1], perturbed[i-1], perturbed[i+2]
			out = append(out, boardCubic{
				p0: p0,
				p1: boardPoint{p0.x + (next.x-prev.x)/6, p0.y + (next.y-prev.y)/6},
				p2: boardPoint{next.x + (p0.x-after.x)/6, next.y + (p0.y-after.y)/6},
				p3: next,
			})
		}
		return out
	}
	out := curveWithOffset(1*(1+roughness*.2), rng)
	if !single {
		second := roughRandom{seed: seed + 1}
		out = append(out, curveWithOffset(1.5*(1+roughness*.22), &second)...)
	}
	return out
}

func roughLineCubics(a, b boardPoint, roughness float64, preserve, single bool, rng *roughRandom) []boardCubic {
	makeCubic := func(overlay bool) boardCubic {
		dx, dy := a.x-b.x, a.y-b.y
		length := math.Hypot(dx, dy)
		gain := 1.0
		if length > 500 {
			gain = .4
		} else if length >= 200 {
			gain = -0.0016668*length + 1.233334
		}
		offset := 2.0
		if offset*offset*100 > dx*dx+dy*dy {
			offset = length / 10
		}
		half := offset / 2
		randomOffset := func(limit float64) float64 { return roughness * gain * (rng.next()*2*limit - limit) }
		diverge := .2 + rng.next()*.2
		// RoughJS passes the signed bowing displacement to _offsetOpt. Keeping
		// the sign is observable for negative-slope and multi-segment paths.
		midX := randomOffset((b.y - a.y) / 100)
		midY := randomOffset((a.x - b.x) / 100)
		limit := offset
		if overlay {
			limit = half
		}
		start := a
		if !preserve {
			start = boardPoint{a.x + randomOffset(limit), a.y + randomOffset(limit)}
		}
		p1 := boardPoint{midX + a.x + (b.x-a.x)*diverge + randomOffset(limit), midY + a.y + (b.y-a.y)*diverge + randomOffset(limit)}
		p2 := boardPoint{midX + a.x + 2*(b.x-a.x)*diverge + randomOffset(limit), midY + a.y + 2*(b.y-a.y)*diverge + randomOffset(limit)}
		end := b
		if !preserve {
			end = boardPoint{b.x + randomOffset(limit), b.y + randomOffset(limit)}
		}
		return boardCubic{p0: start, p1: p1, p2: p2, p3: end}
	}
	first := makeCubic(false)
	if single {
		return []boardCubic{first}
	}
	return []boardCubic{first, makeCubic(true)}
}

func growBoardCubicBounds(bounds *boardBounds, p0, p1, p2, p3 boardPoint) {
	values := []boardPoint{p0, p3}
	for axis := 0; axis < 2; axis++ {
		coord := func(p boardPoint) float64 {
			if axis == 0 {
				return p.x
			}
			return p.y
		}
		a, b, c := 3*(coord(p1)-coord(p0))-6*(coord(p2)-coord(p1))+3*(coord(p3)-coord(p2)), 6*(coord(p2)-coord(p1))-6*(coord(p1)-coord(p0)), 3*(coord(p1)-coord(p0))
		roots := quadraticBoardRoots(a, b, c)
		for _, t := range roots {
			if t > 0 && t < 1 {
				u := 1 - t
				values = append(values, boardPoint{
					x: u*u*u*p0.x + 3*u*u*t*p1.x + 3*u*t*t*p2.x + t*t*t*p3.x,
					y: u*u*u*p0.y + 3*u*u*t*p1.y + 3*u*t*t*p2.y + t*t*t*p3.y,
				})
			}
		}
	}
	for _, p := range values {
		bounds.minX, bounds.minY = math.Min(bounds.minX, p.x), math.Min(bounds.minY, p.y)
		bounds.maxX, bounds.maxY = math.Max(bounds.maxX, p.x), math.Max(bounds.maxY, p.y)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func quadraticBoardRoots(a, b, c float64) []float64 {
	if math.Abs(a) < 1e-12 {
		if math.Abs(b) < 1e-12 {
			return nil
		}
		return []float64{-c / b}
	}
	disc := b*b - 4*a*c
	if disc < 0 {
		return nil
	}
	sqrt := math.Sqrt(disc)
	return []float64{(-b + sqrt) / (2 * a), (-b - sqrt) / (2 * a)}
}

func rotateBoardPoint(x, y, cx, cy, angle float64) (float64, float64) {
	cos, sin := math.Cos(angle), math.Sin(angle)
	dx, dy := x-cx, y-cy
	return cx + dx*cos - dy*sin, cy + dx*sin + dy*cos
}

func boundsOfBoardPoints(points []boardPoint) boardBounds {
	bounds := boardBounds{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, point := range points {
		bounds.minX = math.Min(bounds.minX, point.x)
		bounds.minY = math.Min(bounds.minY, point.y)
		bounds.maxX = math.Max(bounds.maxX, point.x)
		bounds.maxY = math.Max(bounds.maxY, point.y)
	}
	return bounds
}

func encodeBoardAnchor(anchor map[string]any) (string, error) {
	data, err := json.Marshal(anchor)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}
