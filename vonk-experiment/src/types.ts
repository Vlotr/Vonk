export type Bron = "code" | "jev" | "llm";

// Uitslag van één testpunt voor één rapport.
// score is null bij testpunten die we alleen tonen (17 en 18).
export interface Check {
  nr: number;
  score: number | null;
  zekerheid: number;
  bron: Bron;
  twijfel: boolean;
  uitleg: string;
  waarde?: number; // 17: kosten in $, 18: seconden
  details?: unknown; // losse antwoorden van Jev of de LLM
}

export interface Prompt {
  project: string;
  id: string;
  versie: string;
  notitie: string;
  variabelen: string[];
  verplichteKoppen: string[];
  lengte: { min: number; max: number };
  userBericht: string;
  tekst: string;
}

export interface Case {
  id: string;
  variabelen: Record<string, string>;
}

export interface TestInput {
  versie: string;
  cases: Case[];
}

export interface Prijs {
  input: number;
  output: number;
}

export interface Config {
  prompt: string;
  testInput: string;
  goudenStandaard: string;
  controleSlecht: { case: string; bestand: string };
  judgeModel: string;
  jevModel: string;
  zekerheidMin: number;
  prijzen: Record<string, Prijs>;
}

export interface Testpunt {
  nr: number;
  naam: string;
  aan: boolean;
  gewicht: number;
}

// Alles wat een check nodig heeft over één case.
export interface CaseInfo {
  case: Case;
  goud: string;
  feiten: string[];
}

// Teller voor kosten en tijd van een soort call.
export interface Teller {
  calls: number;
  kosten: number;
  seconden: number;
}

// Score per testpunt-nummer. null: niet gemeten.
export type Scores = Record<number, number | null>;

export interface Output {
  case: string;
  run: number;
  tekst: string;
  tokensIn: number;
  tokensUit: number;
  kosten: number;
  seconden: number;
  checks: Check[];
}

export interface Controle {
  naam: string;
  case: string;
  checks: Check[];
  perTestpunt: Scores;
  totaal: number | null;
}

export interface Twijfel {
  case: string;
  run: string;
  nr: number;
  uitleg: string;
}

export interface Resultaat {
  id: string;
  datum: string;
  versies: { prompt: string; testInput: string; goudenStandaard: string; checks: string };
  modellen: { test: string; judge: string; jev: string };
  runs: number;
  zekerheidMin: number;
  testpunten: Testpunt[];
  totaal: number | null;
  perTestpunt: Scores;
  perCase: Record<string, Scores>;
  outputs: Output[];
  consistentie: { case: string; check: Check }[];
  controles: Controle[];
  twijfel: Twijfel[];
  jev: { vragen: number; onzeker: number };
  kosten: { test: Teller; judge: Teller; jev: Teller; totaal: number };
  duurSeconden: number;
}
