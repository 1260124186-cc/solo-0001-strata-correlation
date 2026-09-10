package importing

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
)

// Columns are the required CSV header, in any order.
var Columns = []string{"name", "site", "depth_mm", "note", "top_mm", "bottom_mm", "rock", "description", "marker"}

var layerFieldPattern = regexp.MustCompile(`^layers\[(\d+)\](?:\.(.+))?$`)

// StructureError describes a malformed CSV that cannot even reach a preview
// (bad header, broken quoting or no records).
type StructureError struct {
	Detail string
}

func (e *StructureError) Error() string { return e.Detail }

type builder struct {
	line     int
	name     string
	site     string
	depthRaw string
	depth    int64
	note     string
	layers   []LayerDraft
}

// Parse turns raw CSV bytes into a preview Import without creating any profile.
// Geological rules are exactly the existing geology package rules; row errors
// carry the original line numbers and raw fields.
func Parse(source []byte, now time.Time) (Import, error) {
	reader := csv.NewReader(bytes.NewReader(source))
	reader.FieldsPerRecord = -1 // ragged rows become preview errors instead of aborting the parse

	header, err := reader.Read()
	if err != nil {
		return Import{}, &StructureError{Detail: "CSV 缺少表头或内容为空"}
	}
	if len(header) > 0 {
		header[0] = strings.TrimPrefix(header[0], "\uFEFF")
	}
	if err = checkHeader(header); err != nil {
		return Import{}, err
	}
	index := make(map[string]int, len(header))
	for i, col := range header {
		index[strings.TrimSpace(col)] = i
	}
	cell := func(rec []string, col string) string {
		if i := index[col]; i < len(rec) {
			return rec[i]
		}
		return ""
	}

	// Keep slices non-nil even when empty so stored previews stay
	// DeepEqual-stable across Clone and JSON snapshot round-trips.
	rowErrors := []RowError{}
	builders := make(map[string]*builder)
	order := []string{}
	dataRows := 0

	for {
		rec, readErr := reader.Read()
		if readErr == io.EOF {
			break
		}
		line, _ := reader.FieldPos(0) // position of the record just read
		if readErr != nil {
			if parseErr, ok := readErr.(*csv.ParseError); ok && parseErr.Line > 0 {
				line = parseErr.Line
			}
			// with FieldsPerRecord=-1 only unrecoverable quoting errors reach here
			return Import{}, &StructureError{Detail: fmt.Sprintf("CSV 格式错误，第 %d 行无法解析", line)}
		}
		if isEmptyRecord(rec) {
			continue // tolerate blank lines common in spreadsheet exports
		}
		dataRows++

		raw := RawFields{
			Name: cell(rec, "name"), Site: cell(rec, "site"), DepthMM: cell(rec, "depth_mm"),
			Note: cell(rec, "note"), TopMM: cell(rec, "top_mm"), BottomMM: cell(rec, "bottom_mm"),
			Rock: cell(rec, "rock"), Description: cell(rec, "description"), Marker: cell(rec, "marker"),
		}
		if len(rec) > len(header) {
			raw.Extra = append([]string{}, rec[len(header):]...)
		}

		name := strings.TrimSpace(raw.Name)
		if name == "" {
			rowErrors = append(rowErrors, RowError{Line: line, Field: "name", Detail: "剖面名称不能为空", Raw: raw})
			continue
		}
		if len(rec) != len(header) {
			rowErrors = append(rowErrors, RowError{Line: line, Field: "row", Detail: fmt.Sprintf("列数为 %d，表头要求 %d 列", len(rec), len(header)), Raw: raw})
			continue
		}

		site := strings.TrimSpace(raw.Site)
		depthRaw := strings.TrimSpace(raw.DepthMM)
		note := strings.TrimSpace(raw.Note)
		b, exists := builders[name]
		if !exists {
			b = &builder{line: line, name: name, site: site, depthRaw: depthRaw, note: note}
			if n, perr := strconv.ParseInt(depthRaw, 10, 64); perr == nil {
				b.depth = n
			}
			builders[name] = b
			order = append(order, name)
		} else {
			if site != b.site {
				rowErrors = append(rowErrors, RowError{Line: line, Field: "site", Detail: fmt.Sprintf("与第 %d 行的地点不一致", b.line), Raw: raw})
			}
			if depthRaw != b.depthRaw {
				rowErrors = append(rowErrors, RowError{Line: line, Field: "depth_mm", Detail: fmt.Sprintf("与第 %d 行的总深度不一致", b.line), Raw: raw})
			}
			if note != b.note {
				rowErrors = append(rowErrors, RowError{Line: line, Field: "note", Detail: fmt.Sprintf("与第 %d 行的说明不一致", b.line), Raw: raw})
			}
		}

		top, topErr := strconv.ParseInt(strings.TrimSpace(raw.TopMM), 10, 64)
		if topErr != nil {
			rowErrors = append(rowErrors, RowError{Line: line, Field: "top_mm", Detail: "顶深必须为整数毫米", Raw: raw})
			continue
		}
		bottom, bottomErr := strconv.ParseInt(strings.TrimSpace(raw.BottomMM), 10, 64)
		if bottomErr != nil {
			rowErrors = append(rowErrors, RowError{Line: line, Field: "bottom_mm", Detail: "底深必须为整数毫米", Raw: raw})
			continue
		}
		rock := geology.Lithology(strings.TrimSpace(raw.Rock))
		if !geology.ValidRock(rock) {
			rowErrors = append(rowErrors, RowError{Line: line, Field: "rock", Detail: "不支持的岩性", Raw: raw})
			continue
		}
		b.layers = append(b.layers, LayerDraft{
			Layer: geology.Layer{
				TopMM:       top,
				BottomMM:    bottom,
				Rock:        rock,
				Description: strings.TrimSpace(raw.Description),
				Marker:      strings.TrimSpace(raw.Marker),
			},
			Line: line,
		})
	}

	if dataRows == 0 {
		return Import{}, &StructureError{Detail: "CSV 只有表头，没有任何数据行"}
	}

	groups := make([]GroupDraft, 0, len(order))
	for _, name := range order {
		b := builders[name]
		metadata := geology.Metadata{Name: b.name, Site: b.site, DepthMM: b.depth, Note: b.note}
		normalized, metadataErr := geology.NormalizeMetadata(metadata)
		if metadataErr != nil {
			rowErrors = append(rowErrors, RowError{
				Line:   b.line,
				Field:  metadataField(metadataErr),
				Detail: problemDetail(metadataErr),
				Raw:    rawOf(b),
			})
			continue
		}

		sort.SliceStable(b.layers, func(i, j int) bool {
			if b.layers[i].TopMM != b.layers[j].TopMM {
				return b.layers[i].TopMM < b.layers[j].TopMM
			}
			return b.layers[i].BottomMM < b.layers[j].BottomMM
		})
		layers := make([]geology.Layer, len(b.layers))
		for i, l := range b.layers {
			layers[i] = l.Layer
		}
		normalizedLayers, layerErr := geology.NormalizeLayers(layers, normalized.DepthMM)
		if layerErr != nil {
			rowErrors = append(rowErrors, mapLayerError(layerErr, b.layers)...)
		} else {
			for i := range b.layers {
				b.layers[i].Layer = normalizedLayers[i]
			}
		}

		draftLayers := make([]LayerDraft, len(b.layers))
		copy(draftLayers, b.layers)
		groups = append(groups, GroupDraft{
			Line:    b.line,
			Name:    normalized.Name,
			Site:    normalized.Site,
			DepthMM: normalized.DepthMM,
			Note:    normalized.Note,
			Layers:  draftLayers,
		})
	}

	sort.SliceStable(rowErrors, func(i, j int) bool { return rowErrors[i].Line < rowErrors[j].Line })

	return Import{
		ID:        IDFor(source),
		Status:    StatusPreview,
		SourceCSV: append([]byte{}, source...),
		Groups:    groups,
		Errors:    rowErrors,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

func checkHeader(header []string) error {
	if len(header) == 0 {
		return &StructureError{Detail: "CSV 缺少表头"}
	}
	seen := make(map[string]bool, len(header))
	var unknown []string
	for _, h := range header {
		col := strings.TrimSpace(h)
		if seen[col] {
			return &StructureError{Detail: "CSV 表头存在重复列: " + col}
		}
		seen[col] = true
	}
	for _, required := range Columns {
		if !seen[required] {
			return &StructureError{Detail: "CSV 表头缺少必需列: " + required}
		}
	}
	for col := range seen {
		if !isColumn(col) {
			unknown = append(unknown, col)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return &StructureError{Detail: "CSV 表头存在未知列: " + strings.Join(unknown, ", ")}
	}
	return nil
}

func isColumn(col string) bool {
	for _, c := range Columns {
		if c == col {
			return true
		}
	}
	return false
}

func isEmptyRecord(rec []string) bool {
	for _, v := range rec {
		if strings.TrimSpace(v) != "" {
			return false
		}
	}
	return true
}

func rawOf(b *builder) RawFields {
	return RawFields{Name: b.name, Site: b.site, DepthMM: b.depthRaw, Note: b.note}
}

func problemDetail(err error) string {
	if p, ok := err.(*geology.Problem); ok {
		return p.Detail
	}
	return err.Error()
}

func metadataField(err error) string {
	if p, ok := err.(*geology.Problem); ok && p.Field != "" {
		return p.Field
	}
	return "metadata"
}

// mapLayerError translates a layers[i] validation error back to the original
// CSV line that produced the layer.
func mapLayerError(err error, drafts []LayerDraft) []RowError {
	p, ok := err.(*geology.Problem)
	if !ok || len(drafts) == 0 {
		line := 0
		var raw RawFields
		if len(drafts) > 0 {
			line, raw = drafts[0].Line, rawOfLayer(drafts[0])
		}
		return []RowError{{Line: line, Field: "layers", Detail: err.Error(), Raw: raw}}
	}
	idx, sub := -1, ""
	if matches := layerFieldPattern.FindStringSubmatch(p.Field); matches != nil {
		idx, _ = strconv.Atoi(matches[1])
		sub = matches[2]
	}
	if idx < 0 || idx >= len(drafts) {
		return []RowError{{Line: drafts[0].Line, Field: "layers", Detail: p.Detail, Raw: rawOfLayer(drafts[0])}}
	}
	d := drafts[idx]
	field := "layers"
	if sub != "" {
		field = sub
	}
	return []RowError{{Line: d.Line, Field: field, Detail: p.Detail, Raw: rawOfLayer(d)}}
}

func rawOfLayer(d LayerDraft) RawFields {
	return RawFields{
		TopMM:       strconv.FormatInt(d.TopMM, 10),
		BottomMM:    strconv.FormatInt(d.BottomMM, 10),
		Rock:        string(d.Rock),
		Description: d.Description,
		Marker:      d.Marker,
	}
}
