import { readFileSync } from "node:fs";
import path from "node:path";
import { config, fout, leesJson } from "./config";
import { leesWerkboek, werkboekAlsTekst } from "./excel";
import { kerncijfers } from "./kerncijfers";
import type { Case, CaseInfo, Kerncijfers, Prompt, TestInput } from "./types";

export interface Geladen {
  prompt: Prompt;
  input: TestInput;
  goudVersie: string;
  infos: CaseInfo[];
}

// Leest een case. Een Excel wordt tekst in zijn variabele, en de code rekent er de kerncijfers uit.
async function leesCase(c: Case, goud: string, feiten: string[]): Promise<CaseInfo> {
  const variabelen = { ...c.variabelen };
  let cijfers: Kerncijfers = {};
  for (const [naam, pad] of Object.entries(c.bestanden ?? {})) {
    const wb = await leesWerkboek(pad);
    variabelen[naam] = werkboekAlsTekst(wb);
    const periode = variabelen.periode ?? fout(`Case ${c.id} mist variabele "periode"`);
    cijfers = { ...cijfers, ...kerncijfers(wb, periode) };
  }
  return { case: { ...c, variabelen }, goud, feiten, kerncijfers: cijfers };
}

// Lees prompt, test-input en gouden standaard.
export async function laad(): Promise<Geladen> {
  const prompt = leesJson<Prompt>(config.prompt);
  const input = leesJson<TestInput>(config.testInput);
  const goudVersie = path.basename(config.goudenStandaard);
  const feiten = leesJson<Record<string, string[]>>(path.join(config.goudenStandaard, "feiten.json"));
  const infos = await Promise.all(
    input.cases.map((c) =>
      leesCase(
        c,
        readFileSync(path.join(config.goudenStandaard, `${c.id}.md`), "utf8"),
        feiten[c.id] ?? fout(`Geen feiten voor ${c.id} in feiten.json`),
      ),
    ),
  );
  return { prompt, input, goudVersie, infos };
}

// Vul de variabelen in. Mist er een? Dan stoppen we.
export function vulIn(prompt: Prompt, c: Case): string {
  let tekst = prompt.tekst;
  for (const naam of prompt.variabelen) {
    const waarde = c.variabelen[naam] ?? fout(`Case ${c.id} mist variabele "${naam}"`);
    tekst = tekst.replaceAll(`{${naam}}`, waarde);
  }
  const rest = tekst.match(/\{[a-z_]+\}/);
  if (rest) fout(`${rest[0]} staat in de prompt, maar niet in de lijst variabelen`);
  return tekst;
}
