/**
 * 分组「按模型额外倍率」表单状态与 API 形态之间的转换。
 *
 * API 形态是 Record<模型名, 倍率>（后端 groups.model_rate_multipliers JSONB），
 * 表单形态是有序行数组——map 在 Vue 里没法稳定地做增删行编辑。
 *
 * 倍率语义：叠乘在分组有效倍率与高峰因子之上，不是覆盖。
 * key 支持精确模型名与末尾 * 前缀通配（精确优先，其次最长前缀），与后端
 * ModelMultiplierFor 一致；此处只做形态转换与清洗，匹配语义由后端裁决。
 */

export interface ModelRateMultiplierRow {
  model: string;
  multiplier: number;
}

/** 与后端 maxModelRateMultiplierEntries 对齐，避免提交后才被 400 打回。 */
export const MAX_MODEL_RATE_MULTIPLIER_ROWS = 200;

export function modelRateMultipliersToRows(
  config?: Record<string, number> | null,
): ModelRateMultiplierRow[] {
  return Object.entries(config || {})
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([model, multiplier]) => ({ model, multiplier }));
}

/**
 * 行数组 → API map。丢弃空模型名与非有限数值。
 *
 * 倍率 0 必须保留：它是「该模型免费」的合法配置，不能被 falsy 过滤掉。
 * 负数同样丢弃——后端会拒绝，前端先行清掉可以避免整次提交失败。
 */
export function modelRateMultiplierRowsToConfig(
  rows: ModelRateMultiplierRow[],
): Record<string, number> {
  return Object.fromEntries(
    rows
      .map((row) => [row.model.trim(), row.multiplier] as const)
      .filter(
        ([model, multiplier]) =>
          model !== "" && Number.isFinite(multiplier) && multiplier >= 0,
      ),
  );
}

/**
 * 提交前的本地校验，错误信息用 i18n key 返回，由调用方翻译。
 * 与后端 ValidateModelRateMultipliers 同规则，目的是把错误提示留在表单里。
 */
export function validateModelRateMultiplierRows(
  rows: ModelRateMultiplierRow[],
): { valid: true } | { valid: false; reason: string; model?: string } {
  if (rows.length > MAX_MODEL_RATE_MULTIPLIER_ROWS) {
    return { valid: false, reason: "tooManyRows" };
  }

  const seen = new Set<string>();
  for (const row of rows) {
    const model = row.model.trim();
    // 空行是编辑中的常态（点了「添加」还没填），提交时静默丢弃而非报错。
    if (model === "") {
      continue;
    }
    const starCount = (model.match(/\*/g) || []).length;
    if (starCount > 1 || (model.includes("*") && !model.endsWith("*"))) {
      return { valid: false, reason: "invalidWildcard", model };
    }
    if (!Number.isFinite(row.multiplier) || row.multiplier < 0) {
      return { valid: false, reason: "invalidMultiplier", model };
    }
    if (seen.has(model)) {
      return { valid: false, reason: "duplicateModel", model };
    }
    seen.add(model);
  }

  return { valid: true };
}
