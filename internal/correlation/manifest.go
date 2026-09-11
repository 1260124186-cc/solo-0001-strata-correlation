package correlation

import (
	"crypto/sha256"
	"encoding/hex"
)

// Manifest 记录一次 CSV 导出的来源结果、算法版本与内容摘要，
// 供复核者在导出后核对文件确实来自该结果且未被改动。
type Manifest struct {
	ResultID  string   `json:"result_id"`
	Algorithm string   `json:"algorithm"`
	Columns   []string `json:"columns"`
	Rows      int      `json:"rows"`
	SHA256    string   `json:"csv_sha256"`
}

// CSVDigest 计算导出内容的 SHA-256 摘要。摘要针对实际导出的字节，
// 覆盖每一列的内容、列顺序与空白，任何改动都会得到不同摘要。
func CSVDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// NewManifest 从已保存的不可变对比结果推导清单。它不读取剖面当前状态，
// 因此剖面重新打开或服务重启之后，同一结果的清单保持不变。
func NewManifest(result Result) (Manifest, error) {
	content, err := CSVBytes(result)
	if err != nil {
		return Manifest{}, err
	}
	return Manifest{
		ResultID:  result.ID,
		Algorithm: result.Algorithm,
		Columns:   append([]string{}, csvHeader...),
		Rows:      len(result.Segments),
		SHA256:    CSVDigest(content),
	}, nil
}

// VerifyCSV 重新计算导出内容的摘要，并与保存结果应有的摘要比对。
func VerifyCSV(result Result, content []byte) (Manifest, bool, error) {
	manifest, err := NewManifest(result)
	if err != nil {
		return Manifest{}, false, err
	}
	return manifest, CSVDigest(content) == manifest.SHA256, nil
}
