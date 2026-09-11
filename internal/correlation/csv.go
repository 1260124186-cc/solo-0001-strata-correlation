package correlation

import (
	"bytes"
	"encoding/csv"
	"io"
	"strconv"
)

var csvHeader = []string{"top_mm", "bottom_mm", "thickness_mm", "left_rock", "right_rock", "relation"}

func WriteCSV(dst io.Writer, result Result) error {
	w := csv.NewWriter(dst)
	if err := w.Write(csvHeader); err != nil {
		return err
	}
	for _, segment := range result.Segments {
		row := []string{
			strconv.FormatInt(segment.TopMM, 10),
			strconv.FormatInt(segment.BottomMM, 10),
			strconv.FormatInt(segment.BottomMM-segment.TopMM, 10),
			string(segment.LeftRock), string(segment.RightRock), segment.Relation,
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

func CSVBytes(result Result) ([]byte, error) {
	var buf bytes.Buffer
	if err := WriteCSV(&buf, result); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
