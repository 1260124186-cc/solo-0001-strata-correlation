package correlation

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"io"
	"strconv"
)

const (
	// ExportVersion identifies the exact byte layout of the external CSV.
	// Any change to the header or rows must bump this value, because
	// credentials sign the bytes produced here.
	ExportVersion = "strata-comparison-csv-v1"
	// CSVContentType is the single content type used for every CSV response
	// and download; verification binds content to this media type as well.
	CSVContentType = "text/csv; charset=utf-8"
)

// WriteCSV renders the externally distributed interval table. This function is
// the only place that turns a comparison result into export bytes; the HTTP
// handler and the credential issuer must both go through CSVBytes so a
// signature can never cover a different rendering than the one downloaded.
func WriteCSV(dst io.Writer, result Result) error {
	w := csv.NewWriter(dst)
	if err := w.Write([]string{"top_mm", "bottom_mm", "thickness_mm", "left_rock", "right_rock", "relation"}); err != nil {
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

// CSVBytes returns the exact export bytes a recipient downloads. There must be
// no other implementation of this rendering in the codebase.
func CSVBytes(result Result) ([]byte, error) {
	var buf bytes.Buffer
	if err := WriteCSV(&buf, result); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ContentDigest hashes the exact exported bytes. The returned string is
// hex(sha256(bytes)); credentials bind to the same value.
func ContentDigest(result Result) (digest string, bytes []byte, err error) {
	bytes, err = CSVBytes(result)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(bytes)
	return hex.EncodeToString(sum[:]), bytes, nil
}
