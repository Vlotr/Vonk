import type { Rij, Werkboek } from "./excel";
import type { CaseInfo, Kerncijfers } from "./types";

// Het juiste antwoord, uitgerekend door code. Gemiddelden en percentages zijn exact: de check rondt zelf af.
// Labels zijn gewone tekst, want ze gaan ook als tekst naar Jev en de judge.

const MAANDEN = ["januari", "februari", "maart", "april", "mei", "juni", "juli", "augustus", "september", "oktober", "november", "december"];

const centen = (x: number) => Math.round(x * 100) / 100;
const som = (xs: number[]) => centen(xs.reduce((s, x) => s + x, 0));

function kwartaalVan(datum: string): string {
  const [jaar, maand] = datum.split("-").map(Number);
  return `Q${Math.ceil(maand / 3)} ${jaar}`;
}

function vorigKwartaal(periode: string): string {
  const m = periode.match(/^Q([1-4]) (\d{4})$/);
  if (!m) throw new Error(`Periode "${periode}" is geen kwartaal zoals "Q3 2026"`);
  const [k, jaar] = [Number(m[1]), Number(m[2])];
  return k === 1 ? `Q4 ${jaar - 1}` : `Q${k - 1} ${jaar}`;
}

function maandVan(datum: string): string {
  const [jaar, maand] = datum.split("-").map(Number);
  return `${MAANDEN[maand - 1]} ${jaar}`;
}

const uniek = (rijen: Rij[]) => new Set(rijen.map((r) => r.ordernummer)).size;
const bedrag = (rijen: Rij[]) => som(rijen.map((r) => Number(r.bedrag)));
const stuks = (rijen: Rij[]) => rijen.reduce((s, r) => s + Number(r.aantal), 0);

function groepeer(rijen: Rij[], sleutel: (r: Rij) => string): Map<string, Rij[]> {
  const uit = new Map<string, Rij[]>();
  for (const r of rijen) uit.set(sleutel(r), [...(uit.get(sleutel(r)) ?? []), r]);
  return uit;
}

function hoogste(lijst: [string, number][]): string {
  return lijst.reduce((a, b) => (b[1] > a[1] ? b : a))[0];
}

export function kerncijfers(wb: Werkboek, periode: string): Kerncijfers {
  const nu = periode;
  const vorig = vorigKwartaal(periode);
  const k: Kerncijfers = {};
  const alle = wb.Orders ?? [];
  const tellen = alle.filter((r) => r.status !== "geannuleerd");
  const inKwartaal = (rijen: Rij[], kw: string) => rijen.filter((r) => kwartaalVan(String(r.orderdatum)) === kw);
  const nuRegels = inKwartaal(tellen, nu);
  const vorigRegels = inKwartaal(tellen, vorig);

  // Omzet en orders.
  const omzetNu = bedrag(nuRegels);
  const omzetVorig = bedrag(vorigRegels);
  k[`Omzet ${nu}`] = omzetNu;
  k[`Omzet ${vorig}`] = omzetVorig;
  k[`Omzet verschil ${nu} t.o.v. ${vorig}`] = centen(omzetNu - omzetVorig);
  if (omzetVorig) k[`Omzet groei % ${nu} t.o.v. ${vorig}`] = ((omzetNu - omzetVorig) / omzetVorig) * 100;
  k[`Orders ${nu}`] = uniek(nuRegels);
  k[`Orders ${vorig}`] = uniek(vorigRegels);
  k[`Orderregels ${nu}`] = nuRegels.length;
  k[`Stuks ${nu}`] = stuks(nuRegels);
  k[`Stuks ${vorig}`] = stuks(vorigRegels);
  if (uniek(nuRegels)) k[`Gemiddelde orderwaarde ${nu}`] = omzetNu / uniek(nuRegels);
  if (uniek(vorigRegels)) k[`Gemiddelde orderwaarde ${vorig}`] = omzetVorig / uniek(vorigRegels);
  k[`Geannuleerde orders ${nu} (tellen niet mee)`] = uniek(inKwartaal(alle, nu).filter((r) => r.status === "geannuleerd"));
  k["Open orders na de periode (tellen niet mee)"] = uniek(alle.filter((r) => r.status === "open"));

  // Per product.
  const producten = [...new Set(alle.map((r) => String(r.product)))];
  const verschillen: [string, number][] = [];
  for (const p of producten) {
    const pNu = nuRegels.filter((r) => r.product === p);
    const pVorig = vorigRegels.filter((r) => r.product === p);
    if (!pNu.length && !pVorig.length) continue;
    k[`Omzet ${nu}, ${p}`] = bedrag(pNu);
    k[`Stuks ${nu}, ${p}`] = stuks(pNu);
    k[`Omzet ${vorig}, ${p}`] = bedrag(pVorig);
    k[`Stuks ${vorig}, ${p}`] = stuks(pVorig);
    const verschil = centen(bedrag(pNu) - bedrag(pVorig));
    k[`Omzet verschil, ${p}`] = verschil;
    if (bedrag(pVorig)) k[`Omzet groei %, ${p}`] = (verschil / bedrag(pVorig)) * 100;
    verschillen.push([p, verschil]);
  }
  const perProductNu: [string, number][] = producten.map((p) => [p, stuks(nuRegels.filter((r) => r.product === p))]);
  k[`Best verkocht product ${nu} (stuks)`] = hoogste(perProductNu);
  k[`Product met de meeste omzet ${nu}`] = hoogste(producten.map((p) => [p, bedrag(nuRegels.filter((r) => r.product === p))]));
  const stijgers = verschillen.filter(([, v]) => v > 0);
  const dalers = verschillen.filter(([, v]) => v < 0).map(([p, v]): [string, number] => [p, -v]);
  if (stijgers.length) k["Product met de grootste stijging"] = hoogste(stijgers);
  if (dalers.length) k["Product met de grootste daling"] = hoogste(dalers);

  // Per maand.
  const perMaand = groepeer(nuRegels, (r) => maandVan(String(r.orderdatum)));
  for (const [maand, rijen] of perMaand) {
    k[`Omzet ${maand}`] = bedrag(rijen);
    k[`Orders ${maand}`] = uniek(rijen);
  }
  k[`Drukste maand ${nu} (omzet)`] = hoogste([...perMaand].map(([m, rijen]) => [m, bedrag(rijen)]));

  // Levering. Een order hoort bij het kwartaal van de orderdatum.
  const info = Object.fromEntries((wb.Info ?? []).map((r) => [String(r.veld), r.waarde]));
  const doel = Number(info["doel levertijd (werkdagen)"]);
  if (doel) k["Doel levertijd (werkdagen)"] = doel;
  const ordersNu = new Map(nuRegels.map((r) => [r.ordernummer, String(r.orderdatum)]));
  const levering = (wb.Levering ?? []).filter((r) => ordersNu.has(r.ordernummer));
  if (levering.length === 0) {
    k[`Levertijd ${nu}`] = "geen data";
  } else {
    const dagen = levering.map((r) => Number(r.werkdagen));
    const opTijd = dagen.filter((d) => d <= doel).length;
    k[`Levertijd gemiddeld ${nu} (werkdagen)`] = dagen.reduce((s, d) => s + d, 0) / dagen.length;
    k[`Orders op tijd ${nu}`] = opTijd;
    k[`Orders te laat ${nu}`] = dagen.length - opTijd;
    k[`Op tijd % ${nu}`] = (opTijd / dagen.length) * 100;
    k[`Langste levertijd ${nu} (werkdagen)`] = Math.max(...dagen);
    const leverPerMaand = groepeer(levering, (r) => maandVan(ordersNu.get(r.ordernummer)!));
    for (const [maand, rijen] of leverPerMaand) {
      const d = rijen.map((r) => Number(r.werkdagen));
      k[`Levertijd gemiddeld ${maand} (werkdagen)`] = d.reduce((s, x) => s + x, 0) / d.length;
      k[`Orders te laat ${maand}`] = d.filter((x) => x > doel).length;
    }
  }

  // Retouren van orders uit de periode. Ze tellen niet af van de omzet.
  const retouren = (wb.Retouren ?? []).filter((r) => ordersNu.has(r.ordernummer));
  k[`Retourorders ${nu}`] = uniek(retouren);
  k[`Retour stuks ${nu}`] = stuks(retouren);
  if (retouren.length) {
    k[`Retour % van de stuks ${nu}`] = (stuks(retouren) / stuks(nuRegels)) * 100;
    const redenen = groepeer(retouren, (r) => String(r.reden));
    k[`Meest voorkomende retourreden ${nu}`] = hoogste([...redenen].map(([reden, rijen]) => [reden, stuks(rijen)]));
  }
  return k;
}

// Getallen in Nederlandse notatie, op 3 decimalen: 10,846.
export function kerncijfersAlsTekst(k: Kerncijfers): string {
  return Object.entries(k)
    .map(([label, w]) => `- ${label}: ${typeof w === "number" ? String(Math.round(w * 1000) / 1000).replace(".", ",") : w}`)
    .join("\n");
}

// De input voor Jev en de judge: de variabelen, plus de kerncijfers als die er zijn.
// Het test-model krijgt de kerncijfers niet.
export function invoer(info: CaseInfo): Record<string, string> {
  if (Object.keys(info.kerncijfers).length === 0) return info.case.variabelen;
  return { ...info.case.variabelen, kerncijfers: kerncijfersAlsTekst(info.kerncijfers) };
}
