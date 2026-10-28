import Anthropic from "@anthropic-ai/sdk";
import { config } from "./config";
import type { CaseInfo, Check, Teller } from "./types";

export const client = new Anthropic();
export const judgeTeller: Teller = { calls: 0, kosten: 0, seconden: 0 };

export function kostenVan(model: string, usage: Anthropic.Usage): number {
  const prijs = config.prijzen[model];
  if (!prijs) throw new Error(`Geen prijs voor ${model} in config.json`);
  return (usage.input_tokens * prijs.input + usage.output_tokens * prijs.output) / 1_000_000;
}

// Zet inhoud om naar tekst met tags, bijvoorbeeld <rapport>...</rapport>.
export function alsTekst(inhoud: string | Record<string, string>): string {
  if (typeof inhoud === "string") return inhoud;
  return Object.entries(inhoud)
    .map(([k, v]) => `<${k}>\n${v}\n</${k}>`)
    .join("\n\n");
}

// Eén judge-call die JSON teruggeeft volgens het schema.
async function vraagJudge<T>(prompt: string, properties: Record<string, unknown>): Promise<T> {
  const start = performance.now();
  const res = await client.messages.create({
    model: config.judgeModel,
    max_tokens: 16000,
    output_config: {
      format: {
        type: "json_schema",
        schema: { type: "object", properties, required: Object.keys(properties), additionalProperties: false },
      },
    },
    messages: [{ role: "user", content: prompt }],
  });
  judgeTeller.calls++;
  judgeTeller.seconden += (performance.now() - start) / 1000;
  judgeTeller.kosten += kostenVan(config.judgeModel, res.usage);
  if (res.stop_reason !== "end_turn") throw new Error(`Judge stopte met reden "${res.stop_reason}"`);
  const tekst = res.content.map((b) => (b.type === "text" ? b.text : "")).join("");
  return JSON.parse(tekst) as T;
}

const cijfer = { type: "integer", enum: [1, 2, 3, 4, 5] };

// Testpunt 12, 13 en 14 in één call. Het gouden rapport staat achteraan.
async function toonVolledigLeesbaar(tekst: string, info: CaseInfo, aan: Set<number>): Promise<Check[]> {
  const prompt = `Je beoordeelt een kwartaalrapport dat een AI schreef voor een klant van Promocat.
Vergelijk het met de gouden standaard: een perfect rapport voor dezelfde klant.

Geef drie cijfers van 1 (slecht) tot 5 (even goed als de gouden standaard, of beter):
- toon: lijken toon en stijl op de gouden standaard? Denk aan zakelijk, vriendelijk, en de klant aanspreken met "jullie".
- volledig: is het rapport even volledig en diep als de gouden standaard?
- leesbaar: is het rapport duidelijk en goed leesbaar?

Zet "zeker" op false als je twijfelt over een van de cijfers.

${alsTekst({ rapport: tekst, gouden_standaard: info.goud })}`;

  const a = await vraagJudge<{ toon: number; volledig: number; leesbaar: number; zeker: boolean }>(prompt, {
    toon: cijfer,
    volledig: cijfer,
    leesbaar: cijfer,
    zeker: { type: "boolean" },
  });
  const delen = [
    { nr: 12, cijfer: a.toon },
    { nr: 13, cijfer: a.volledig },
    { nr: 14, cijfer: a.leesbaar },
  ];
  return delen
    .filter((d) => aan.has(d.nr))
    .map((d) => ({
      nr: d.nr,
      score: (d.cijfer - 1) / 4,
      zekerheid: a.zeker ? 1 : 0,
      bron: "llm",
      twijfel: !a.zeker,
      uitleg: `Cijfer ${d.cijfer} van 5${a.zeker ? "" : ", judge is niet zeker"}`,
      details: a,
    }));
}

type Oordeel = "A beter" | "gelijk" | "B beter";

// Testpunt 15: twee calls, de tweede met A en B omgedraaid.
async function totaalVergelijk(tekst: string, info: CaseInfo): Promise<Check> {
  const vraag = (a: string, b: string) =>
    vraagJudge<{ oordeel: Oordeel }>(
      `Je vergelijkt twee kwartaalrapporten voor dezelfde klant van Promocat. Beide zijn gemaakt met dezelfde data.

Welk rapport is in totaal beter? Let op juiste feiten en getallen, volledigheid, toon en leesbaarheid.
Antwoord met "A beter", "gelijk" of "B beter".

${alsTekst({ ...info.case.variabelen, rapport_a: a, rapport_b: b })}`,
      { oordeel: { type: "string", enum: ["A beter", "gelijk", "B beter"] } },
    );
  const [eerst, omgedraaid] = await Promise.all([vraag(tekst, info.goud), vraag(info.goud, tekst)]);
  // Vertaal naar: is de output beter, gelijk of slechter dan goud?
  const output1 = { "A beter": "beter", gelijk: "gelijk", "B beter": "slechter" }[eerst.oordeel];
  const output2 = { "A beter": "slechter", gelijk: "gelijk", "B beter": "beter" }[omgedraaid.oordeel];
  const details = { eerst: eerst.oordeel, omgedraaid: omgedraaid.oordeel };
  if (output1 !== output2) {
    return {
      nr: 15,
      score: 0.5,
      zekerheid: 0,
      bron: "llm",
      twijfel: true,
      uitleg: `Oneens: eerst ${output1}, omgedraaid ${output2}`,
      details,
    };
  }
  return {
    nr: 15,
    score: output1 === "slechter" ? 0 : 1,
    zekerheid: 1,
    bron: "llm",
    twijfel: false,
    uitleg: `Beide keren: output ${output1} dan goud`,
    details,
  };
}

export async function llmChecks(tekst: string, info: CaseInfo, aan: Set<number>): Promise<Check[]> {
  const [eerste, vijftien] = await Promise.all([
    [12, 13, 14].some((n) => aan.has(n)) ? toonVolledigLeesbaar(tekst, info, aan) : Promise.resolve([]),
    aan.has(15) ? totaalVergelijk(tekst, info) : Promise.resolve(null),
  ]);
  return vijftien ? [...eerste, vijftien] : eerste;
}

// Als Jev niet zeker is, stelt de LLM dezelfde vraag opnieuw.
export async function herCheck(
  inhoud: string | Record<string, string>,
  vraag: string,
): Promise<{ score: number; zeker: boolean }> {
  const a = await vraagJudge<{ score: number; zeker: boolean }>(
    `Je controleert een kwartaalrapport voor een klant van Promocat. Hieronder staan de inhoud en een vraag.
Geef een score van 0 tot 1, zoals de vraag uitlegt. Zet "zeker" op false als je het niet zeker weet.

<inhoud>
${alsTekst(inhoud)}
</inhoud>

<vraag>
${vraag}
</vraag>`,
    { score: { type: "number" }, zeker: { type: "boolean" } },
  );
  return { score: Math.min(1, Math.max(0, a.score)), zeker: a.zeker };
}
