package geology

import "fmt"

type Problem struct {
	Code   string `json:"code"`
	Field  string `json:"field,omitempty"`
	Detail string `json:"detail"`
	// Conflict 仅在写入因版本或状态冲突被拒绝时携带，描述拒绝时刻的当前资料状态。
	Conflict *ConflictInfo `json:"conflict,omitempty"`
}

// ConflictInfo 让客户端不必再发一次请求就能基于最新状态重新构造原子更新。
// ProfileID 不出现在响应中，仅用于 API 层组装 Resource 定位。
type ConflictInfo struct {
	ExpectedVersion int    `json:"expected_version"`
	CurrentVersion  int    `json:"current_version"`
	CurrentState    State  `json:"current_state"`
	Fingerprint     string `json:"fingerprint"`
	Resource        string `json:"resource"`
	ProfileID       string `json:"-"`
}

func (p *Problem) Error() string {
	if p.Field == "" {
		return p.Detail
	}
	return fmt.Sprintf("%s: %s", p.Field, p.Detail)
}

func Invalid(field, detail string) error {
	return &Problem{Code: "invalid", Field: field, Detail: detail}
}

func Missing(detail string) error {
	return &Problem{Code: "missing", Detail: detail}
}

func Conflict(detail string) error {
	return &Problem{Code: "conflict", Detail: detail}
}

// StateConflict 拒绝一次对 p 的写入，并附上拒绝时刻的结构化当前状态。
// 服务不自动合并或重试；调用方必须在产生任何版本、事件或快照写入之前返回该错误。
func StateConflict(p Profile, expected int, detail string) error {
	return &Problem{
		Code:   "conflict",
		Detail: detail,
		Conflict: &ConflictInfo{
			ExpectedVersion: expected,
			CurrentVersion:  p.Version,
			CurrentState:    p.State,
			Fingerprint:     p.Fingerprint(),
			ProfileID:       p.ID,
		},
	}
}

func VersionConflict(p Profile, expected int) error {
	return StateConflict(p, expected, fmt.Sprintf("预期版本 %d，当前版本 %d", expected, p.Version))
}
