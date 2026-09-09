package geology

import "fmt"

type Problem struct {
	Code   string `json:"code"`
	Field  string `json:"field,omitempty"`
	Detail string `json:"detail"`
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
