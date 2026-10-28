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
  bestanden?: Record<string, string>; // Excel per variabele, wordt tekst bij het laden
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
  controles: string;
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

// Het juiste antwoord, uitgerekend door code uit de Excel. Getallen zijn exact.
export type Kerncijfers = Record<string, number | string>;

// Alles wat een check nodig heeft over één case.
export interface CaseInfo {
  case: Case;
  goud: string;
  feiten: string[];
  kerncijfers: Kerncijfers; // leeg als de case geen Excel heeft
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

// Een controle uit controles.json: een rapport met een bekende fout.
export interface ControleDef {
  id: string;
  nr: number; // het testpunt dat de fout moet vangen
  case: string;
  fout: string;
  bestand?: string;
  bestanden?: string[]; // testpunt 16: meerdere runs. "goud" is het gouden rapport.
  max?: number; // de score moet hier op of onder komen
  min?: number; // de score moet hier op of boven komen
}

export interface ControleUitslag {
  id: string;
  nr: number;
  case: string;
  fout: string;
  max?: number;
  min?: number;
  verwacht?: string; // bij 17 en 18, die geen score hebben
  goud: number | null; // score van het gouden rapport op dit testpunt
  score: number | null;
  geslaagd: boolean;
  uitleg: string;
  ookGeraakt: { nr: number; goud: number; score: number }[];
  checks: Check[];
}

export interface ControleResultaat {
  id: string;
  datum: string;
  versies: { prompt: string; testInput: string; goudenStandaard: string; checks: string; controles: string };
  modellen: { judge: string; jev: string };
  zekerheidMin: number;
  testpunten: Testpunt[];
  goud: Controle[];
  uitslagen: ControleUitslag[];
  geslaagd: number;
  twijfel: Twijfel[];
  jev: { vragen: number; onzeker: number };
  kosten: { judge: Teller; jev: Teller; totaal: number };
  duurSeconden: number;
}
