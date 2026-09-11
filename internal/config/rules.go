package config

import (
	"strings"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
)

// RuleSet 把启动配置解析为锁定门槛规则集合，并交由领域层规范化校验。
// 配置非法时启动失败，避免服务以意料之外的门槛运行。
func (c Config) RuleSet() (geology.RuleSet, error) {
	rs := geology.RuleSet{MinLayerMM: c.MinLayerMM}
	for _, rule := range strings.Split(c.SealRules, ",") {
		rule = strings.TrimSpace(rule)
		if rule != "" {
			rs.Rules = append(rs.Rules, rule)
		}
	}
	enabled := false
	for _, rule := range rs.Rules {
		if rule == geology.RuleMarkerPaired {
			enabled = true
		}
	}
	// 未启用标志层规则时忽略组配置，避免默认参数与默认规则互相冲突。
	if enabled {
		for _, group := range strings.Split(c.MarkerPairGroups, ";") {
			if strings.TrimSpace(group) == "" {
				continue
			}
			var suffixes []string
			for _, suffix := range strings.Split(group, ",") {
				suffixes = append(suffixes, strings.TrimSpace(suffix))
			}
			rs.MarkerGroups = append(rs.MarkerGroups, suffixes)
		}
	}
	return geology.NormalizeRuleSet(rs)
}
