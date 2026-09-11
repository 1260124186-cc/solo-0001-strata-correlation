package geology

import "fmt"

type Problem struct {
	Code          string        `json:"code"`
	Field         string        `json:"field,omitempty"`
	Detail        string        `json:"detail"`
	Expected      int           `json:"expected_version,omitempty"`
	Current       int           `json:"current_version,omitempty"`
	ChangedFields []FieldChange `json:"changed_fields,omitempty"`
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

func VersionConflict(want, actual int) error {
	return Conflict(fmt.Sprintf("预期版本 %d，当前版本 %d", want, actual))
}

// StaleVersion 表示写入时发现剖面已经前进到更新版本。changedFields 列出
// 预期版本与当前版本之间已经变化的元数据和状态字段，供冲突方判断如何重试。
func StaleVersion(want, actual int, changedFields []FieldChange) error {
	return &Problem{
		Code:          "version_conflict",
		Detail:        fmt.Sprintf("预期版本 %d，当前版本 %d", want, actual),
		Expected:      want,
		Current:       actual,
		ChangedFields: changedFields,
	}
}
