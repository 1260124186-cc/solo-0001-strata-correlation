package correlation

import (
	"encoding/csv"
	"io"
	"strconv"
)

func WriteCSV(dst io.Writer, result Result) error {
	w := csv.NewWriter(dst)
	header := []string{"top_mm", "bottom_mm", "thickness_mm", "left_rock", "right_rock", "relation", "window_top_mm", "window_bottom_mm"}
	if err := w.Write(header); err != nil {
		return err
	}
	// The window is repeated on every interval row so the exported file keeps
	// the actual window even after rows are filtered or reordered. Empty
	// values mean the comparison covered the whole profiles.
	windowTop, windowBottom := "", ""
	if result.Window != nil {
		windowTop = strconv.FormatInt(result.Window.TopMM, 10)
		windowBottom = strconv.FormatInt(result.Window.BottomMM, 10)
	}
	for _, segment := range result.Segments {
		row := []string{
			strconv.FormatInt(segment.TopMM, 10),
			strconv.FormatInt(segment.BottomMM, 10),
			strconv.FormatInt(segment.BottomMM-segment.TopMM, 10),
			string(segment.LeftRock), string(segment.RightRock), segment.Relation,
			windowTop, windowBottom,
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}
