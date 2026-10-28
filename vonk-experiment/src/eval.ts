import { mkdirSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { parseArgs } from "node:util";
import { CHECKS_VERSIE, alleenTonen, codeChecks } from "./checks";
import { config, leesJson, testpunten } from "./config";
import { consistentie, jevChecks, jevTeller } from "./jev";
import { client, judgeTeller, kostenVan, llmChecks } from "./judge";
import { maakRapport } from "./rapport";
import type { Case, CaseInfo, Check, Controle, Output, Prompt, Resultaat, Scores, TestInput, Twijfel } from "./types";

function fout(bericht: string): never {
  throw new Error(bericht);
}

// Toon fouten kort, zonder stack trace.
function stopMetFout(e: unknown) {
  console.error(`Fout: ${e instanceof Error ? e.message : String(e)}`);
  process.exit(1);
}
process.on("uncaughtException", stopMetFout);

const { values } = parseArgs({
  options: { model: { type: "string" }, runs: { type: "string", default: "3" } },
});

const model = values.model ?? fout("Geef een model op: npm run eval -- --model <anthropic-model> --runs 3");
const runs = Number(values.runs);
if (!Number.isInteger(runs) || runs < 1) fout("--runs moet een heel getal zijn, 1 of meer");
if (model === config.judgeModel) fout(`Het test-model moet anders zijn dan de judge (${config.judgeModel})`);
if (!config.prijzen[model]) fout(`Geen prijs voor ${model} in config.json`);
for (const sleutel of ["ANTHROPIC_API_KEY", "OPENROUTER_API_KEY"]) {
  if (!process.env[sleutel]) fout(`${sleutel} ontbreekt. Zet hem in .env (zie .env.example)`);
}

// 1. Lees prompt, test-input en gouden standaard.
const prompt = leesJson<Prompt>(config.prompt);
const input = leesJson<TestInput>(config.testInput);
const goudVersie = path.basename(config.goudenStandaard);
const feiten = leesJson<Record<string, string[]>>(path.join(config.goudenStandaard, "feiten.json"));
const infos: CaseInfo[] = input.cases.map((c) => ({
  case: c,
  goud: readFileSync(path.join(config.goudenStandaard, `${c.id}.md`), "utf8"),
  feiten: feiten[c.id] ?? fout(`Geen feiten voor ${c.id} in feiten.json`),
}));

// 2. Vul de variabelen in. Mist er een? Dan stoppen we.
function vulIn(c: Case): string {
  let tekst = prompt.tekst;
  for (const naam of prompt.variabelen) {
    const waarde = c.variabelen[naam] ?? fout(`Case ${c.id} mist variabele "${naam}"`);
    tekst = tekst.replaceAll(`{${naam}}`, waarde);
  }
  const rest = tekst.match(/\{[a-z_]+\}/);
  if (rest) fout(`${rest[0]} staat in de prompt, maar niet in de lijst variabelen`);
  return tekst;
}
const systemPrompts = new Map(input.cases.map((c) => [c.id, vulIn(c)]));

const aan = new Set(testpunten.filter((t) => t.aan).map((t) => t.nr));
const controlePunten = new Set([...aan].filter((nr) => nr <= 15));
const testTeller = { calls: 0, kosten: 0, seconden: 0 };

function log(bericht: string) {
  console.log(`[${new Date().toLocaleTimeString("nl-NL")}] ${bericht}`);
}

async function schrijfRapport(systeem: string) {
  const start = performance.now();
  const res = await client.messages.create({
    model,
    max_tokens: 16000,
    system: systeem,
    messages: [{ role: "user", content: prompt.userBericht }],
  });
  const seconden = (performance.now() - start) / 1000;
  if (res.stop_reason !== "end_turn") fout(`Test-model stopte met reden "${res.stop_reason}"`);
  const kosten = kostenVan(model, res.usage);
  testTeller.calls++;
  testTeller.kosten += kosten;
  testTeller.seconden += seconden;
  return {
    tekst: res.content.map((b) => (b.type === "text" ? b.text : "")).join(""),
    tokensIn: res.usage.input_tokens,
    tokensUit: res.usage.output_tokens,
    kosten,
    seconden,
  };
}

// Eerst code, dan Jev, dan LLM.
async function beoordeel(tekst: string, info: CaseInfo, punten: Set<number>): Promise<Check[]> {
  const code = codeChecks(tekst, info, prompt, punten);
  const jev = await jevChecks(tekst, info, punten);
  const llm = await llmChecks(tekst, info, punten);
  return [...code, ...jev, ...llm].sort((a, b) => a.nr - b.nr);
}

function gemiddelde(xs: number[]): number | null {
  return xs.length ? xs.reduce((s, x) => s + x, 0) / xs.length : null;
}

// Per testpunt het gemiddelde over alle checks met dat nummer.
function perTestpunt(checks: Check[]): Scores {
  const uit: Scores = {};
  for (const t of testpunten.filter((t) => aan.has(t.nr))) {
    uit[t.nr] = gemiddelde(checks.filter((c) => c.nr === t.nr && c.score !== null).map((c) => c.score!));
  }
  return uit;
}

// Gewogen gemiddelde van de testpunten die aan staan.
function totaalScore(scores: Scores): number | null {
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

function twijfelsVan(checks: Check[], caseId: string, run: string): Twijfel[] {
  return checks.filter((c) => c.twijfel).map((c) => ({ case: caseId, run, nr: c.nr, uitleg: c.uitleg }));
}

function vorigeRun(): Resultaat | null {
  const bestanden = readdirSync("results").filter((f) => /^run-.*\.json$/.test(f)).sort();
  const laatste = bestanden.at(-1);
  return laatste ? leesJson<Resultaat>(path.join("results", laatste)) : null;
}

async function main() {
  const start = performance.now();
  mkdirSync("results", { recursive: true });
  const id = `run-${new Date().toLocaleString("sv-SE").replace(" ", "_").replace(/:/g, "-")}`;
  log(`${id}: ${model}, ${runs} runs per case`);

  // 3 en 4. Elke case --runs keer draaien en checken. Daarna testpunt 16 per case.
  const outputs: Output[] = [];
  const consistent: { case: string; check: Check }[] = [];
  await Promise.all(
    infos.map(async (info) => {
      const teksten: string[] = [];
      for (let run = 1; run <= runs; run++) {
        log(`${info.case.id} run ${run}: schrijven`);
        const r = await schrijfRapport(systemPrompts.get(info.case.id)!);
        log(`${info.case.id} run ${run}: checken`);
        const checks = await beoordeel(r.tekst, info, aan);
        if (aan.has(17)) {
          checks.push(alleenTonen(17, r.kosten, `${r.tokensIn} in, ${r.tokensUit} uit, $${r.kosten.toFixed(4)}`));
        }
        if (aan.has(18)) checks.push(alleenTonen(18, r.seconden, `${r.seconden.toFixed(1)} s`));
        outputs.push({ case: info.case.id, run, ...r, checks });
        teksten.push(r.tekst);
      }
      if (aan.has(16)) {
        const check = await consistentie(teksten);
        if (check) consistent.push({ case: info.case.id, check });
      }
    }),
  );
  outputs.sort((a, b) => a.case.localeCompare(b.case) || a.run - b.run);
  consistent.sort((a, b) => a.case.localeCompare(b.case));

  // 5. Controles: de gouden rapporten zelf, en het slechte rapport tegen zijn case.
  log("controles: checken");
  const slechtInfo = infos.find((i) => i.case.id === config.controleSlecht.case) ?? fout("Case van slecht rapport bestaat niet");
  const teControleren = [
    ...infos.map((info) => ({ naam: `goud ${info.case.id}`, info, tekst: info.goud })),
    { naam: `slecht ${slechtInfo.case.id}`, info: slechtInfo, tekst: readFileSync(config.controleSlecht.bestand, "utf8") },
  ];
  const controles: Controle[] = await Promise.all(
    teControleren.map(async (c) => {
      const checks = await beoordeel(c.tekst, c.info, controlePunten);
      const scores = perTestpunt(checks);
      return { naam: c.naam, case: c.info.case.id, checks, perTestpunt: scores, totaal: totaalScore(scores) };
    }),
  );

  // Scores.
  const alleChecks = [...outputs.flatMap((o) => o.checks), ...consistent.map((c) => c.check)];
  const scores = perTestpunt(alleChecks);
  const perCase: Record<string, Scores> = {};
  for (const info of infos) {
    const caseId = info.case.id;
    perCase[caseId] = perTestpunt([
      ...outputs.filter((o) => o.case === caseId).flatMap((o) => o.checks),
      ...consistent.filter((c) => c.case === caseId).map((c) => c.check),
    ]);
  }
  const twijfel = [
    ...outputs.flatMap((o) => twijfelsVan(o.checks, o.case, String(o.run))),
    ...consistent.flatMap((c) => twijfelsVan([c.check], c.case, "alle")),
    ...controles.flatMap((c) => twijfelsVan(c.checks, c.naam, "-")),
  ];

  // 6. Opslaan.
  const vorige = vorigeRun();
  const resultaat: Resultaat = {
    id,
    datum: new Date().toISOString(),
    versies: {
      prompt: `${prompt.project}/${prompt.id} v${prompt.versie}`,
      testInput: input.versie,
      goudenStandaard: goudVersie,
      checks: CHECKS_VERSIE,
    },
    modellen: { test: model, judge: config.judgeModel, jev: config.jevModel },
    runs,
    zekerheidMin: config.zekerheidMin,
    testpunten,
    totaal: totaalScore(scores),
    perTestpunt: scores,
    perCase,
    outputs,
    consistentie: consistent,
    controles,
    twijfel,
    jev: { vragen: jevTeller.vragen, onzeker: jevTeller.onzeker },
    kosten: {
      test: testTeller,
      judge: judgeTeller,
      jev: { calls: jevTeller.calls, kosten: jevTeller.kosten, seconden: jevTeller.seconden },
      totaal: testTeller.kosten + judgeTeller.kosten + jevTeller.kosten,
    },
    duurSeconden: (performance.now() - start) / 1000,
  };
  writeFileSync(path.join("results", `${id}.json`), JSON.stringify(resultaat, null, 2));
  writeFileSync(path.join("results", `${id}.md`), maakRapport(resultaat, vorige));

  const totaal = resultaat.totaal === null ? "-" : resultaat.totaal.toFixed(2);
  log(`Klaar. Totaalscore ${totaal}, kosten $${resultaat.kosten.totaal.toFixed(2)}`);
  log(`Rapport: results/${id}.md`);
}

main().catch(stopMetFout);
