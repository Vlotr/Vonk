import type { CaseInfo, Check, Prompt } from "./types";

// Verhoog dit als je de werking van een check verandert.
export const CHECKS_VERSIE = "2";

function codeCheck(nr: number, score: number, uitleg: string): Check {
  return { nr, score, zekerheid: 1, bron: "code", twijfel: false, uitleg };
}

// Voor testpunt 17 en 18: geen score, alleen een waarde.
export function alleenTonen(nr: number, waarde: number, uitleg: string): Check {
  return { nr, score: null, zekerheid: 1, bron: "code", twijfel: false, uitleg, waarde };
}

// Splitst markdown in secties per "## Kop".
export function secties(tekst: string): { kop: string; inhoud: string }[] {
  const uit: { kop: string; inhoud: string }[] = [];
  for (const regel of tekst.split("\n")) {
    const m = regel.match(/^##\s+(.+?)\s*$/);
    if (m) uit.push({ kop: m[1], inhoud: "" });
    else if (uit.length > 0) uit[uit.length - 1].inhoud += regel + "\n";
  }
  return uit;
}

export function telWoorden(tekst: string): number {
  return tekst.split(/\s+/).filter((w) => /[\p{L}\p{N}]/u.test(w)).length;
}

// Getallen in Nederlandse notatie: "12.400" is 12400, "1,5" is 1.5.
export function getallen(tekst: string): { ruw: string; waarde: number }[] {
  return [...tekst.matchAll(/\d+(?:[.,]\d+)*/g)].map((m) => {
    let s = m[0];
    if (/^\d{1,3}(\.\d{3})+(,\d+)?$/.test(s)) s = s.replace(/\./g, "");
    return { ruw: m[0], waarde: Number(s.replace(",", ".")) };
  });
}

// Zinnen en opsomming-regels, zonder koppen. Voor testpunt 9.
export function zinnen(tekst: string): string[] {
  return tekst
    .split("\n")
    .filter((r) => !r.trim().startsWith("#"))
    .map((r) => r.replace(/^\s*[-*]\s+/, "").trim())
    .flatMap((r) => r.split(/(?<=[.!?])\s+/))
    .filter((z) => /\p{L}/u.test(z));
}

export function codeChecks(tekst: string, info: CaseInfo, prompt: Prompt, aan: Set<number>): Check[] {
  const checks: Check[] = [];
  const delen = secties(tekst);
  const kleineKop = (k: string) => k.trim().toLowerCase();
  const verplicht = prompt.verplichteKoppen;
  const gevondenKoppen = delen.map((d) => kleineKop(d.kop));

  if (aan.has(1)) {
    const mist = verplicht.filter((k) => !gevondenKoppen.includes(kleineKop(k)));
    const score = (verplicht.length - mist.length) / verplicht.length;
    checks.push(codeCheck(1, score, mist.length ? `Mist: ${mist.join(", ")}` : "Alle koppen gevonden"));
  }

  if (aan.has(2)) {
    const verplichtKlein = verplicht.map(kleineKop);
    const volgorde = gevondenKoppen.filter((k) => verplichtKlein.includes(k));
    const verwacht = verplichtKlein.filter((k) => volgorde.includes(k));
    const goed = volgorde.length > 0 && volgorde.join("|") === verwacht.join("|");
    checks.push(codeCheck(2, goed ? 1 : 0, `Volgorde: ${volgorde.join(", ") || "geen koppen"}`));
  }

  if (aan.has(3)) {
    const woorden = telWoorden(tekst);
    const goud = telWoorden(info.goud);
    const score = Math.max(0, 1 - Math.abs(woorden - goud) / goud);
    checks.push(codeCheck(3, score, `${woorden} woorden, goud heeft er ${goud}`));
  }

  if (aan.has(4)) {
    const t = tekst.trim();
    const fouten: string[] = [];
    if (!/^## /m.test(t)) fouten.push("geen ## kop");
    try {
      JSON.parse(t);
      fouten.push("tekst is JSON");
    } catch {
      // Geen JSON: goed.
    }
    if (t.startsWith("```") && t.endsWith("```")) fouten.push("``` om alles heen");
    checks.push(codeCheck(4, fouten.length ? 0 : 1, fouten.length ? fouten.join(", ") : "Markdown klopt"));
  }

  if (aan.has(5)) {
    const fouten: string[] = [];
    const restjes = tekst.match(/\{[^{}\n]*\}/g);
    if (restjes) fouten.push(`restje ${restjes.join(", ")}`);
    const leeg = delen.filter((d) => d.inhoud.trim() === "").map((d) => d.kop);
    if (leeg.length) fouten.push(`lege sectie ${leeg.join(", ")}`);
    checks.push(codeCheck(5, fouten.length ? 0 : 1, fouten.length ? fouten.join("; ") : "Geen restjes"));
  }

  if (aan.has(8)) {
    const toegestaan = new Set(getallen(Object.values(info.case.variabelen).join("\n")).map((g) => g.waarde));
    // Goed uitgerekende getallen mogen ook, afgerond op 0 of 1 decimaal.
    // Zonder min-teken, want een daling van 34% schrijf je als 34.
    for (const w of Object.values(info.kerncijfers)) {
      if (typeof w !== "number") continue;
      const a = Math.abs(w);
      for (const x of [a, Math.round(a), Math.round(a * 10) / 10]) toegestaan.add(x);
    }
    const gevonden = getallen(tekst);
    const fout = gevonden.filter((g) => !toegestaan.has(g.waarde)).map((g) => g.ruw);
    const score = gevonden.length ? (gevonden.length - fout.length) / gevonden.length : 1;
    const uitleg = fout.length
      ? `${fout.length} van ${gevonden.length} getallen niet in input of kerncijfers: ${fout.join(", ")}`
      : `Alle ${gevonden.length} getallen staan in de input of kerncijfers`;
    checks.push(codeCheck(8, score, uitleg));
  }

  return checks;
}
