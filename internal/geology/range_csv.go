package geology

import (
	"encoding/csv"
	"io"
	"strconv"
)

func WriteRangeCSV(dst io.Writer, result RangeResult) error {
	w := csv.NewWriter(dst)
	header := []string{
		"profile_id", "version", "kind", "top_mm", "bottom_mm", "thickness_mm",
		"source_top_mm", "source_bottom_mm", "rock", "marker", "description",
	}
	if err := w.Write(header); err != nil {
		return err
	}
	for _, item := range result.Items {
		rock, marker, description := "", "", ""
		if item.Kind == RangeLayer {
			rock = string(item.Layer.Rock)
			marker = item.Layer.Marker
			description = item.Layer.Description
		}
		row := []string{
			result.ProfileID,
			strconv.Itoa(result.Version),
			item.Kind,
			strconv.FormatInt(item.TopMM, 10),
			strconv.FormatInt(item.BottomMM, 10),
			strconv.FormatInt(item.BottomMM-item.TopMM, 10),
			strconv.FormatInt(item.SourceTopMM, 10),
			strconv.FormatInt(item.SourceBottomMM, 10),
			rock,
			marker,
			description,
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}
