# Bevindingen eval-experiment

2 runs op 28 oktober 2026. Test-model `claude-sonnet-5-5`, 3 runs per case. Judge `claude-opus-5-5`. Jev `typesafe/jev-1.14`.

- Run 1: `results/run-2026-10-28_16-53-54.md`
- Run 2: `results/run-2026-10-28_16-55-27.md`

## 1. Scoren de gouden rapporten hoog (0,9 of meer) op 1 tot 15?

**Ja.** Alle 3 de gouden rapporten scoren in beide runs 0,99 in totaal.

- Op elk testpunt is de score 0,9 of meer.
- De laagste is testpunt 11 (namen): 0,90. Jev is daar wel zeker (0,80 tot 0,90), maar geeft nooit meer dan 0,90 tot 0,95.

## 2. Scoort het slechte rapport duidelijk lager?

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

## 3. Werkt Jev goed met Nederlands? Hoe vaak was Jev niet zeker?

**Grotendeels ja.**

- Jev was **niet zeker** bij 27 van de 313 vragen (9%) in run 1, en bij 23 van de 316 vragen (7%) in run 2.
- Bij feiten (7), tegenspraak (10) en taal (6) was Jev **nooit** onzeker.
- Bijna alle onzekere vragen zaten bij:
  - testpunt 9 (per zin): 20 en 18 keer
  - testpunt 11 (namen): 6 en 5 van de 13 keer. Dat is bijna de helft.
- Bij die vragen deed de LLM het opnieuw. In 7 en 6 gevallen kwam de LLM op het andere antwoord uit. De LLM was zelf ook vaak niet zeker (16 en 9 keer).
- Zwak punt: één Engelse zin tussen Nederlandse zinnen (zie vraag 2). In een korte proef met maar 2 zinnen zag Jev het Engels wel (kans op Engels 0,99).
- Jev is erg goedkoop: $0,003 per run, voor 42 calls.

## 4. Liggen de totaalscores van de 2 runs dicht bij elkaar?

**Ja.** Run 1: 0,908. Run 2: 0,914. Het verschil is 0,006.

Per testpunt bewegen vooral de judge-punten:

- Testpunt 13, 14 en 15 verschillen elk 0,06.
- Testpunt 15 is het minst stabiel. Case-2 ging van 0,50 naar 1,00, case-3 van 0,50 naar 0,33.
- Bij testpunt 15 was de judge het in run 1 bij 4 van de 9 rapporten met zichzelf oneens. Hij koos dan beide keren dezelfde plek (twee keer A, of twee keer B), dus niet het beste rapport. In run 2 was hij het 1 keer oneens.
- Testpunt 16 (lijken de runs op elkaar): 0,91 en 0,88.

## 5. Wat kostte een run en hoe lang duurde hij?

| | Run 1 | Run 2 |
| --- | --- | --- |
| Kosten totaal | $0,71 | $0,62 |
| Judge | $0,60 | $0,52 |
| Test-model (9 calls) | $0,11 | $0,09 |
| Jev (42 calls) | $0,003 | $0,003 |
| Duur | 1 min 2 s | 55 s |
| Test-model per call | 8,4 s | 7,4 s |

De judge kost ongeveer 85% van het geld.

## Ook opgevallen

- **Lengte (testpunt 3) scoort laag: 0,68 en 0,65.** De rapporten hebben 153 tot 178 woorden. Dat past in de regel (120 tot 220), maar het goud heeft er 118 tot 133. Dit testpunt meet dus "lijkt op goud", niet "past in de regel".
- **De judge zegt vaak "niet zeker" bij 12 tot 14:** bij 12 van de 18 rapporten. Bij goud en bij het slechte rapport was hij altijd zeker. Daardoor komt de meeste twijfel van de judge (22 van 32 en 19 van 26 gevallen), niet van Jev.
- **Testpunt 8 ving één keer "Q4"** in case-1 ("Wij kijken uit naar een vervolg in Q4!"). Q4 staat niet in de data van case-1. Het is een klein ding, maar de check doet wat hij moet doen.
- **Afwijking van de opdracht:** de judge draait niet op temperature 0. Sinds 21 oktober 2026 geeft de API een fout als je `temperature` meestuurt. Dat kan meespelen bij de schommelingen in 13 tot 15.
