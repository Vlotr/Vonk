# Concept: eigen monitoring tool

Oct 16, 2026 · @Merlijn van den Berg

## Doel

We bouwen één lichte tool voor foutmeldingen en pagina-statistieken, die iedereen makkelijk zelf kan hosten.

- **Probleem:** zelf-gehoste Sentry is zwaar. Zelf-gehoste PostHog is alleen een hobby-setup. Lichte tools zoals GlitchTip en Rybbit doen maar de helft.
- **Voor wie:** developers en kleine teams met eigen servers. Eerst onze eigen Laravel-apps.
- **Versie 1 lukt als:** we fouten uit Laravel en de browser zien, en per pagina zien hoe vaak en hoe lang die bekeken wordt.

## Features

Versie 1 draait om fouten, pagina's en meldingen. De rest komt stap voor stap.

| Feature | Wat het doet | Versie |
| --- | --- | --- |
| Fouten opvangen | Laravel en browser, met stack trace, pagina, gebruiker en browser | 1 |
| Groeperen | Dezelfde fout wordt één issue, met een teller | 1 |
| Issue-status | Open, opgelost of genegeerd. Komt een opgeloste fout terug, dan gaat het issue weer open | 1 |
| Projecten | Meerdere apps in één dashboard | 1 |
| Meldingen | Via mail of Slack, per project in te stellen | 1 |
| Gebruikers en toegang | Uitnodigen via een link, rollen en toegang per project | 1 |
| Paginaweergaven | Welke pagina's, hoe vaak en hoe lang. Bots tellen apart | 1 |
| Health check | Laat zien of de tool zelf goed draait | 1 |
| Custom events | Eigen events met een grafiek, los van fouten | 2 |
| Gebruikersreis | Welke pagina's iemand zag vóór een fout | 2 |
| Source maps | Leesbare JavaScript-fouten, via de upload-API van Sentry | 2 |
| Releases | Zien in welke versie een fout begon | 2 |
| Trage pagina's | Laadtijd per pagina | 3 |
| Uptime checks | Melding als een site plat ligt | 3 |
| API en MCP | AI en andere tools kunnen data lezen | 3 |
| Session replay | Opname van wat een gebruiker deed | Later, veel werk |

## Meldingen

Elke soort melding is per project aan of uit te zetten en in te stellen.

| Soort | Wanneer | Wat je instelt |
| --- | --- | --- |
| Nieuw issue | Een fout komt voor het eerst binnen | Aan of uit |
| Terugkerend issue | Een opgeloste fout komt terug. Het issue gaat dan weer open | Aan of uit |
| Piek | Veel meer fouten dan normaal | De drempel |
| Samenvatting | Overzicht van de afgelopen periode | Dagelijks, wekelijks of maandelijks |

Meldingen gaan via Slack, en via mail als `MAIL_URL` is ingesteld. Heeft de tool zelf een probleem, zoals een volle schijf, dan krijgen de admins een melding.

## Gebruikers en toegang

Je nodigt mensen uit via een link. Er is geen login via Google of GitHub.

- **Rollen:** admin en lid. Een admin beheert projecten, gebruikers en instellingen.
- **Toegang per project:** een lid ziet alleen de projecten waar hij toegang toe heeft.
- **Eerste gebruiker:** maak je aan via de webpagina. Daarna gaat registratie dicht.
- **Wachtwoord vergeten:** reset via mail. Zonder mail reset een admin het wachtwoord van een lid in Settings. Een admin zelf reset via een commando op de server.

## SDK's

We bouwen geen eigen SDK's. We doen ons voor als Sentry en PostHog, zodat gebruikers hun bestaande SDK's houden.

| SDK | Voor | Wat wij nabouwen | Let op |
| --- | --- | --- | --- |
| [Sentry](https://develop.sentry.dev/sdk/foundations/transport/) (Laravel en browser) | Fouten | `/api/<project-id>/envelope/`, inloggen via de DSN. In versie 2 ook de upload-API voor source maps | Gzip-compressie en rate-limit headers ondersteunen |
| [PostHog](https://posthog.com/docs/libraries/js/config) (browser) | Pagina's en events | `api_host` wijst naar ons. Endpoints: `/i/v0/e/`, `/batch/` en `/flags/?v=2` | Flags eerst als lege lijst teruggeven |

- **Tijd per pagina:** het verschil tussen pageview en pageleave. Zonder pageleave, bijvoorbeeld als iemand het tabblad sluit, telt het bezoek niet mee in de gemiddelde tijd. We markeren het met "gebruiker heeft de pagina verlaten".
- **Bots:** bekende bots herkennen we aan de user agent. Ze tellen niet mee in de statistieken, maar we tonen het aantal apart.
- **Koppelen:** gebruik dezelfde user id in Sentry en PostHog. Dan zie je bij een fout ook de pagina's ervoor.
- **Laravel-fouten:** alleen via Sentry. Nightwatch laten we weg, anders komen fouten dubbel binnen.

## Zelf hosten

De tool draait standaard als één container, met alle data op één volume.

### Installatie

- **Alles in één image:** backend, dashboard en achtergrondtaken.
- **Geen extra services:** geen Postgres, Redis of queue nodig. Postgres kan optioneel voor grote installaties.
- **Overal te draaien:** image voor x86 en ARM.
- **HTTPS:** via een reverse proxy, zoals Caddy of Traefik. De tool zelf spreekt HTTP. De handleiding heeft een voorbeeld met Caddy.
- **Bereikbaar vanaf internet:** browser-fouten komen van de apparaten van bezoekers.

### Instellingen

| Instelling | Verplicht | Waarvoor |
| --- | --- | --- |
| `SECRET_KEY` | Ja | Beveiliging van sessies en sleutels |
| `BASE_URL` | Ja | Het adres van de tool |
| `DATABASE_URL` | Nee | Postgres in plaats van SQLite |
| `MAIL_URL` | Nee | SMTP voor mail. Zonder mail gaan meldingen alleen via Slack |

Zo ziet het compose-bestand eruit voor een gebruiker:

```yaml
services:
  app:
    image: jouwnaam/monitor:1
    ports: ["8000:8000"]
    environment:
      SECRET_KEY: verander-mij
      BASE_URL: https://monitor.example.com
      # MAIL_URL: smtp://gebruiker:wachtwoord@mail.example.com:587
    volumes: ["data:/data"]
volumes:
  data:
```

### Beheer

- **Bewaartermijn:** per project 30 dagen, 90 dagen, 180 dagen, 1 jaar of onbeperkt. Standaard 90 dagen. Oude data gaat automatisch weg.
- **Updates:** database-updates draaien bij het opstarten. Daarvoor maakt de tool automatisch een backup.
- **Backup en terugzetten:** in Settings. De stappen staan ook in de handleiding. Een backup is één volume kopiëren.
- **Health check:** een `/health`-endpoint, een statuspagina in Settings en een melding als er iets mis is, zoals een volle schijf.

## Techniek

We kiezen Go met SQLite, omdat dat één klein bestand oplevert dat weinig geheugen gebruikt.

| Onderdeel | Keuze | Waarom |
| --- | --- | --- |
| Backend | Go | Eén klein bestand, snel, weinig RAM, makkelijk voor ARM |
| Dashboard | React met Vite | Bekend voor ons, wordt in het Go-bestand ingebouwd |
| Database standaard | SQLite in WAL-modus | Eén bestand, geen setup |
| Database optioneel | PostgreSQL | Voor grote installaties, via `DATABASE_URL` |
| Later | DuckDB | Snelle grafieken bij veel events, ook een bestand |
| Niet | ClickHouse | Te zwaar voor makkelijk zelf hosten |

- **SQLite snel houden:** events in batches schrijven. Per uur samenvattingen maken, zoals views en tijd per pagina. Het dashboard leest alleen die samenvattingen.
- **Geen CGO:** een pure Go SQLite-driver, zodat bouwen voor ARM simpel blijft.
- **Verwachte grootte:** image van ongeveer 20 tot 30 MB.

## Architectuur

Alles draait in één Go-proces. Alleen de database is een los bestand op het volume.

```mermaid
flowchart TD
    L["Laravel-app (Sentry SDK)"] --> P
    B["Browser (Sentry en PostHog SDK)"] --> P
    P["Reverse proxy, bijv. Caddy (HTTPS)"] --> I
    subgraph C["Eén container (Go)"]
        I["Ingest"] --> V["Verwerker"]
        V --> D[("Database: SQLite of Postgres")]
        D --> DB["Dashboard (React)"]
    end
    V --> M["Mail of Slack"]
    DB --> T["Jouw team"]
```

Apps sturen data naar de ingest. De verwerker schrijft het in batches weg, en het dashboard leest alleen uit de database. Een reverse proxy zoals Caddy zit voor de container en regelt HTTPS.

## Datamodel

Elf tabellen dekken versie 1 en 2.

| Tabel | Wat erin staat | Belangrijkste velden |
| --- | --- | --- |
| users | Wie kan inloggen | e-mail, wachtwoord-hash, rol (admin of lid) |
| invites | Openstaande uitnodigingen | e-mail, rol, token, verloopt op |
| project\_access | Welk lid welk project ziet | gebruiker, project |
| projects | Eén per app | naam, Sentry-sleutel, PostHog-token, bewaartermijn (dagen of onbeperkt) |
| issues | Gegroepeerde fouten | project, fingerprint, titel, status, eerste en laatste keer, opgelost op, teller |
| error\_events | Losse fouten | issue, tijd, gebruiker, pagina, release, data als JSON |
| pageviews | Paginabezoeken | project, sessie, gebruiker, pagina, start, duur, verlaten zonder pageleave |
| custom\_events | Eigen events | project, naam, gebruiker, tijd, data als JSON |
| hourly\_stats | Samenvattingen per uur | project, pagina, uur, views, unieke bezoekers, totale duur, bots |
| alerts | Instellingen voor meldingen | project, soort, kanaal (mail of Slack), drempel of frequentie |
| source\_maps | Source maps, vanaf versie 2 | project, release, bestand |

De fingerprint maken we uit het fouttype en de bovenste regels van de stack trace. Zo worden gelijke fouten één issue.

## Privacy en AVG

We slaan zo weinig mogelijk op, en alle data blijft op de eigen server.

- **IP-adressen:** niet opslaan, of het laatste deel weghalen.
- **Gevoelige velden:** wachtwoorden, tokens en cookies uit foutdata filteren voordat we opslaan.
- **Bewaartermijn:** standaard 90 dagen, per project in te stellen tot onbeperkt. Bij onbeperkt groeit de schijf, en moet de beheerder zelf op de AVG letten.
- **Verwijderen op verzoek:** alle data van één gebruiker wissen via zijn user id.
- **Cookies:** in de docs adviseren we PostHog zonder cookies te draaien. Of dan een cookiebanner nodig is, checken we met een jurist.

## Planning

We bouwen in vijf fases. Elke fase heeft een duidelijk "klaar als".

1. **Proof of concept:** Sentry-envelopes ontvangen en fouten in een lijst tonen.
   - Klaar als: een Laravel-fout in de lijst staat.
2. **Fouten compleet:** groeperen, status en heropenen, projecten, gebruikers en toegang, meldingen, health check en de ene container.
   - Klaar als: onze eigen apps erop draaien.
3. **Pagina's:** PostHog-endpoints, pageviews, tijd per pagina, bots apart en samenvattingen per uur.
   - Klaar als: we per pagina views en gemiddelde tijd zien.
4. **Versie 2:** custom events, gebruikersreis, source maps en releases.
   - Klaar als: we bij een fout de pagina's ervoor zien.
5. **Delen met anderen:** handleiding met Caddy-voorbeeld, backup en terugzetten, publieke image en de Postgres-optie.
   - Klaar als: iemand anders het in 10 minuten draait.

## Risico's en open vragen

Het grootste risico is dat Sentry of PostHog hun SDK's veranderen.

| Risico | Wat we doen |
| --- | --- |
| SDK-update breekt onze endpoints | Geteste SDK-versies noemen, automatische tests per SDK |
| PostHog-SDK roept meer endpoints aan dan we kennen | Testen welke, en die functies uitzetten in de config |
| SQLite wordt traag bij veel verkeer | Batches, samenvattingen per uur, Postgres als optie |
| Onbeperkt bewaren vult de schijf | Standaard 90 dagen, en een melding als de schijf bijna vol is |
| Adblockers blokkeren PostHog | Eigen domein of pad gebruiken |
| Spam op het publieke endpoint | Sleutel per project controleren, limiet per minuut |
| Go is nieuw voor ons | Klein beginnen, AI laten helpen |

- [ ] Hoe heet de tool?
- [ ] Wordt het open source, en met welke licentie?
- [ ] Wie bouwt het, en hoeveel uur per week?
- [ ] Hoeveel events per dag verwachten we?
- [ ] Is een cookiebanner nodig? Vragen aan een jurist.
