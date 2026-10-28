import type { Resultaat } from "./types";

// Getallen in Nederlandse notatie: 0,87.
function getal(x: number | null | undefined, decimalen = 2): string {
  return x === null || x === undefined ? "–" : x.toFixed(decimalen).replace(".", ",");
}

const dollar = (x: number) => `$${getal(x, 4)}`;
const cel = (s: string) => s.replace(/\|/g, "\\|").replace(/\n/g, " ");

function gemiddelde(xs: number[]): number | null {
  return xs.length ? xs.reduce((s, x) => s + x, 0) / xs.length : null;
}

function verschil(nu: number | null | undefined, oud: number | null | undefined): string {
  if (nu === null || nu === undefined || oud === null || oud === undefined) return "–";
  const d = Math.round((nu - oud) * 100) / 100; // zo wordt -0,004 geen "-0,00"
  return `${d >= 0 ? "+" : ""}${getal(d)}`;
}

function duur(seconden: number): string {
  return `${Math.floor(seconden / 60)} min ${Math.round(seconden % 60)} s`;
}

function tabel(kop: string[], rijen: string[][]): string {
  return [
    `| ${kop.join(" | ")} |`,
    `| ${kop.map(() => "---").join(" | ")} |`,
    ...rijen.map((r) => `| ${r.join(" | ")} |`),
  ].join("\n");
}

export function maakRapport(r: Resultaat, vorige: Resultaat | null): string {
  const punten = r.testpunten.filter((t) => t.aan);
  const cases = Object.keys(r.perCase);
  const naam = (nr: number) => r.testpunten.find((t) => t.nr === nr)?.naam ?? `Testpunt ${nr}`;
  const delen: string[] = [];

  delen.push(`# Eval-rapport ${r.id}`);
  delen.push(
    [
      `- Prompt: ${r.versies.prompt}`,
      `- Test-input: ${r.versies.testInput}`,
      `- Gouden standaard: ${r.versies.goudenStandaard}`,
      `- Checks: versie ${r.versies.checks}`,
      `- Test-model: ${r.modellen.test}, ${r.runs} runs per case`,
      `- Judge: ${r.modellen.judge}`,
      `- Jev: ${r.modellen.jev}, drempel zekerheid ${getal(r.zekerheidMin, 1)}`,
    ].join("\n"),
  );

  // 1. Totaalscore
  delen.push(
    `## Totaalscore\n\n**${getal(r.totaal)}**\n\nGewogen gemiddelde van alle testpunten die aan staan, over alle cases en runs.`,
  );

  // 2. Score per testpunt en per case. Bij 17 en 18 een waarde, geen score.
  const waarde = (nr: number, caseId?: string) =>
    gemiddelde(
      r.outputs
        .filter((o) => !caseId || o.case === caseId)
        .flatMap((o) => o.checks)
        .filter((c) => c.nr === nr && c.waarde !== undefined)
        .map((c) => c.waarde!),
    );
  const toonWaarde = (nr: number, w: number | null) =>
    w === null ? "–" : nr === 17 ? dollar(w) : `${getal(w, 1)} s`;
  const rijen = punten.map((t) =>
    t.nr === 17 || t.nr === 18
      ? [String(t.nr), t.naam, String(t.gewicht), ...cases.map((c) => toonWaarde(t.nr, waarde(t.nr, c))), toonWaarde(t.nr, waarde(t.nr))]
      : [String(t.nr), t.naam, String(t.gewicht), ...cases.map((c) => getal(r.perCase[c][t.nr])), getal(r.perTestpunt[t.nr])],
  );
  delen.push(
    `## Score per testpunt\n\nScore van 0 tot 1. Per case is het het gemiddelde over de runs. Bij 17 en 18 staat het gemiddelde per call. Die tellen niet mee.\n\n${tabel(["#", "Testpunt", "Gewicht", ...cases, "Gemiddeld"], rijen)}`,
  );

  // 3. Controles
  const controlePunten = punten.filter((t) => t.nr <= 15);
  const controleRijen = controlePunten.map((t) => [
    String(t.nr),
    t.naam,
    ...r.controles.map((c) => getal(c.perTestpunt[t.nr])),
  ]);
  controleRijen.push(["", "**Totaal**", ...r.controles.map((c) => `**${getal(c.totaal)}**`)]);
  const lageScores = r.controles.flatMap((c) =>
    c.checks
      .filter((ch) => ch.score !== null && ch.score < 0.9)
      .map((ch) => `- ${c.naam}, ${ch.nr}. ${naam(ch.nr)} (${getal(ch.score)}): ${ch.uitleg}`),
  );
  delen.push(
    `## Controles\n\nDe gouden rapporten moeten hoog scoren. Het slechte rapport moet laag scoren.\n\n${tabel(["#", "Testpunt", ...r.controles.map((c) => c.naam)], controleRijen)}` +
      (lageScores.length ? `\n\nScores onder 0,9:\n\n${lageScores.join("\n")}` : ""),
  );

  // 4. Vergelijking met de vorige run
  if (vorige) {
    const vergelijkRijen = punten
      .filter((t) => t.nr !== 17 && t.nr !== 18)
      .map((t) => [
        String(t.nr),
        t.naam,
        getal(vorige.perTestpunt[t.nr]),
        getal(r.perTestpunt[t.nr]),
        verschil(r.perTestpunt[t.nr], vorige.perTestpunt[t.nr]),
      ]);
    vergelijkRijen.push(["", "**Totaal**", getal(vorige.totaal), getal(r.totaal), `**${verschil(r.totaal, vorige.totaal)}**`]);
    delen.push(
      `## Vergelijking met vorige run\n\nVorige run: ${vorige.id}, test-model ${vorige.modellen.test}.\n\n${tabel(["#", "Testpunt", "Vorige", "Nu", "Verschil"], vergelijkRijen)}\n\n- Kosten: ${dollar(vorige.kosten.totaal)} toen, ${dollar(r.kosten.totaal)} nu.\n- Duur: ${duur(vorige.duurSeconden)} toen, ${duur(r.duurSeconden)} nu.`,
    );
  }

  // 5. Twijfelgevallen
  const jevZin = `Jev was niet zeker bij ${r.jev.onzeker} van de ${r.jev.vragen} vragen (zekerheid onder ${getal(r.zekerheidMin, 1)}). Die vragen deed de LLM opnieuw.`;
  const twijfelTabel = r.twijfel.length
    ? tabel(
        ["Case", "Run", "Testpunt", "Waarom"],
        r.twijfel.map((t) => [t.case, t.run, `${t.nr}. ${naam(t.nr)}`, cel(t.uitleg)]),
      )
    : "Geen twijfelgevallen.";
  delen.push(`## Twijfelgevallen\n\n${jevZin}\n\n${twijfelTabel}`);

  // 6. Kosten en snelheid
  const k = r.kosten;
  const kostenRijen = [
    ["Test-model", String(k.test.calls), dollar(k.test.kosten), `${getal(k.test.seconden, 1)} s`],
    ["Judge (LLM)", String(k.judge.calls), dollar(k.judge.kosten), `${getal(k.judge.seconden, 1)} s`],
    ["Jev", String(k.jev.calls), dollar(k.jev.kosten), `${getal(k.jev.seconden, 1)} s`],
    ["**Totaal**", "", `**${dollar(k.totaal)}**`, ""],
  ];
  const seconden = r.outputs.map((o) => o.seconden);
  const tokensIn = gemiddelde(r.outputs.map((o) => o.tokensIn)) ?? 0;
  const tokensUit = gemiddelde(r.outputs.map((o) => o.tokensUit)) ?? 0;
  delen.push(
    `## Kosten en snelheid\n\n${tabel(["Onderdeel", "Calls", "Kosten", "Tijd (opgeteld)"], kostenRijen)}\n\n` +
      [
        `- Test-model: gemiddeld ${getal(gemiddelde(seconden), 1)} s per call (snelst ${getal(Math.min(...seconden), 1)} s, traagst ${getal(Math.max(...seconden), 1)} s).`,
        `- Test-model: gemiddeld ${Math.round(tokensIn)} tokens in en ${Math.round(tokensUit)} tokens uit per call.`,
        `- De hele run duurde ${duur(r.duurSeconden)}.`,
      ].join("\n"),
  );

  return delen.join("\n\n") + "\n";
}
