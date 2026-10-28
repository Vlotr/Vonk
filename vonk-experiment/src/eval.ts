import { mkdirSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { parseArgs } from "node:util";
import { aan, beoordeel, controlePunten, perTestpunt, totaalScore } from "./beoordeel";
import { CHECKS_VERSIE, alleenTonen } from "./checks";
import { config, fout, leesJson, testpunten } from "./config";
import { consistentie, jevTeller } from "./jev";
import { client, judgeTeller, kostenVan, meet } from "./judge";
import { laad, vulIn } from "./laden";
import { maakRapport } from "./rapport";
import type { Check, Controle, Output, Resultaat, Scores, Twijfel } from "./types";

// Toon fouten kort, zonder stack trace.
function stopMetFout(e: unknown): never {
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

// 1 en 2. Lees prompt, test-input (Excel wordt tekst) en gouden standaard. Vul de variabelen in.
const { prompt, input, goudVersie, infos } = await laad().catch(stopMetFout);
const systemPrompts = new Map(infos.map((i) => [i.case.id, vulIn(prompt, i.case)]));

const testTeller = { calls: 0, kosten: 0, seconden: 0 };

function log(bericht: string) {
  console.log(`[${new Date().toLocaleTimeString("nl-NL")}] ${bericht}`);
}

async function schrijfRapport(systeem: string) {
  const { uitkomst: res, seconden } = await meet(() =>
    client.messages.create({
      model,
      max_tokens: 16000,
      system: systeem,
      messages: [{ role: "user", content: prompt.userBericht }],
    }),
  );
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

function twijfelsVan(checks: Check[], caseId: string, run: string): Twijfel[] {
  return checks.filter((c) => c.twijfel).map((c) => ({ case: caseId, run, nr: c.nr, uitleg: c.uitleg }));
}

// De laatste run met dezelfde test-input. Andere input is niet te vergelijken.
function vorigeRun(): Resultaat | null {
  const runs = readdirSync("results")
    .filter((f) => /^run-.*\.json$/.test(f))
    .sort()
    .map((f) => leesJson<Resultaat>(path.join("results", f)))
    .filter((r) => r.versies.testInput === input.versie);
  return runs.at(-1) ?? null;
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
        const checks = await beoordeel(r.tekst, info, prompt, aan);
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
      const checks = await beoordeel(c.tekst, c.info, prompt, controlePunten);
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
