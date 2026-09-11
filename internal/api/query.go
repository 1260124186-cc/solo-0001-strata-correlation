package api

import (
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"net/url"
	"strconv"
)

func query(raw string, allowed ...string) (url.Values, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, geology.Invalid("query", "查询字符串格式错误")
	}
	permitted := make(map[string]bool)
	for _, key := range allowed {
		permitted[key] = true
	}
	for key, entries := range values {
		if !permitted[key] {
			return nil, geology.Invalid(key, "不支持的查询参数")
		}
		if len(entries) != 1 {
			return nil, geology.Invalid(key, "查询参数不能重复")
		}
	}
	return values, nil
}

func integer(value, field string, fallback, minValue, maxValue int) (int, error) {
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < minValue || n > maxValue {
		return 0, geology.Invalid(field, "整数参数超出允许范围")
	}
	return n, nil
}

func integer64(value, field string, minValue, maxValue int64) (int64, error) {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < minValue || n > maxValue {
		return 0, geology.Invalid(field, "整数参数超出允许范围")
	}
	return n, nil
}

func pagination(values url.Values) (offset, limit int, err error) {
	offset, err = integer(values.Get("offset"), "offset", 0, 0, 1000000)
	if err != nil {
		return
	}
	limit, err = integer(values.Get("limit"), "limit", 20, 1, 100)
	return
}

func filter(raw string) (geology.Filter, error) {
	q, err := query(raw, "q", "site", "state", "rock", "offset", "limit")
	if err != nil {
		return geology.Filter{}, err
	}
	offset, limit, err := pagination(q)
	if err != nil {
		return geology.Filter{}, err
	}
	f := geology.Filter{Query: q.Get("q"), Site: q.Get("site"), State: geology.State(q.Get("state")), Rock: geology.Lithology(q.Get("rock")), Offset: offset, Limit: limit}
	return f, f.Validate()
}
