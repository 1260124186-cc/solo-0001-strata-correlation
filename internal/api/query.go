package api

import (
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
	"net/url"
	"strconv"
	"time"
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

func pagination(values url.Values) (offset, limit int, err error) {
	offset, err = integer(values.Get("offset"), "offset", 0, 0, 1000000)
	if err != nil {
		return
	}
	limit, err = integer(values.Get("limit"), "limit", 20, 1, 100)
	return
}

func timestamp(value, field string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, geology.Invalid(field, "时间必须为 RFC 3339 格式，如 2026-09-11T08:30:00Z")
	}
	return parsed, nil
}

func historyFilter(values url.Values) (geology.HistoryFilter, error) {
	offset, limit, err := pagination(values)
	if err != nil {
		return geology.HistoryFilter{}, err
	}
	f := geology.HistoryFilter{Action: values.Get("action"), Offset: offset, Limit: limit}
	if f.FromVersion, err = integer(values.Get("from_version"), "from_version", 0, 1, 500); err != nil {
		return geology.HistoryFilter{}, err
	}
	if f.ToVersion, err = integer(values.Get("to_version"), "to_version", 0, 1, 500); err != nil {
		return geology.HistoryFilter{}, err
	}
	if f.Since, err = timestamp(values.Get("since"), "since"); err != nil {
		return geology.HistoryFilter{}, err
	}
	if f.Until, err = timestamp(values.Get("until"), "until"); err != nil {
		return geology.HistoryFilter{}, err
	}
	return f, f.Validate()
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
