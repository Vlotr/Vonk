import { createHash } from "node:crypto";
import { mkdirSync } from "node:fs";
import path from "node:path";
import ExcelJS from "exceljs";
import { datumTekst, leesWerkboek, werkboekAlsTekst } from "./excel";
import { kerncijfers, kerncijfersAlsTekst } from "./kerncijfers";

// Maakt test-input 2.0: per klant een Excel met orderregels over Q2 en Q3 2026.
// Alle data is verzonnen. Vaste seed: opnieuw draaien geeft dezelfde data.

const MAP = "data/test-input/2.0";

type Rnd = () => number;

function mulberry32(seed: number): Rnd {
  return () => {
    seed = (seed + 0x6d2b79f5) | 0;
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

// Heel getal van min tot en met max, in stappen van stap.
const tussen = (rnd: Rnd, min: number, max: number, stap = 1) =>
  min + stap * Math.floor(rnd() * (Math.floor((max - min) / stap) + 1));

const dag = (maand: number, d: number) => new Date(Date.UTC(2026, maand - 1, d));
const isWerkdag = (d: Date) => d.getUTCDay() !== 0 && d.getUTCDay() !== 6;

function werkdagenNa(d: Date, n: number): Date {
  const uit = new Date(d);
  while (n > 0) {
    uit.setUTCDate(uit.getUTCDate() + 1);
    if (isWerkdag(uit)) n--;
  }
  return uit;
}

// Een willekeurige werkdag van van tot en met tot.
function werkdag(rnd: Rnd, van: Date, tot: Date): Date {
  const dagen: Date[] = [];
  for (const d = new Date(van); d <= tot; d.setUTCDate(d.getUTCDate() + 1)) {
    if (isWerkdag(d)) dagen.push(new Date(d));
  }
  return dagen[Math.floor(rnd() * dagen.length)];
}

interface Product {
  naam: string;
  prijs: number;
  varianten: string[];
}

// Hoe vaak een product in een order zit, in hoeveel varianten, en hoeveel stuks per variant.
interface Keuze {
  product: Product;
  kans: number;
  varianten: [number, number];
  aantal: [number, number, number];
}

interface Regel {
  product: Product;
  variant: string;
  aantal: number;
}

type Status = "geleverd" | "geannuleerd" | "open";

interface Order {
  nummer: string;
  datum: Date;
  status: Status;
  regels: Regel[];
  werkdagen?: number;
}

interface Retour {
  order: Order;
  regel: Regel;
  reden: string;
}

function regels(rnd: Rnd, keuzes: Keuze[]): Regel[] {
  let gekozen = keuzes.filter((k) => rnd() < k.kans);
  if (gekozen.length === 0) gekozen = [keuzes[0]];
  return gekozen.flatMap((k) => {
    const n = Math.min(tussen(rnd, k.varianten[0], k.varianten[1]), k.product.varianten.length);
    const start = Math.floor(rnd() * k.product.varianten.length);
    return Array.from({ length: n }, (_, i) => ({
      product: k.product,
      variant: k.product.varianten[(start + i) % k.product.varianten.length],
      aantal: tussen(rnd, ...k.aantal),
    }));
  });
}

function orders(rnd: Rnd, aantal: number, van: Date, tot: Date, keuzes: Keuze[], status: Status = "geleverd"): Order[] {
  return Array.from({ length: aantal }, () => ({
    nummer: "",
    datum: werkdag(rnd, van, tot),
    status,
    regels: regels(rnd, keuzes),
  }));
}

// Sorteert op datum en geeft ordernummers.
function nummer(lijst: Order[], prefix: string): Order[] {
  lijst.sort((a, b) => a.datum.getTime() - b.datum.getTime());
  lijst.forEach((o, i) => (o.nummer = `${prefix}-${1001 + i}`));
  return lijst;
}

// Dezelfde vaste bestelling een kwartaal later, met kleine verschillen in aantal.
function kwartaalLater(rnd: Rnd, o: Order, stap: (r: Regel) => number): Order {
  const maand = o.datum.getUTCMonth() + 4;
  return {
    nummer: "",
    datum: werkdag(rnd, dag(maand, 1), dag(maand, 28)),
    status: "geleverd",
    regels: o.regels.map((r) => ({ ...r, aantal: Math.max(stap(r), r.aantal + stap(r) * tussen(rnd, -1, 1)) })),
  };
}

const stuks = (o: Order) => o.regels.reduce((s, r) => s + r.aantal, 0);
const centen = (x: number) => Math.round(x * 100) / 100;

interface Klant {
  id: string;
  klant: string;
  accountmanager: string;
  info?: [string, string][];
  orders: Order[];
  retouren: Retour[];
  notities: [Date, string][];
  // Zonder levering: tabblad Levering blijft leeg.
  levering: boolean;
}

// case-1: groei, vooral door de katoenen tassen. Alle retouren: verkeerde kleur.
function bakkerij(): Klant {
  const r = (n: number) => mulberry32(1010 + n);
  const tas = { naam: "Katoenen tas met logo", prijs: 2.4, varianten: ["naturel", "zwart", "rood"] };
  const schort = { naam: "Schort met logo", prijs: 14.5, varianten: ["zwart", "bordeaux"] };
  const beker = { naam: "Koffiebeker met logo", prijs: 3.2, varianten: ["wit"] };
  const theedoek = { naam: "Theedoek met logo", prijs: 4.1, varianten: ["wit", "beige"] };
  const rest: Keuze[] = [
    { product: schort, kans: 0.4, varianten: [1, 2], aantal: [5, 20, 5] },
    { product: beker, kans: 0.5, varianten: [1, 1], aantal: [48, 144, 24] },
    { product: theedoek, kans: 0.4, varianten: [1, 2], aantal: [20, 60, 10] },
  ];
  const keuzes: Keuze[] = [{ product: tas, kans: 0.5, varianten: [1, 2], aantal: [100, 225, 25] }, ...rest];
  const stap = new Map<Product, number>([[tas, 25], [schort, 5], [beker, 24], [theedoek, 10]]);
  const q2 = orders(r(1), 34, dag(4, 1), dag(6, 30), keuzes);
  const rq3 = r(2);
  const q3 = q2.map((o) => {
    const later = kwartaalLater(rq3, o, (regel) => stap.get(regel.product)!);
    // De groei zit in de tassen: grotere aantallen, en vaker een tas erbij.
    for (const regel of later.regels) if (regel.product === tas) regel.aantal += 50;
    if (!later.regels.some((regel) => regel.product === tas) && rq3() < 0.4) {
      later.regels.unshift({ product: tas, variant: "naturel", aantal: tussen(rq3, 100, 200, 25) });
    }
    return later;
  });
  const lijst = nummer(
    [
      ...q2,
      ...q3,
      ...orders(r(3), 1, dag(5, 1), dag(5, 31), keuzes, "geannuleerd"),
      { nummer: "", datum: dag(8, 11), status: "geannuleerd", regels: tas.varianten.map((v) => ({ product: tas, variant: v, aantal: 300 })) },
      ...orders(r(4), 1, dag(9, 1), dag(9, 30), rest, "geannuleerd"),
      ...orders(r(5), 3, dag(10, 1), dag(10, 23), keuzes, "open"),
    ],
    "GK",
  );
  const rl = r(6);
  for (const o of lijst) o.werkdagen = rl() < 0.1 ? tussen(rl, 11, 12) : tussen(rl, 6, 10);
  const metTas = lijst.filter((o) => o.status === "geleverd" && o.datum >= dag(7, 1) && o.regels.some((r) => r.product === tas));
  const retouren = [metTas[4], metTas[15]].map((o) => ({
    order: o,
    regel: o.regels.find((r) => r.product === tas)!,
    reden: "verkeerde kleur",
  }));
  return {
    id: "case-1",
    klant: "Bakkerij De Gouden Korst",
    accountmanager: "Fatima Bouali",
    orders: lijst,
    retouren,
    notities: [
      [dag(4, 14), "Nieuwe contactpersoon voor facturen: Sanne de Wit."],
      [dag(8, 19), "De klant vraagt naar duurzame opties, zoals tassen van gerecycled katoen."],
      [dag(9, 24), "Bezoek aan de showroom gepland voor volgend voorjaar."],
    ],
    levering: true,
  };
}

// case-2: daling, vooral doordat de wedstrijdtenues (Q2) niet terugkomen.
// Grote orders vanaf half augustus zijn te laat: start van het seizoen.
function hockeyclub(): Klant {
  const r = (n: number) => mulberry32(2020 + n);
  const maten = ["S", "M", "L", "XL"];
  const shirt = { naam: "Trainingsshirt met logo", prijs: 12.5, varianten: maten };
  const tenue = { naam: "Wedstrijdtenue met logo", prijs: 39, varianten: maten };
  const sokken = { naam: "Hockeysokken met logo", prijs: 6.5, varianten: ["35-38", "39-42", "43-46"] };
  const tas = { naam: "Sporttas met logo", prijs: 18, varianten: ["zwart"] };
  const cap = { naam: "Cap met logo", prijs: 7.5, varianten: ["one size"] };
  const gewoon: Keuze[] = [
    { product: shirt, kans: 0.5, varianten: [1, 3], aantal: [10, 25, 5] },
    { product: sokken, kans: 0.5, varianten: [1, 3], aantal: [20, 40, 10] },
    { product: tas, kans: 0.3, varianten: [1, 1], aantal: [10, 25, 5] },
    { product: cap, kans: 0.4, varianten: [1, 1], aantal: [10, 30, 10] },
  ];
  const tenues: Keuze[] = [
    { product: tenue, kans: 1, varianten: [4, 4], aantal: [10, 20, 2] },
    { product: sokken, kans: 1, varianten: [3, 3], aantal: [20, 40, 10] },
  ];
  const seizoen: Keuze[] = [
    { product: shirt, kans: 1, varianten: [4, 4], aantal: [10, 20, 5] },
    { product: sokken, kans: 1, varianten: [3, 3], aantal: [20, 40, 10] },
    { product: tas, kans: 0.3, varianten: [1, 1], aantal: [10, 20, 5] },
  ];
  const lijst = nummer(
    [
      ...orders(r(1), 16, dag(4, 1), dag(6, 30), gewoon),
      ...orders(r(2), 2, dag(5, 4), dag(5, 22), tenues),
      ...orders(r(3), 9, dag(7, 1), dag(8, 14), gewoon),
      ...orders(r(4), 4, dag(8, 17), dag(9, 18), seizoen),
      ...orders(r(5), 1, dag(4, 1), dag(6, 30), gewoon, "geannuleerd"),
      ...orders(r(6), 1, dag(7, 1), dag(9, 30), gewoon, "geannuleerd"),
      ...orders(r(7), 2, dag(10, 1), dag(10, 23), gewoon, "open"),
    ],
    "HM",
  );
  const rl = r(8);
  for (const o of lijst) {
    if (o.datum < dag(8, 17)) o.werkdagen = tussen(rl, 7, 10);
    else o.werkdagen = stuks(o) >= 120 ? tussen(rl, 14, 18) : tussen(rl, 11, 13);
  }
  return {
    id: "case-2",
    klant: "Hockeyclub De Meeuwen",
    accountmanager: "Joris Verbeek",
    orders: lijst,
    retouren: [],
    notities: [
      [dag(5, 12), "Nieuwe wedstrijdtenues besteld. Die worden om de twee jaar vervangen."],
      [dag(6, 30), "Het clubhuis wordt deze zomer verbouwd."],
      [dag(8, 20), "Het nieuwe seizoen start op 6 september. Teamkleding wordt vaak laat besteld. Daardoor staat de levertijd onder druk."],
    ],
    levering: true,
  };
}

// case-3: gelijk, weinig maar grote orders. Geen leverdata. Retouren: jassen vallen een maat te klein.
function installateur(): Klant {
  const r = (n: number) => mulberry32(3030 + n);
  const maten = ["S", "M", "L", "XL", "XXL"];
  const jas = { naam: "Softshell jas met logo", prijs: 54, varianten: maten };
  const polo = { naam: "Polo met logo", prijs: 16.5, varianten: maten };
  const bodywarmer = { naam: "Bodywarmer met logo", prijs: 32, varianten: ["M", "L", "XL"] };
  const muts = { naam: "Muts met logo", prijs: 6.75, varianten: ["one size"] };
  const keuzes: Keuze[] = [
    { product: jas, kans: 0.9, varianten: [3, 5], aantal: [3, 12, 1] },
    { product: polo, kans: 0.8, varianten: [3, 5], aantal: [4, 16, 2] },
    { product: bodywarmer, kans: 0.5, varianten: [2, 3], aantal: [3, 9, 1] },
    { product: muts, kans: 0.6, varianten: [1, 1], aantal: [20, 60, 10] },
  ];
  // Q3 lijkt op Q2: dezelfde vaste bestellingen, een kwartaal later, met kleine verschillen.
  const q2 = orders(r(1), 9, dag(4, 1), dag(6, 30), keuzes);
  const rq3 = r(2);
  const q3 = q2.map((o) => kwartaalLater(rq3, o, () => 1));
  const lijst = nummer(
    [
      ...q2,
      ...q3,
      ...orders(r(3), 1, dag(8, 1), dag(8, 31), [{ ...keuzes[0], kans: 1, varianten: [5, 5], aantal: [10, 10, 1] }], "geannuleerd"),
      ...orders(r(4), 1, dag(10, 1), dag(10, 23), [keuzes[3], keuzes[1]], "open"),
    ],
    "VD",
  );
  const metJas = lijst.filter((o) => o.status === "geleverd" && o.datum >= dag(7, 1) && o.regels.some((r) => r.product === jas));
  const retouren = [
    { order: metJas[1], maat: "L" },
    { order: metJas[3], maat: "M" },
  ].map(({ order, maat }) => {
    const regel = order.regels.find((r) => r.product === jas && r.variant === maat) ?? order.regels.find((r) => r.product === jas)!;
    return { order, regel, reden: "valt een maat te klein" };
  });
  return {
    id: "case-3",
    klant: "Van Dijk Installatietechniek",
    accountmanager: "Fatima Bouali",
    info: [["levering", "Geen leverdata. Het magazijnsysteem registreert voor deze klant geen leverdatum."]],
    orders: lijst,
    retouren,
    notities: [
      [dag(4, 8), "Factuuradres gewijzigd naar Industrieweg 12 in Zwolle."],
      [dag(9, 15), "De klant wil in Q4 ook werkbroeken met logo bestellen."],
      [dag(9, 29), "Monteurs zeggen dat de softshell jassen klein vallen."],
    ],
    levering: false,
  };
}

async function schrijf(k: Klant, pad: string) {
  const wb = new ExcelJS.Workbook();
  wb.creator = "Vonk testdata";
  wb.created = wb.modified = dag(10, 28);
  const blad = (naam: string, kop: string[], rijen: (string | number | Date)[][]) => {
    const b = wb.addWorksheet(naam);
    b.addRow(kop).font = { bold: true };
    for (const r of rijen) b.addRow(r);
    b.columns.forEach((c) => (c.width = 18));
  };

  blad("Info", ["veld", "waarde"], [
    ["klant", k.klant],
    ["periode", "Q3 2026"],
    ["vorige periode", "Q2 2026"],
    ["doel levertijd (werkdagen)", 10],
    ["accountmanager", k.accountmanager],
    ...(k.info ?? []),
  ]);
  blad(
    "Orders",
    ["ordernummer", "orderdatum", "product", "variant", "aantal", "stukprijs", "bedrag", "status"],
    k.orders.flatMap((o) =>
      o.regels.map((r) => [o.nummer, o.datum, r.product.naam, r.variant, r.aantal, r.product.prijs, centen(r.aantal * r.product.prijs), o.status]),
    ),
  );
  const geleverd = k.orders.filter((o) => o.status === "geleverd");
  blad(
    "Levering",
    ["ordernummer", "besteldatum", "leverdatum", "werkdagen"],
    k.levering ? geleverd.map((o) => [o.nummer, o.datum, werkdagenNa(o.datum, o.werkdagen!), o.werkdagen!]) : [],
  );
  blad(
    "Retouren",
    ["ordernummer", "product", "variant", "aantal", "reden", "datum"],
    k.retouren.map((r) => [r.order.nummer, r.regel.product.naam, r.regel.variant, r.regel.aantal, r.reden, werkdagenNa(r.order.datum, 15)]),
  );
  blad("Notities", ["datum", "notitie"], k.notities);
  await wb.xlsx.writeFile(pad);
}

async function main() {
  mkdirSync(MAP, { recursive: true });
  for (const k of [bakkerij(), hockeyclub(), installateur()]) {
    const pad = path.join(MAP, `${k.id}.xlsx`);
    await schrijf(k, pad);
    // Lees terug, zodat we zien wat de eval ook ziet.
    const wb = await leesWerkboek(pad);
    const tekst = werkboekAlsTekst(wb);
    const hash = createHash("sha256").update(tekst).digest("hex").slice(0, 12);
    const regels = Object.entries(wb).map(([naam, rijen]) => `${naam} ${rijen.length}`).join(", ");
    console.log(`\n## ${k.id}: ${k.klant} (${pad})`);
    console.log(`Regels: ${regels}. Tekst: ${tekst.length} tekens, hash ${hash}. Eerste order ${datumTekst(k.orders[0].datum)}.`);
    console.log(kerncijfersAlsTekst(kerncijfers(wb, "Q3 2026")));
  }
}

main().catch((e) => {
  console.error(`Fout: ${e instanceof Error ? e.message : String(e)}`);
  process.exit(1);
});
