# Bevindingen eval-experiment

## Test-input 2.0: Excel per klant

28 oktober 2026. Per klant een Excel met 130 tot 171 orderregels over Q2 en Q3 2026, in vijf tabbladen. Er staan geen totalen in. Het model moet zelf rekenen en verbanden zoeken. Prompt v2, gouden standaard 2.0.0, checks versie 2.

- Controles: `results/controles-2026-10-28_17-47-10.md` (`npm run controles`)
- Eval: `results/run-2026-10-28_17-57-47.md`. Test-model `claude-sonnet-5-5`, 3 runs per case. Judge `claude-opus-5-5`. Jev `typesafe/jev-1.14`.

### 1. Vangt elke check zijn fout?

**Ja.** Alle 20 controles slagen. Elk controle-rapport is een gouden rapport met één bekende fout. Elk testpunt heeft minstens één controle.

| # | Controle | Goud | Controle | Grens |
| --- | --- | --- | --- | --- |
| 1 | 2 koppen vervangen door "Overig" | 1,00 | 0,67 | ≤ 0,70 |
| 2 | Cijfers en Producten omgedraaid | 1,00 | 0,00 | ≤ 0,00 |
| 3 | Twee keer zo lang | 1,00 | 0,01 | ≤ 0,50 |
| 4 | Alles in een ``` blok | 1,00 | 0,00 | ≤ 0,00 |
| 5 | `{klant}` laten staan | 1,00 | 0,00 | ≤ 0,00 |
| 6 | Advies in het Engels | 0,99 | 0,01 | ≤ 0,50 |
| 6 | Eén Engelse zin | 0,99 | 0,00 | ≤ 0,50 |
| 7 | Feiten en verbanden weg of vaag | 0,99 | 0,25 | ≤ 0,60 |
| 8 | 8 van de 40 getallen fout gerekend | 1,00 | 0,80 | ≤ 0,85 |
| 9 | 6 verzonnen zinnen | 0,93 | 0,78 | ≤ 0,85 |
| 10 | Verkeerde verklaringen (case-2) | 0,99 | 0,04 | ≤ 0,50 |
| 11 | Klantnaam en 2 productnamen fout | 0,93 | 0,06 | ≤ 0,50 |
| 12 | Informeel, met emoji's | 1,00 | 0,00 | ≤ 0,50 |
| 13 | Alleen kale getallen | 1,00 | 0,00 | ≤ 0,50 |
| 14 | Lange kronkelzinnen | 1,00 | 0,00 | ≤ 0,50 |
| 15 | Slecht rapport, veel fouten | 1,00 | 0,00 | ≤ 0,00 |
| 16 | 3 runs, zelfde inhoud | – | 1,00 | ≥ 0,75 |
| 16 | 3 runs, andere inhoud | – | 0,31 | ≤ 0,50 |
| 17 | Vaste tokens geven $0,04; model zonder prijs geeft fout | – | klopt | – |
| 18 | 1,5 s wachten wordt 1,5 s gemeten | – | klopt | – |

Krap gevangen:

- **8 (getallen): 0,80.** De check is een gemiddelde over alle getallen. Een paar fouten in een lang rapport tellen weinig. "81 orders" (orderregels geteld als orders) kwam erdoor: 81 is toevallig het juiste aantal orderregels. De check kijkt of een getal ergens voorkomt, niet wat het betekent.
- **9 (verzonnen): 0,78.** Alle 6 verzonnen zinnen werden gevonden, maar het gemiddelde over 38 zinnen zakt weinig.

Ook opgevallen:

- **Eén Engelse zin wordt nu gevangen (0,00).** In 1.0 gaf dat 0,72.
- Jev ziet fout gespelde namen ook als tegenspraak met goud (testpunt 10: 0,18).
- Goud case-3 scoort 0,89 op namen (11), net onder 0,9.

### 2. Kan het model rekenen en uitzoeken?

**Ja.** Totaalscore 0,88. Gouden rapporten 0,98 tot 0,99, slecht rapport 0,31.

- **Rekenen:** de kerncijfers kloppen in alle 9 rapporten: omzet, groei, orders, orderwaarde, levertijd en % op tijd. Geannuleerde en open orders tellen niet mee. Orders zijn unieke ordernummers.
- **Uitzoeken:** de verbanden worden gevonden. De groei komt van de tassen, de daling van de tenues, de late levering valt rond de start van het seizoen en de jassen vallen te klein. Testpunt 13 (volledig en diep) is overal 1,00.
- **Meer dan gevraagd:** het model rekent ook het aandeel per product uit, de omzet van geannuleerde orders en de levertijd in Q2. Volgens de judge is de output in 7 van de 9 rapporten beter dan goud (testpunt 15). 1 keer slechter, 1 keer was de judge het met zichzelf oneens.
- **Eén echte rekenfout:** "Zonder dit product daalde de omzet met 7%" (case-2). Dat moet 9% zijn. Testpunt 9 ving dit, testpunt 8 niet (zie vraag 3).
- **Grijs gebied:** case-3 run 1 zegt dat de daling door een geannuleerde order komt. De omzet daalde € 154, de geannuleerde order was € 2.700. Dat is een uitleg, geen feit uit de data. Testpunt 9 en 10 rekenen het af.

### 3. Waar trekken de checks onterecht af?

| # | Score | Waarom |
| --- | --- | --- |
| 3 | 0,63 | De rapporten hebben 353 tot 397 woorden. Dat past in de regel (250 tot 400), maar het goud heeft er 258 tot 294. Net als in 1.0 meet dit "lijkt op goud". |
| 8 | 0,94 | 41 getallen afgekeurd, en alle 41 zijn goed uitgerekend. Ze staan alleen niet in de kerncijfers, zoals € 2.523 aan geannuleerde orders of 88% op tijd in Q2. De echte fout (7%) kwam erdoor, want 7 staat ook in de input. |
| 10 | 0,72 | Eén ja/nee-vraag voor het hele rapport. Twijfelt Jev, dan bepaalt één LLM-antwoord de score: 0 of 1. Case-2 gaf 0,00, 1,00 en 0,70 voor drie rapporten die inhoudelijk op elkaar lijken. |
| 11 | 0,86 | Het model kort productnamen af in tabellen: "Katoenen tas" in plaats van "Katoenen tas met logo". Case-1: 0,73. |
| 12 en 14 | 0,75 | De judge geeft meestal 4 van 5, en zegt bij 8 van de 9 rapporten "niet zeker". |

Daarnaast is **het goud te mager** voor deze input: het model scoort er in 7 van de 9 keer boven.

### 4. Werkt Jev met de grote input?

**Ja, maar Jev is vaker onzeker.**

- Geen fouten, ook niet met 10.000 tot 14.000 tekens data in de state.
- Jev kan geen honderden regels optellen. Daarom krijgt hij de kerncijfers mee, uitgerekend door code.
- Niet zeker bij 113 van de 784 vragen (14%) bij de controles, en bij 144 van de 612 (24%) in de eval. In 1.0 was dat 7 tot 9%.
- Bijna alle twijfel zit bij testpunt 9 (per zin): 5 tot 16 vragen per rapport.

### 5. Wat kostte het en hoe lang duurde het?

| | Controles | Eval |
| --- | --- | --- |
| Kosten totaal | $5,47 | $8,09 |
| Judge | $5,46 (127 calls) | $7,30 (183 calls) |
| Test-model | – | $0,78 (9 calls) |
| Jev | $0,015 | $0,011 |
| Duur | 37 s | 3 min 46 s |

- De judge kost 90% van het geld. Elke onzekere Jev-vraag gaat opnieuw naar de judge, met de hele Excel als tekst erbij.
- Het test-model schrijft zo'n 700 tokens rapport, maar gebruikt gemiddeld 7.104 tokens uit per call: het rekent in zijn denkstappen. Een call duurt 45 s (1.0: 7 tot 8 s).

### Volgende stappen

- **Testpunt 8:** meer kerncijfers, zoals geannuleerde en open orders, levertijd in Q2 en aandeel per product.
- **Kosten:** de twijfelvragen per rapport bundelen in één judge-call.
- **Testpunt 10:** per bewering vragen, niet één vraag voor het hele rapport.
- **Testpunt 11:** een afgekorte productnaam goedkeuren als hij eenduidig is.
- **Goud 2.0.0:** rijker maken, nu het model er meestal boven zit.

## Test-input 1.0: kleine cases

2 runs op 28 oktober 2026. Test-model `claude-sonnet-5-5`, 3 runs per case. Judge `claude-opus-5-5`. Jev `typesafe/jev-1.14`.

- Run 1: `results/run-2026-10-28_16-53-54.md`
- Run 2: `results/run-2026-10-28_16-55-27.md`

### 1. Scoren de gouden rapporten hoog (0,9 of meer) op 1 tot 15?

**Ja.** Alle 3 de gouden rapporten scoren in beide runs 0,99 in totaal.

- Op elk testpunt is de score 0,9 of meer.
- De laagste is testpunt 11 (namen): 0,90. Jev is daar wel zeker (0,80 tot 0,90), maar geeft nooit meer dan 0,90 tot 0,95.

### 2. Scoort het slechte rapport duidelijk lager?

**Ja.** Het slechte rapport scoort 0,34 (run 1) en 0,33 (run 2). Het goud scoort 0,99.

Gevangen:

| Fout | Testpunt | Score |
| --- | --- | --- |
| Koppen in verkeerde volgorde | 2 | 0,00 |
| Te kort (32 woorden, goud heeft er 133) | 3 | 0,24 |
| Restje `{periode}` en lege sectie Advies | 5 | 0,00 |
| Fout getal (€ 14.000) | 8, 9 en 10 | 0,33 / zin 0,02 / 0,04 |
| Verzonnen feit (vestiging in Utrecht) | 9 | zin 0,02 |
| Feiten missen | 7 | 0,02 |
| In totaal slechter dan goud | 12 tot 15 | 0,00 tot 0,25 |

Half gevangen:

- **De Engelse zin.** Testpunt 6 gaf 0,78 en 0,72 (goud: 1,00). Jev was zeker genoeg (0,71 en 0,63), dus de LLM keek niet opnieuw. Eén Engelse zin moet volgens de opdracht ook tellen, maar de score is niet 0.

Testpunt 1, 4 en 11 gaven 1,00. Dat klopt: alle koppen staan erin, het is markdown en de namen kloppen. Die fouten zitten niet in het slechte rapport.

### 3. Werkt Jev goed met Nederlands? Hoe vaak was Jev niet zeker?

**Grotendeels ja.**

- Jev was **niet zeker** bij 27 van de 313 vragen (9%) in run 1, en bij 23 van de 316 vragen (7%) in run 2.
- Bij feiten (7), tegenspraak (10) en taal (6) was Jev **nooit** onzeker.
- Bijna alle onzekere vragen zaten bij:
  - testpunt 9 (per zin): 20 en 18 keer
  - testpunt 11 (namen): 6 en 5 van de 13 keer. Dat is bijna de helft.
- Bij die vragen deed de LLM het opnieuw. In 7 en 6 gevallen kwam de LLM op het andere antwoord uit. De LLM was zelf ook vaak niet zeker (16 en 9 keer).
- Zwak punt: één Engelse zin tussen Nederlandse zinnen (zie vraag 2). In een korte proef met maar 2 zinnen zag Jev het Engels wel (kans op Engels 0,99).
- Jev is erg goedkoop: $0,003 per run, voor 42 calls.

### 4. Liggen de totaalscores van de 2 runs dicht bij elkaar?

**Ja.** Run 1: 0,908. Run 2: 0,914. Het verschil is 0,006.

Per testpunt bewegen vooral de judge-punten:

- Testpunt 13, 14 en 15 verschillen elk 0,06.
- Testpunt 15 is het minst stabiel. Case-2 ging van 0,50 naar 1,00, case-3 van 0,50 naar 0,33.
- Bij testpunt 15 was de judge het in run 1 bij 4 van de 9 rapporten met zichzelf oneens. Hij koos dan beide keren dezelfde plek (twee keer A, of twee keer B), dus niet het beste rapport. In run 2 was hij het 1 keer oneens.
- Testpunt 16 (lijken de runs op elkaar): 0,91 en 0,88.

### 5. Wat kostte een run en hoe lang duurde hij?

| | Run 1 | Run 2 |
| --- | --- | --- |
| Kosten totaal | $0,71 | $0,62 |
| Judge | $0,60 | $0,52 |
| Test-model (9 calls) | $0,11 | $0,09 |
| Jev (42 calls) | $0,003 | $0,003 |
| Duur | 1 min 2 s | 55 s |
| Test-model per call | 8,4 s | 7,4 s |

De judge kost ongeveer 85% van het geld.

### Ook opgevallen

- **Lengte (testpunt 3) scoort laag: 0,68 en 0,65.** De rapporten hebben 153 tot 178 woorden. Dat past in de regel (120 tot 220), maar het goud heeft er 118 tot 133. Dit testpunt meet dus "lijkt op goud", niet "past in de regel".
- **De judge zegt vaak "niet zeker" bij 12 tot 14:** bij 12 van de 18 rapporten. Bij goud en bij het slechte rapport was hij altijd zeker. Daardoor komt de meeste twijfel van de judge (22 van 32 en 19 van 26 gevallen), niet van Jev.
- **Testpunt 8 ving één keer "Q4"** in case-1 ("Wij kijken uit naar een vervolg in Q4!"). Q4 staat niet in de data van case-1. Het is een klein ding, maar de check doet wat hij moet doen.
- **Afwijking van de opdracht:** de judge draait niet op temperature 0. Sinds 21 oktober 2026 geeft de API een fout als je `temperature` meestuurt. Dat kan meespelen bij de schommelingen in 13 tot 15.
