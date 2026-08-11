package service

import (
	"fmt"
	"math"
	"strings"
)

// maxModelRateMultiplierEntries 限制单个分组可配置的模型倍率条目数。
// ModelMultiplierFor 在未命中精确名时要线性扫描整张表，且它跑在每次计费的热路径上；
// 设上限是为了给这次扫描一个确定的成本上界，不是业务限制。
const maxModelRateMultiplierEntries = 200

// ModelMultiplierFor 返回模型 model 的额外叠乘倍率。
//
// 匹配优先级：精确模型名 > 最长的末尾 * 前缀通配。两者都未命中返回 1.0（不叠乘）。
// 通配语义复用 matchWildcard（与 model_mapping / ModelRouting 一致，仅支持末尾 *，大小写敏感），
// 因此管理员在这三处填的模型名写法完全相同。
//
// 纯 "*" 是合法配置且与直接改 RateMultiplier 不等价：分组默认倍率会被
// user_group_rates 的用户级覆盖顶掉，而本表是叠乘的，对被覆盖的用户同样生效。
//
// 安全降级（与 Account.BillingRateMultiplier 的约定一致）：g 为 nil、表为空、
// 模型名为空、值为 NaN/Inf/负数，一律返回 1.0——脏数据不能把计费放大或归零。
// 值为 0 是合法配置，表示该模型免费。
func (g *Group) ModelMultiplierFor(model string) float64 {
	if g == nil || len(g.ModelRateMultipliers) == 0 {
		return 1.0
	}
	name := strings.TrimSpace(model)
	if name == "" {
		return 1.0
	}
	if v, ok := g.ModelRateMultipliers[name]; ok {
		return sanitizeModelRateMultiplier(v)
	}

	bestPattern := ""
	bestValue := 1.0
	found := false
	for pattern, v := range g.ModelRateMultipliers {
		p := strings.TrimSpace(pattern)
		if !strings.HasSuffix(p, "*") {
			// 非通配条目：上面的直查已覆盖干净 key，这里只兜底 key 带空白的脏数据
			// （所有写路径都会 Normalize，但直接改库绕得过去）。精确命中立即返回，
			// 优先级与直查一致，不参与下面的最长前缀比较。
			if p == name {
				return sanitizeModelRateMultiplier(v)
			}
			continue
		}
		if !matchWildcard(p, name) {
			continue
		}
		// 最长前缀优先；等长时按字典序取小，保证 map 迭代乱序下结果稳定。
		if !found || len(p) > len(bestPattern) || (len(p) == len(bestPattern) && p < bestPattern) {
			bestPattern, bestValue, found = p, v, true
		}
	}
	if !found {
		return 1.0
	}
	return sanitizeModelRateMultiplier(bestValue)
}

// sanitizeModelRateMultiplier 把非法值降级为 1.0（不叠乘）。
// 负数按非法处理而非按 0：0 会让计费静默归零，1.0 只是不生效，后者更安全。
func sanitizeModelRateMultiplier(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 1.0
	}
	return v
}

// ValidateModelRateMultipliers 是模型倍率配置的唯一校验来源，供 handler 与 service 共用。
// 允许空表（不叠乘）与 0 值（免费）；拒绝非法通配写法、负数与非有限数。
func ValidateModelRateMultipliers(m map[string]float64) error {
	if len(m) > maxModelRateMultiplierEntries {
		return fmt.Errorf("model_rate_multipliers 条目数不能超过 %d，当前 %d", maxModelRateMultiplierEntries, len(m))
	}
	for pattern, v := range m {
		p := strings.TrimSpace(pattern)
		if p == "" {
			return fmt.Errorf("model_rate_multipliers 的模型名不能为空")
		}
		if strings.Count(p, "*") > 1 || (strings.Contains(p, "*") && !strings.HasSuffix(p, "*")) {
			return fmt.Errorf("model_rate_multipliers 的模型名 %q 只支持末尾一个 * 通配（如 claude-opus-4*）", p)
		}
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("model_rate_multipliers[%q] 必须是有限数", p)
		}
		if v < 0 {
			return fmt.Errorf("model_rate_multipliers[%q] 不能为负", p)
		}
	}
	return nil
}

// NormalizeModelRateMultipliers 归一化落库值：去掉 key 两侧空白、丢弃空 key，
// 空表统一返回 nil（对应 JSONB NULL），避免 "{}" 与 NULL 两种等价状态同时存在。
// 与 ValidateModelRateMultipliers 的分工同 NormalizePeakRateConfig：先归一化、后校验。
func NormalizeModelRateMultipliers(m map[string]float64) map[string]float64 {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]float64, len(m))
	for pattern, v := range m {
		p := strings.TrimSpace(pattern)
		if p == "" {
			continue
		}
		out[p] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
