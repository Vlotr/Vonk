import { zinnen } from "./checks";
import { config } from "./config";
import { herCheck } from "./judge";
import type { Bron, CaseInfo, Check, Teller } from "./types";

const JEV_URL = "https://openrouter.ai/api/alpha/decisions";

export const jevTeller: Teller & { vragen: number; onzeker: number } = {
  calls: 0,
  kosten: 0,
  seconden: 0,
  vragen: 0,
  onzeker: 0,
};

// "doel" sturen we niet naar Jev. Het is de optie waarvan we de kans willen.
type JevVraag =
  | { type: "noul"; instructions: string; criteria: { true: string; false: string } }
  | { type: "choice"; instructions: string; criteria: Record<string, string>; doel: string }
  | { type: "score"; instructions: string; criteria: string[] };

interface JevAntwoord {
  noul?: number;
  choice?: string;
  score?: number;
  confidence?: number;
  probabilities?: Record<string, number>;
}

// kans: 0 tot 1. Bij noul de kans op ja, bij choice de kans op "doel", bij score de positie.
interface Antwoord {
  kans: number;
  zekerheid: number;
  bron: Bron;
  twijfel: boolean;
  jevKans: number;
  jevZekerheid: number;
}

type Inhoud = string | Record<string, string>;

async function postJev(state: Inhoud, questions: Record<string, unknown>) {
  const body = JSON.stringify({ model: config.jevModel, state, questions });
  for (let poging = 1; ; poging++) {
    const start = performance.now();
    const res = await fetch(JEV_URL, {
      method: "POST",
      headers: { Authorization: `Bearer ${process.env.OPENROUTER_API_KEY}`, "Content-Type": "application/json" },
      body,
    });
    jevTeller.seconden += (performance.now() - start) / 1000;
    if (res.ok) {
      const json = (await res.json()) as { answers: Record<string, JevAntwoord>; usage?: { cost?: number } };
      jevTeller.calls++;
      jevTeller.kosten += json.usage?.cost ?? 0;
      return json;
    }
    const fout = await res.text();
    const opnieuw = res.status === 429 || res.status >= 500;
    if (!opnieuw || poging >= 3) throw new Error(`Jev gaf fout ${res.status}: ${fout}`);
    await new Promise((r) => setTimeout(r, 2000 * poging));
  }
}

// De vraag in gewone tekst, voor de LLM als Jev niet zeker is.
function alsLlmVraag(v: JevVraag): string {
  if (v.type === "noul") return `${v.instructions}\n\nScore 1 = ${v.criteria.true}\nScore 0 = ${v.criteria.false}`;
  if (v.type === "choice") {
    const opties = Object.entries(v.criteria).map(([o, u]) => `- ${o}: ${u}`);
    return `${v.instructions}\n\nOpties:\n${opties.join("\n")}\n\nGeef als score de kans dat het antwoord "${v.doel}" is.`;
  }
  const schaal = v.criteria.map((c, i) => `- ${i / (v.criteria.length - 1)} = ${c}`);
  return `${v.instructions}\n\nSchaal:\n${schaal.join("\n")}`;
}

// Stelt alle vragen in één Jev-call. Niet zeker? Dan vraagt de LLM het opnieuw.
async function vraagJev(state: Inhoud, vragen: Record<string, JevVraag>): Promise<Record<string, Antwoord>> {
  if (Object.keys(vragen).length === 0) return {};
  const questions = Object.fromEntries(
    Object.entries(vragen).map(([k, v]) => [k, { type: v.type, instructions: v.instructions, criteria: v.criteria }]),
  );
  const json = await postJev(state, questions);
  const uit: Record<string, Antwoord> = {};
  await Promise.all(
    Object.entries(vragen).map(async ([k, v]) => {
      const a = json.answers?.[k];
      if (!a) throw new Error(`Jev gaf geen antwoord op vraag "${k}"`);
      let kans: number;
      let zekerheid: number;
      if (v.type === "noul") {
        kans = a.noul ?? 0;
        zekerheid = Math.abs(2 * kans - 1);
      } else if (v.type === "choice") {
        kans = a.probabilities?.[v.doel] ?? (a.choice === v.doel ? 1 : 0);
        zekerheid = a.confidence ?? 0;
      } else {
        kans = (a.score ?? 0) / (v.criteria.length - 1);
        zekerheid = a.confidence ?? 0;
      }
      jevTeller.vragen++;
      const basis = { jevKans: kans, jevZekerheid: zekerheid };
      if (zekerheid >= config.zekerheidMin) {
        uit[k] = { kans, zekerheid, bron: "jev", twijfel: false, ...basis };
        return;
      }
      jevTeller.onzeker++;
      const llm = await herCheck(state, alsLlmVraag(v));
      uit[k] = { kans: llm.score, zekerheid: llm.zeker ? 1 : 0, bron: "llm", twijfel: !llm.zeker, ...basis };
    }),
  );
  return uit;
}

const gemiddelde = (xs: number[]) => xs.reduce((s, x) => s + x, 0) / xs.length;

// Maakt één check van een of meer antwoorden.
function samen(nr: number, antwoorden: Antwoord[], score: number, uitleg: string, details: unknown): Check {
  const opnieuw = antwoorden.filter((a) => a.bron === "llm").length;
  const twijfel = antwoorden.filter((a) => a.twijfel).length;
  const vragen = (n: number) => (n === 1 ? "1 vraag" : `${n} vragen`);
  const extra: string[] = [];
  if (opnieuw) extra.push(`${vragen(opnieuw)} opnieuw door LLM`);
  if (twijfel) extra.push(`${vragen(twijfel)} met twijfel`);
  return {
    nr,
    score,
    zekerheid: antwoorden.length ? Math.min(...antwoorden.map((a) => a.zekerheid)) : 1,
    bron: opnieuw ? "llm" : "jev",
    twijfel: twijfel > 0,
    uitleg: [uitleg, ...extra].join(". "),
    details,
  };
}

const kort = (n: number) => n.toFixed(2).replace(".", ",");

// Testpunt 6, 7, 9, 10 en 11. Drie Jev-calls, elk met alleen de inhoud die nodig is.
export async function jevChecks(tekst: string, info: CaseInfo, aan: Set<number>): Promise<Check[]> {
  const vragenRapport: Record<string, JevVraag> = {};
  if (aan.has(6)) {
    vragenRapport.taal = {
      type: "choice",
      instructions:
        "In which language is the report written? Check every sentence. If even one sentence is in another language, do not choose nl.",
      criteria: {
        nl: "Every sentence is in Dutch.",
        en: "At least one sentence is in English.",
        de: "At least one sentence is in German.",
        anders: "Another language, or no clear language.",
      },
      doel: "nl",
    };
  }
  if (aan.has(7)) {
    info.feiten.forEach((feit, i) => {
      vragenRapport[`feit_${i + 1}`] = {
        type: "noul",
        instructions: `Does the report state this fact? The wording may differ, but the meaning and the numbers must be the same.\n\nFact: "${feit}"`,
        criteria: {
          true: "The report clearly states this fact.",
          false: "The fact is missing from the report, or the report says something different.",
        },
      };
    });
  }

  const zinLijst = aan.has(9) ? zinnen(tekst) : [];
  const vragenData: Record<string, JevVraag> = {};
  zinLijst.forEach((zin, i) => {
    vragenData[`zin_${i + 1}`] = {
      type: "noul",
      instructions: `Is this sentence from the report supported by the input data (klant, periode, data)?\n\nSentence: "${zin}"`,
      criteria: {
        true: "Everything in the sentence follows from the input data. Advice is fine if it follows logically from the data.",
        false: "The sentence has a fact, number or claim that is not in the input data, or that conflicts with it.",
      },
    };
  });
  if (aan.has(11)) {
    vragenData.namen = {
      type: "noul",
      instructions:
        "Are the customer name and all product names in the report correct, exactly as in the input data?",
      criteria: {
        true: "The customer name and all product names match the input data.",
        false: "A customer name or product name is wrong, misspelled or not in the input data.",
      },
    };
  }

  const vragenGoud: Record<string, JevVraag> = {};
  if (aan.has(10)) {
    vragenGoud.tegenspraak = {
      type: "noul",
      instructions: "Does the report contradict the gold standard report on any fact or number?",
      criteria: {
        true: "The report says something that conflicts with the gold standard.",
        false: "Nothing in the report conflicts with the gold standard. Missing details are not a conflict.",
      },
    };
  }

  const [rapport, data, goud] = await Promise.all([
    vraagJev({ rapport: tekst }, vragenRapport),
    vraagJev({ ...info.case.variabelen, rapport: tekst }, vragenData),
    vraagJev({ gouden_standaard: info.goud, rapport: tekst }, vragenGoud),
  ]);

  const checks: Check[] = [];
  if (aan.has(6)) {
    const a = rapport.taal;
    checks.push(samen(6, [a], a.kans, `Kans op Nederlands: ${kort(a.kans)}`, a));
  }
  if (aan.has(7)) {
    const lijst = info.feiten.map((feit, i) => ({ feit, ...rapport[`feit_${i + 1}`] }));
    const mist = lijst.filter((f) => f.kans < 0.5).map((f) => `"${f.feit}"`);
    const uitleg = mist.length ? `Mist: ${mist.join(", ")}` : `Alle ${lijst.length} feiten gevonden`;
    checks.push(samen(7, lijst, gemiddelde(lijst.map((f) => f.kans)), uitleg, lijst));
  }
  if (aan.has(9)) {
    const lijst = zinLijst.map((zin, i) => ({ zin, ...data[`zin_${i + 1}`] }));
    const nietGesteund = lijst.filter((z) => z.kans < 0.5).map((z) => `"${z.zin}"`);
    if (lijst.length === 0) {
      checks.push(samen(9, [], 0, "Geen zinnen gevonden", []));
    } else {
      const uitleg = nietGesteund.length
        ? `Niet gesteund: ${nietGesteund.join(", ")}`
        : `Alle ${lijst.length} zinnen gesteund door de data`;
      checks.push(samen(9, lijst, gemiddelde(lijst.map((z) => z.kans)), uitleg, lijst));
    }
  }
  if (aan.has(10)) {
    const a = goud.tegenspraak;
    checks.push(samen(10, [a], 1 - a.kans, `Kans op tegenspraak: ${kort(a.kans)}`, a));
  }
  if (aan.has(11)) {
    const a = data.namen;
    checks.push(samen(11, [a], a.kans, `Kans dat namen kloppen: ${kort(a.kans)}`, a));
  }
  return checks;
}

// Testpunt 16: lijken de runs van één case op elkaar? Elk paar runs is één vraag.
export async function consistentie(teksten: string[]): Promise<Check | null> {
  if (teksten.length < 2) return null;
  const state = Object.fromEntries(teksten.map((t, i) => [`run_${i + 1}`, t]));
  const vragen: Record<string, JevVraag> = {};
  for (let i = 1; i <= teksten.length; i++) {
    for (let j = i + 1; j <= teksten.length; j++) {
      vragen[`run_${i}_${j}`] = {
        type: "score",
        instructions: `How similar are report run_${i} and report run_${j} in content: facts, numbers, conclusions and advice? Ignore differences in wording.`,
        criteria: [
          "Completely different content",
          "Mostly different content",
          "Partly the same content",
          "Mostly the same content",
          "The same content, only the wording differs",
        ],
      };
    }
  }
  const antwoorden = await vraagJev(state, vragen);
  const lijst = Object.entries(antwoorden).map(([paar, a]) => ({ paar, ...a }));
  const uitleg = lijst.map((p) => `${p.paar.replace("run_", "run ").replace("_", " en ")}: ${kort(p.kans)}`).join(", ");
  return samen(16, lijst, gemiddelde(lijst.map((p) => p.kans)), uitleg, lijst);
}
