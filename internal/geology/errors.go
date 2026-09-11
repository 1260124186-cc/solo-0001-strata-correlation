package geology

import "fmt"

type Problem struct {
	Code     string    `json:"code"`
	Field    string    `json:"field,omitempty"`
	Detail   string    `json:"detail"`
	Findings []Finding `json:"findings,omitempty"`
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

// LockConflict 表示分层未通过锁定门槛，findings 给出命中的规则及问题深度区间。
func LockConflict(findings []Finding) error {
	copied := append([]Finding{}, findings...)
	return &Problem{Code: "conflict", Detail: "分层未通过锁定完整性规则，不能锁定", Findings: copied}
}

func VersionConflict(want, actual int) error {
	return Conflict(fmt.Sprintf("预期版本 %d，当前版本 %d", want, actual))
}
