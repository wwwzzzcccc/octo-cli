package cmd

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/spf13/cobra"

	"github.com/Mininglamp-OSS/octo-cli/internal/cmdutil"
	"github.com/Mininglamp-OSS/octo-cli/internal/output"
)

// Board content search mirrors Excalidraw 0.18.1 SearchMenu: trim the query,
// inspect only live text elements, search originalText as a case-insensitive
// literal substring, return each non-overlapping occurrence, and order elements
// top-to-bottom while retaining scene order for equal y values.
const (
	defaultFindLimit = 100
	maxFindLimit     = 1000
	previewUnits     = 24
)

func registerSceneFindCmd(scene *cobra.Command, f *cmdutil.Factory) {
	var limit int
	cmd := &cobra.Command{
		Use:   "find <docId> <query>",
		Short: "Find text occurrences using the Web board search semantics",
		Long: `Search a doc_type 'board' scene like the Web board's Ctrl/Cmd+F.

The query is trimmed, then matched literally and case-insensitively against
originalText on live text elements. Each non-overlapping occurrence is returned.
Bound labels are included as text elements and carry their containerId. Match
indexes and lengths use JavaScript UTF-16 code-unit offsets.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSceneFind(cmd, f, args[0], args[1], limit)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", defaultFindLimit, "maximum occurrences to return (1..1000)")
	scene.AddCommand(cmd)
}

func runSceneFind(cmd *cobra.Command, f *cmdutil.Factory, docID, rawQuery string, limit int) error {
	if limit < 1 || limit > maxFindLimit {
		return emitFindValidation(f,
			fmt.Sprintf("--limit must be between 1 and %d", maxFindLimit),
			"pass a positive limit within the bound")
	}

	s, err := getScene(cmd, f, docID)
	if err != nil {
		_ = f.EmitError(err) //nolint:errcheck // envelope on stderr; RunE returns the same error
		return err
	}

	query := strings.TrimSpace(rawQuery)
	matches, total := findTextMatches(s.Elements, query, limit)
	out := map[string]any{
		"docId":         docID,
		"query":         query,
		"caseSensitive": false,
		"count":         len(matches),
		"totalMatches":  total,
		"truncated":     total > len(matches),
		"matches":       matches,
		"baseVersion":   s.BaseVersion,
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return err
	}
	return f.EmitSuccess(encoded)
}

// findTextMatches returns one JSON-ready row per occurrence. It computes the
// uncapped total as well, so `truncated` is exact rather than inferred from
// merely reaching the limit.
func findTextMatches(elements []map[string]any, query string, limit int) ([]any, int) {
	if query == "" {
		return []any{}, 0
	}

	type orderedText struct {
		e          map[string]any
		sceneOrder int
		y          float64
	}
	texts := make([]orderedText, 0)
	for i, e := range elements {
		if e == nil || e["isDeleted"] == true || e["type"] != "text" {
			continue
		}
		if _, ok := e["originalText"].(string); !ok {
			continue
		}
		texts = append(texts, orderedText{e: e, sceneOrder: i, y: floatField(e, "y")})
	}
	sort.SliceStable(texts, func(i, j int) bool {
		if texts[i].y != texts[j].y {
			return texts[i].y < texts[j].y
		}
		return texts[i].sceneOrder < texts[j].sceneOrder
	})

	needle := utf16.Encode([]rune(query))
	matches := make([]any, 0, min(limit, len(texts)))
	total := 0
	for _, item := range texts {
		text := item.e["originalText"].(string)
		hay := utf16.Encode([]rune(text))
		for _, index := range utf16LiteralOffsets(hay, needle) {
			total++
			if len(matches) >= limit {
				continue
			}
			row := map[string]any{
				"textElementId": stringField(item.e, "id"),
				"index":         index,
				"length":        len(needle),
				"text":          utf16Preview(hay, index, len(needle)),
			}
			if x, ok := item.e["x"]; ok {
				row["x"] = x
			}
			if y, ok := item.e["y"]; ok {
				row["y"] = y
			}
			if cid, ok := item.e["containerId"].(string); ok && cid != "" {
				row["containerId"] = cid
				row["bound"] = true
			}
			if fid, ok := item.e["frameId"].(string); ok && fid != "" {
				row["frameId"] = fid
			}
			matches = append(matches, row)
		}
	}
	return matches, total
}

// utf16LiteralOffsets performs a non-overlapping case-insensitive scan over
// UTF-16 units. ASCII behavior is byte-for-byte equivalent to JS /literal/gi;
// non-ASCII simple-case pairs preserve UTF-16 offsets and do not normalize text.
func utf16LiteralOffsets(hay, needle []uint16) []int {
	if len(needle) == 0 || len(needle) > len(hay) {
		return nil
	}
	var out []int
	for i := 0; i+len(needle) <= len(hay); {
		if equalFoldUTF16(hay[i:i+len(needle)], needle) {
			out = append(out, i)
			i += len(needle) // JS global RegExp occurrences do not overlap
		} else {
			i++
		}
	}
	return out
}

func equalFoldUTF16(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] == b[i] {
			continue
		}
		ar, br := rune(a[i]), rune(b[i])
		if unicode.ToLower(ar) != unicode.ToLower(br) && unicode.ToUpper(ar) != unicode.ToUpper(br) {
			return false
		}
	}
	return true
}

func utf16Preview(text []uint16, start, length int) string {
	from := start - previewUnits
	if from < 0 {
		from = 0
	}
	to := start + length + previewUnits
	if to > len(text) {
		to = len(text)
	}
	var b strings.Builder
	if from > 0 {
		b.WriteRune('…')
	}
	b.WriteString(string(utf16.Decode(text[from:to])))
	if to < len(text) {
		b.WriteRune('…')
	}
	return b.String()
}

func floatField(e map[string]any, key string) float64 {
	if v, ok := e[key].(float64); ok {
		return v
	}
	return 0
}

func stringField(e map[string]any, key string) string {
	if v, ok := e[key].(string); ok {
		return v
	}
	return ""
}

func emitFindValidation(f *cmdutil.Factory, message, hint string) error {
	err := output.ErrValidation(message, hint)
	_ = f.EmitError(err) //nolint:errcheck // envelope on stderr; RunE returns the same error
	return err
}
