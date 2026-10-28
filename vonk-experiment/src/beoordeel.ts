import { codeChecks } from "./checks";
import { testpunten } from "./config";
import { jevChecks } from "./jev";
import { llmChecks } from "./judge";
import type { CaseInfo, Check, Prompt, Scores } from "./types";

export const aan = new Set(testpunten.filter((t) => t.aan).map((t) => t.nr));
export const controlePunten = new Set([...aan].filter((nr) => nr <= 15));

// Eerst code, dan Jev, dan LLM.
export async function beoordeel(tekst: string, info: CaseInfo, prompt: Prompt, punten: Set<number>): Promise<Check[]> {
  const code = codeChecks(tekst, info, prompt, punten);
  const jev = await jevChecks(tekst, info, punten);
  const llm = await llmChecks(tekst, info, punten);
  return [...code, ...jev, ...llm].sort((a, b) => a.nr - b.nr);
}

export function gemiddelde(xs: number[]): number | null {
  return xs.length ? xs.reduce((s, x) => s + x, 0) / xs.length : null;
}

// Per testpunt het gemiddelde over alle checks met dat nummer.
export function perTestpunt(checks: Check[]): Scores {
  const uit: Scores = {};
  for (const t of testpunten.filter((t) => aan.has(t.nr))) {
    uit[t.nr] = gemiddelde(checks.filter((c) => c.nr === t.nr && c.score !== null).map((c) => c.score!));
  }
  return uit;
}

// Gewogen gemiddelde van de testpunten die aan staan.
export function totaalScore(scores: Scores): number | null {
  let som = 0;
  let gewicht = 0;
  for (const t of testpunten) {
    const s = scores[t.nr];
    if (!aan.has(t.nr) || t.gewicht <= 0 || s === null || s === undefined) continue;
    som += s * t.gewicht;
    gewicht += t.gewicht;
  }
  return gewicht ? som / gewicht : null;
}
