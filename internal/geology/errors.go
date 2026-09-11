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

// ReviewRejected carries a failed review and is returned when a sealing
// request requires a passing review first.
type ReviewRejected struct {
	Review ReviewResult
}

func (e *ReviewRejected) Error() string {
	return fmt.Sprintf("质量审查未通过：发现 %d 个问题", e.Review.Counts.Total)
}

// ReviewGate reports that a sealing request requires a passing review that
// has not been stored for the target revision.
func ReviewGate(detail string) error {
	return &Problem{Code: "review_required", Detail: detail}
}
