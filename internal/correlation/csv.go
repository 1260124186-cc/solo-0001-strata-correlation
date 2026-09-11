package correlation

import (
	"encoding/csv"
	"io"
	"strconv"
)

// Column names and order of both formats are frozen for algorithm
// interval-v1: downstream parses by column position, so any change to a
// published layout requires a new algorithm version.
var defaultHeader = []string{"top_mm", "bottom_mm", "thickness_mm", "left_rock", "right_rock", "relation"}

var detailedHeader = []string{
	"comparison_id", "left_id", "left_version", "right_id", "right_version", "offset_mm",
	"top_mm", "bottom_mm", "right_top_mm", "right_bottom_mm",
	"thickness_mm", "left_rock", "right_rock", "relation",
}

func WriteCSV(dst io.Writer, result Result) error {
	w := csv.NewWriter(dst)
	if err := w.Write(defaultHeader); err != nil {
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

// WriteDetailedCSV exports the review format selected with format=detailed.
// Every field comes from the immutable result and the historical versions it
// is bound to; the current profile state is never consulted. Common
// coordinates are the left original coordinates, and right original
// coordinates are common coordinates minus offset_mm, which converts both
// positive and negative offsets.
func WriteDetailedCSV(dst io.Writer, result Result) error {
	w := csv.NewWriter(dst)
	if err := w.Write(detailedHeader); err != nil {
		return err
	}
	request := result.Request
	leftVersion := strconv.Itoa(request.Left.Version)
	rightVersion := strconv.Itoa(request.Right.Version)
	offset := strconv.FormatInt(request.OffsetMM, 10)
	for _, segment := range result.Segments {
		row := []string{
			result.ID,
			request.Left.ID, leftVersion,
			request.Right.ID, rightVersion,
			offset,
			strconv.FormatInt(segment.TopMM, 10),
			strconv.FormatInt(segment.BottomMM, 10),
			strconv.FormatInt(segment.TopMM-request.OffsetMM, 10),
			strconv.FormatInt(segment.BottomMM-request.OffsetMM, 10),
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
