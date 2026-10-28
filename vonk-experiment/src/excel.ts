import ExcelJS from "exceljs";

export type Waarde = string | number;
export type Rij = Record<string, Waarde>;
// Per tabblad de rijen, met de kopregel als sleutels.
export type Werkboek = Record<string, Rij[]>;

// Datum als 2026-07-03. Excel-datums lezen we in UTC.
export function datumTekst(d: Date): string {
  return d.toISOString().slice(0, 10);
}

function celWaarde(v: ExcelJS.CellValue): Waarde {
  if (v === null || v === undefined) return "";
  if (v instanceof Date) return datumTekst(v);
  if (typeof v === "number" || typeof v === "string") return v;
  if (typeof v === "boolean") return v ? "ja" : "nee";
  if ("result" in v) return celWaarde(v.result as ExcelJS.CellValue);
  if ("richText" in v) return v.richText.map((t) => t.text).join("");
  if ("text" in v) return String(v.text);
  return String(v);
}

export async function leesWerkboek(pad: string): Promise<Werkboek> {
  const wb = new ExcelJS.Workbook();
  await wb.xlsx.readFile(pad);
  const uit: Werkboek = {};
  wb.eachSheet((blad) => {
    const kop: string[] = [];
    const rijen: Rij[] = [];
    blad.eachRow((rij, nr) => {
      const waarden = (rij.values as ExcelJS.CellValue[]).slice(1).map(celWaarde);
      if (nr === 1) kop.push(...waarden.map(String));
      else rijen.push(Object.fromEntries(kop.map((k, i) => [k, waarden[i] ?? ""])));
    });
    uit[blad.name] = rijen;
  });
  return uit;
}

// Getallen met decimale komma en zonder duizendpunt: 1234,50.
function getalTekst(x: number): string {
  return (Number.isInteger(x) ? String(x) : x.toFixed(2)).replace(".", ",");
}

// Het hele werkboek als tekst voor de prompt. Per tabblad een kop, dan regels met ";".
export function werkboekAlsTekst(wb: Werkboek): string {
  return Object.entries(wb)
    .map(([naam, rijen]) => {
      const kop = rijen.length ? Object.keys(rijen[0]) : [];
      const regels = rijen.map((r) => kop.map((k) => (typeof r[k] === "number" ? getalTekst(r[k]) : r[k])).join(";"));
      const inhoud = rijen.length ? [kop.join(";"), ...regels].join("\n") : "(geen regels)";
      return `### Tabblad: ${naam}\n${inhoud}`;
    })
    .join("\n\n");
}
