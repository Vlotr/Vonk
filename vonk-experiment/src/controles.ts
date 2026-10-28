import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import type Anthropic from "@anthropic-ai/sdk";
import { aan, beoordeel, controlePunten, perTestpunt, totaalScore } from "./beoordeel";
import { CHECKS_VERSIE } from "./checks";
import { config, fout, leesJson, testpunten } from "./config";
import { consistentie, jevTeller } from "./jev";
import { judgeTeller, kostenVan, meet } from "./judge";
import { laad } from "./laden";
import { maakControleRapport } from "./rapport";
import type { CaseInfo, Check, Controle, ControleDef, ControleResultaat, ControleUitslag, Twijfel } from "./types";

// Draait de controles: rapporten met een bekende fout. Vangt elke check zijn fout?
// Geen test-model, alleen de checks.

// Toon fouten kort, zonder stack trace.
function stopMetFout(e: unknown): never {
  console.error(`Fout: ${e instanceof Error ? e.message : String(e)}`);
  process.exit(1);
}
process.on("uncaughtException", stopMetFout);

for (const sleutel of ["ANTHROPIC_API_KEY", "OPENROUTER_API_KEY"]) {
  if (!process.env[sleutel]) fout(`${sleutel} ontbreekt. Zet hem in .env (zie .env.example)`);
}

const GOUD_MIN = 0.9; // goud moet op het testpunt minstens dit scoren, anders zegt de controle niets
const OOK_GERAAKT = 0.3; // een ander testpunt dat zoveel onder goud zakt, noemen we

const { prompt, input, goudVersie, infos } = await laad().catch(stopMetFout);
const defs = leesJson<ControleDef[]>(config.controles);
const map = path.dirname(config.controles);

function log(bericht: string) {
  console.log(`[${new Date().toLocaleTimeString("nl-NL")}] ${bericht}`);
}

const infoVan = (id: string) => infos.find((i) => i.case.id === id) ?? fout(`Case ${id} bestaat niet`);
const lees = (bestand: string, info: CaseInfo) =>
  bestand === "goud" ? info.goud : readFileSync(path.join(map, bestand), "utf8");

// Maximaal max tegelijk, zodat Jev geen 429 geeft.
async function metMax<T, U>(lijst: T[], max: number, fn: (x: T) => Promise<U>): Promise<U[]> {
  const uit: U[] = new Array(lijst.length);
  let volgende = 0;
  const werker = async () => {
    while (volgende < lijst.length) {
      const i = volgende++;
      uit[i] = await fn(lijst[i]);
    }
  };
  await Promise.all(Array.from({ length: Math.min(max, lijst.length) }, werker));
  return uit;
}

// Code en Jev (1 tot 11) checken we altijd, want dat is goedkoop. De judge alleen als de controle over de judge gaat.
function puntenVoor(nr: number): Set<number> {
  const judge = nr >= 12 && nr <= 14 ? [12, 13, 14] : nr === 15 ? [15] : [];
  return new Set([...aan].filter((n) => n <= 11 || judge.includes(n)));
}

async function rapportControle(d: ControleDef, goud: Controle[]): Promise<ControleUitslag> {
  const info = infoVan(d.case);
  const punten = puntenVoor(d.nr);
  const checks = await beoordeel(lees(d.bestand ?? fout(`Controle ${d.id} mist "bestand"`), info), info, prompt, punten);
  const scores = perTestpunt(checks);
  const goudScores = goud.find((g) => g.case === d.case)!.perTestpunt;
  const score = scores[d.nr] ?? null;
  const g = goudScores[d.nr] ?? null;
  const gevangen = score !== null && score <= d.max!;
  const goudGoed = g !== null && g >= GOUD_MIN;
  const uitleg = checks.filter((c) => c.nr === d.nr).map((c) => c.uitleg);
  if (!goudGoed) uitleg.push("Goud scoort zelf te laag op dit testpunt");
  const ookGeraakt = [...punten]
    .filter((n) => n !== d.nr)
    .flatMap((n) => {
      const [gs, s] = [goudScores[n], scores[n]];
      return gs != null && s != null && gs - s > OOK_GERAAKT ? [{ nr: n, goud: gs, score: s }] : [];
    });
  return { ...basis(d), goud: g, score, geslaagd: gevangen && goudGoed, uitleg: uitleg.join(". "), ookGeraakt, checks };
}

// Testpunt 16: lijken de runs op elkaar?
async function runsControle(d: ControleDef): Promise<ControleUitslag> {
  const info = infoVan(d.case);
  const check = await consistentie(d.bestanden!.map((b) => lees(b, info)));
  const score = check?.score ?? null;
  const geslaagd = score !== null && (d.min !== undefined ? score >= d.min : score <= d.max!);
  return { ...basis(d), goud: null, score, geslaagd, uitleg: check?.uitleg ?? "Minder dan 2 runs", ookGeraakt: [], checks: check ? [check] : [] };
}

function basis(d: ControleDef) {
  return { id: d.id, nr: d.nr, case: d.case, fout: d.fout, max: d.max, min: d.min };
}

// Testpunt 17: vaste tokens geven vaste kosten. claude-sonnet-5-5 kost $2 in en $10 uit per miljoen tokens.
function kostenControle(): ControleUitslag {
  const usage = { input_tokens: 10_000, output_tokens: 2_000 } as Anthropic.Usage;
  const kosten = kostenVan("claude-sonnet-5-5", usage);
  let onbekend = "geen fout";
  try {
    kostenVan("bestaat-niet", usage);
  } catch (e) {
    onbekend = e instanceof Error ? e.message : String(e);
  }
  const geslaagd = Math.abs(kosten - 0.04) < 1e-9 && onbekend !== "geen fout";
  return {
    id: "tp17-kosten",
    nr: 17,
    case: "-",
    fout: "10.000 tokens in en 2.000 uit op claude-sonnet-5-5; en een model zonder prijs",
    verwacht: "$0,0400 en een fout",
    goud: null,
    score: null,
    geslaagd,
    uitleg: `Kosten $${kosten.toFixed(4)}. Model zonder prijs: ${onbekend}`,
    ookGeraakt: [],
    checks: [],
  };
}

// Testpunt 18: de meting die de eval gebruikt, om een bekende wachttijd heen.
async function snelheidControle(): Promise<ControleUitslag> {
  const { seconden } = await meet(() => new Promise((klaar) => setTimeout(klaar, 1500)));
  return {
    id: "tp18-snelheid",
    nr: 18,
    case: "-",
    fout: "1,5 s wachten",
    verwacht: "1,4 tot 2,0 s",
    goud: null,
    score: null,
    geslaagd: seconden >= 1.4 && seconden <= 2.0,
    uitleg: `Gemeten: ${seconden.toFixed(2)} s`,
    ookGeraakt: [],
    checks: [],
  };
}

function twijfelsVan(checks: Check[], naam: string): Twijfel[] {
  return checks.filter((c) => c.twijfel).map((c) => ({ case: naam, run: "-", nr: c.nr, uitleg: c.uitleg }));
}

async function main() {
  const start = performance.now();
  mkdirSync("results", { recursive: true });
  const id = `controles-${new Date().toLocaleString("sv-SE").replace(" ", "_").replace(/:/g, "-")}`;
  log(`${id}: ${defs.length} controles, plus 17 en 18`);

  // 1. De gouden rapporten. Die moeten hoog scoren, anders zegt een controle niets.
  log("goud: checken");
  const goud = await metMax(infos, 3, async (info): Promise<Controle> => {
    const checks = await beoordeel(info.goud, info, prompt, controlePunten);
    const scores = perTestpunt(checks);
    return { naam: `goud ${info.case.id}`, case: info.case.id, checks, perTestpunt: scores, totaal: totaalScore(scores) };
  });

  // 2. De controles.
  const uitslagen = await metMax(defs, 4, async (d) => {
    log(`${d.id}: checken`);
    return d.bestanden ? runsControle(d) : rapportControle(d, goud);
  });
  uitslagen.push(kostenControle(), await snelheidControle());

  const resultaat: ControleResultaat = {
    id,
    datum: new Date().toISOString(),
    versies: {
      prompt: `${prompt.project}/${prompt.id} v${prompt.versie}`,
      testInput: input.versie,
      goudenStandaard: goudVersie,
      checks: CHECKS_VERSIE,
      controles: path.basename(map),
    },
    modellen: { judge: config.judgeModel, jev: config.jevModel },
    zekerheidMin: config.zekerheidMin,
    testpunten,
    goud,
    uitslagen,
    geslaagd: uitslagen.filter((u) => u.geslaagd).length,
    twijfel: [...goud.flatMap((g) => twijfelsVan(g.checks, g.naam)), ...uitslagen.flatMap((u) => twijfelsVan(u.checks, u.id))],
    jev: { vragen: jevTeller.vragen, onzeker: jevTeller.onzeker },
    kosten: {
      judge: judgeTeller,
      jev: { calls: jevTeller.calls, kosten: jevTeller.kosten, seconden: jevTeller.seconden },
      totaal: judgeTeller.kosten + jevTeller.kosten,
    },
    duurSeconden: (performance.now() - start) / 1000,
  };
  writeFileSync(path.join("results", `${id}.json`), JSON.stringify(resultaat, null, 2));
  writeFileSync(path.join("results", `${id}.md`), maakControleRapport(resultaat));

  for (const u of uitslagen.filter((u) => !u.geslaagd)) log(`Niet geslaagd: ${u.id} (testpunt ${u.nr}): ${u.uitleg}`);
  log(`Klaar. ${resultaat.geslaagd} van ${uitslagen.length} controles geslaagd, kosten $${resultaat.kosten.totaal.toFixed(2)}`);
  log(`Rapport: results/${id}.md`);
  if (resultaat.geslaagd < uitslagen.length) process.exitCode = 1;
}

main().catch(stopMetFout);
