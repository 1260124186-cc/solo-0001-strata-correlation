package correlation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"time"
)

// 算法版本标识。新版本只增不改：已保存的结果永远保留它当时使用的标识，
// 标识同时参与结果编号推导，因此不同版本的同一输入可以并存而互不覆盖。
const (
	V1      = "interval-v1"
	V2      = "interval-v2"
	Current = V2
)

type Reference struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

type Request struct {
	Left     Reference `json:"left"`
	Right    Reference `json:"right"`
	OffsetMM int64     `json:"offset_mm"`
}

// Input 是创建对比的请求体。algorithm 可省略，省略时使用 Current；
// 显式给出旧版本可用于复算历史版本结果。
type Input struct {
	Request
	Algorithm string `json:"algorithm,omitempty"`
}

type Segment struct {
	TopMM     int64             `json:"top_mm"`
	BottomMM  int64             `json:"bottom_mm"`
	LeftRock  geology.Lithology `json:"left_rock"`
	RightRock geology.Lithology `json:"right_rock"`
	Relation  string            `json:"relation"`
}

type MarkerPair struct {
	Name         string `json:"name"`
	LeftMM       int64  `json:"left_mm"`
	RightMM      int64  `json:"right_mm"`
	DifferenceMM int64  `json:"difference_mm"`
}

type Result struct {
	ID         string       `json:"id"`
	Algorithm  string       `json:"algorithm"`
	Request    Request      `json:"request"`
	Segments   []Segment    `json:"segments"`
	Markers    []MarkerPair `json:"markers"`
	OverlapMM  int64        `json:"overlap_mm"`
	KnownMM    int64        `json:"known_mm"`
	EqualMM    int64        `json:"equal_mm"`
	Similarity *float64     `json:"similarity"`
	CreatedAt  time.Time    `json:"created_at"`
}

type AlgorithmInfo struct {
	Algorithm   string `json:"algorithm"`
	Current     bool   `json:"current"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

var algorithmCatalog = []AlgorithmInfo{
	{
		Algorithm:   V1,
		Title:       "边界切分对比 v1",
		Description: "按双方分层边界切分共同区间；任一岩性未知的区间标记 unknown 且不计入相似度分母，全部共同区间未知时相似度为空。",
	},
	{
		Algorithm:   V2,
		Title:       "边界切分对比 v2",
		Description: "区间切分与 v1 完全相同；仅双方岩性都未知才标记 unknown，单侧未知视为 different 并计入相似度分母，仅当共同区间全部双侧未知时相似度为空。",
	},
}

func Algorithms() []AlgorithmInfo {
	items := make([]AlgorithmInfo, len(algorithmCatalog))
	for i, info := range algorithmCatalog {
		info.Current = info.Algorithm == Current
		items[i] = info
	}
	return items
}

func ValidateAlgorithm(algorithm string) error {
	for _, info := range algorithmCatalog {
		if info.Algorithm == algorithm {
			return nil
		}
	}
	return geology.Invalid("algorithm", "未知的对比算法版本")
}

// Resolve 返回应使用的算法版本，缺省为 Current。
func (in Input) Resolve() (Request, string, error) {
	if err := in.Request.Validate(); err != nil {
		return Request{}, "", err
	}
	algorithm := in.Algorithm
	if algorithm == "" {
		algorithm = Current
	}
	if err := ValidateAlgorithm(algorithm); err != nil {
		return Request{}, "", err
	}
	return in.Request, algorithm, nil
}

func (r Request) Validate() error {
	if !geology.ValidID(r.Left.ID, "prf_") || !geology.ValidID(r.Right.ID, "prf_") {
		return geology.Invalid("reference", "剖面编号无效")
	}
	if r.Left.Version < 1 || r.Right.Version < 1 {
		return geology.Invalid("version", "需要指定正整数历史版本")
	}
	if r.Left == r.Right {
		return geology.Invalid("reference", "不能对比同一版本自身")
	}
	if r.OffsetMM < -geology.MaxDepth || r.OffsetMM > geology.MaxDepth {
		return geology.Invalid("offset_mm", "偏移超出一千米范围")
	}
	return nil
}

// Key 推导结果编号。哈希载荷保持初始的 {Algorithm, Request} 形状，
// 因此 interval-v1 的编号与升级前逐字节一致，旧结果不会因升级而失联。
func (r Request) Key(algorithm string) string {
	b, _ := json.Marshal(struct {
		Algorithm string
		Request   Request
	}{algorithm, r})
	sum := sha256.Sum256(b)
	return "cmp_" + hex.EncodeToString(sum[:16])
}

func (r Result) Clone() Result {
	r.Segments = append([]Segment{}, r.Segments...)
	r.Markers = append([]MarkerPair{}, r.Markers...)
	if r.Similarity != nil {
		n := *r.Similarity
		r.Similarity = &n
	}
	return r
}
