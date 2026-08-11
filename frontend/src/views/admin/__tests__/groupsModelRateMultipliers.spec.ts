import { describe, expect, it } from "vitest";
import {
  MAX_MODEL_RATE_MULTIPLIER_ROWS,
  modelRateMultiplierRowsToConfig,
  modelRateMultipliersToRows,
  validateModelRateMultiplierRows,
} from "../groupsModelRateMultipliers";

describe("modelRateMultipliersToRows", () => {
  it("returns an empty array for null/undefined/empty config", () => {
    expect(modelRateMultipliersToRows(null)).toEqual([]);
    expect(modelRateMultipliersToRows(undefined)).toEqual([]);
    expect(modelRateMultipliersToRows({})).toEqual([]);
  });

  it("sorts rows by model name so the form order is stable across reloads", () => {
    expect(
      modelRateMultipliersToRows({
        "gpt-5.5": 0.8,
        "claude-opus-4*": 1.5,
      }),
    ).toEqual([
      { model: "claude-opus-4*", multiplier: 1.5 },
      { model: "gpt-5.5", multiplier: 0.8 },
    ]);
  });

  it("preserves a zero multiplier (model is free, not unset)", () => {
    expect(modelRateMultipliersToRows({ "free-model": 0 })).toEqual([
      { model: "free-model", multiplier: 0 },
    ]);
  });
});

describe("modelRateMultiplierRowsToConfig", () => {
  it("trims model names and drops blank rows", () => {
    expect(
      modelRateMultiplierRowsToConfig([
        { model: "  claude-opus-4*  ", multiplier: 1.5 },
        { model: "   ", multiplier: 2 },
        { model: "", multiplier: 3 },
      ]),
    ).toEqual({ "claude-opus-4*": 1.5 });
  });

  it("keeps a zero multiplier instead of filtering it as falsy", () => {
    expect(
      modelRateMultiplierRowsToConfig([{ model: "free-model", multiplier: 0 }]),
    ).toEqual({ "free-model": 0 });
  });

  it("drops non-finite and negative multipliers", () => {
    expect(
      modelRateMultiplierRowsToConfig([
        { model: "a", multiplier: Number.NaN },
        { model: "b", multiplier: Number.POSITIVE_INFINITY },
        { model: "c", multiplier: -1 },
        { model: "d", multiplier: 1.5 },
      ]),
    ).toEqual({ d: 1.5 });
  });
});

describe("validateModelRateMultiplierRows", () => {
  it("accepts exact names, trailing wildcards, bare star and zero", () => {
    expect(
      validateModelRateMultiplierRows([
        { model: "claude-opus-4.5", multiplier: 1.5 },
        { model: "gpt-5.5*", multiplier: 0.8 },
        { model: "*", multiplier: 1.2 },
        { model: "free-model", multiplier: 0 },
      ]),
    ).toEqual({ valid: true });
  });

  it("ignores blank rows so an unfilled 'add row' does not block submit", () => {
    expect(
      validateModelRateMultiplierRows([
        { model: "  ", multiplier: 1 },
        { model: "claude-opus-4.5", multiplier: 1.5 },
      ]),
    ).toEqual({ valid: true });
  });

  it("rejects wildcards that are not a single trailing star", () => {
    expect(
      validateModelRateMultiplierRows([{ model: "*-opus", multiplier: 1 }]),
    ).toMatchObject({ valid: false, reason: "invalidWildcard" });
    expect(
      validateModelRateMultiplierRows([{ model: "claude-*-4.5", multiplier: 1 }]),
    ).toMatchObject({ valid: false, reason: "invalidWildcard" });
    expect(
      validateModelRateMultiplierRows([{ model: "a*b*", multiplier: 1 }]),
    ).toMatchObject({ valid: false, reason: "invalidWildcard" });
  });

  it("rejects negative and non-finite multipliers", () => {
    expect(
      validateModelRateMultiplierRows([{ model: "a", multiplier: -1 }]),
    ).toMatchObject({ valid: false, reason: "invalidMultiplier" });
    expect(
      validateModelRateMultiplierRows([{ model: "a", multiplier: Number.NaN }]),
    ).toMatchObject({ valid: false, reason: "invalidMultiplier" });
  });

  it("rejects duplicate model names after trimming", () => {
    expect(
      validateModelRateMultiplierRows([
        { model: "claude-opus-4.5", multiplier: 1.5 },
        { model: "  claude-opus-4.5  ", multiplier: 2 },
      ]),
    ).toMatchObject({ valid: false, reason: "duplicateModel" });
  });

  it("rejects more rows than the backend entry cap", () => {
    const rows = Array.from(
      { length: MAX_MODEL_RATE_MULTIPLIER_ROWS + 1 },
      (_, i) => ({ model: `model-${i}`, multiplier: 1 }),
    );
    expect(validateModelRateMultiplierRows(rows)).toMatchObject({
      valid: false,
      reason: "tooManyRows",
    });
  });
});
