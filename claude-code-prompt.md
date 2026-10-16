# Prompts om de monitoring tool te bouwen met Claude Code

Bouw de tool fase voor fase, met elke fase in een eigen sessie. Eén prompt voor alles levert slechter werk op, en zo kun je tussendoor bijsturen.

- **Start:** open een nieuwe Claude Code-sessie in deze map en plak de startprompt. Die doet het fundament en fase 1, en stopt daarna.
- **Daarna:** begin per fase een nieuwe sessie en plak de vervolgprompt. De startprompt laat een `CLAUDE.md` achter, dus elke sessie weet hoe het project in elkaar zit.
- **Eerst een plan:** elke prompt vraagt om eerst een plan en wacht op jouw akkoord. Lees het plan echt door, want daar stuur je het goedkoopst bij.

---

## Startprompt: fundament en fase 1

````text
Je bouwt in deze map een zelf-gehoste monitoring tool voor foutmeldingen (via de Sentry-SDK's) en pagina-statistieken (via de PostHog-SDK).

Alles wat we al besloten hebben staat in concept-monitoring-tool.md. Het ontwerp van het dashboard staat in "Monitoring dashboard-png/": vijf schermen (de PDF bevat dezelfde schermen). Lees het concept helemaal en bekijk alle vijf de PNG's voordat je iets anders doet.

# Wat ik nu wil

Het fundament en fase 1 (proof of concept) uit de planning in het concept. Stop daarna en laat me het resultaat zien. Fase 2 en verder doen we in aparte sessies.

Fase 1 is klaar als een echte fout uit een Laravel-app, verstuurd met de officiële sentry/sentry-laravel SDK, in de issuelijst van ons dashboard staat. Dat bewijs je zelf, in de browser. Alleen unit tests zijn niet genoeg.

# Werkwijze

- Maak eerst een plan en wacht op mijn akkoord voordat je code schrijft. Stel vragen waar het concept iets openlaat.
- Go is nieuw voor ons. Schrijf simpele, gewone Go: de standaardbibliotheek waar het kan (net/http met de routing van Go 1.22+, log/slog, embed), weinig dependencies, geen web-frameworks. Zet in het plan welke dependencies je wilt en waarom.
- Werk in kleine stappen, met een commit na elke stap die werkt. De map is nog geen git-repo, dus begin met git init en een .gitignore.
- Praat Nederlands met mij. Code, comments, commits en de UI zijn Engels, net als de ontwerpen.
- Maak aan het eind een CLAUDE.md met hoe je bouwt, test en draait, de mappenstructuur en de afspraken uit deze prompt. Volgende sessies beginnen daar.

# Fundament

- Go staat nog niet op deze Mac. Node 24, PHP 8.4, Composer en Docker wel. Installeer Go via Homebrew, of vraag het mij.
- Stel een mappenstructuur voor, bijvoorbeeld cmd/, internal/ingest, internal/store, internal/web en web/ voor het dashboard.
- Database: SQLite via een pure Go-driver zonder CGO (bijvoorbeeld modernc.org/sqlite), in WAL-modus. Houd de store-laag zo dat Postgres later past (fase 5), maar bouw Postgres nu niet. Migraties zitten in de binary en draaien bij het opstarten.
- Datamodel: volg de tabellen uit het concept, maar maak nu alleen wat fase 1 nodig heeft (projects, issues, error_events).
- Dashboard: React met Vite en TypeScript. De build komt via go:embed in de Go-binary. In development draait de Vite dev server met een proxy naar Go.
- Instellingen via env: SECRET_KEY en BASE_URL verplicht, DATABASE_URL en MAIL_URL optioneel. Data staat in /data. Stop met een duidelijke foutmelding als een verplichte waarde ontbreekt.
- Eén multi-stage Dockerfile, met een image voor amd64 en arm64. Een Makefile met in elk geval dev, test, build en docker.
- De naam van de tool is nog niet bekend. Zet hem op één plek en toon "[Tool name]", zoals in het ontwerp.

# Fase 1: Sentry-fouten ontvangen en tonen

Ingest:
- POST /api/<project-id>/envelope/. Lees de Sentry-documentatie over envelopes, DSN's en transport (de link staat in het concept). Ga niet gokken.
- Inloggen met de public key uit de X-Sentry-Auth-header, de sentry_key-queryparameter (die gebruikt de browser-SDK) of de dsn in de envelope-header. De key moet bij het project-id horen. Bij een onbekende key: 401.
- Ondersteun gzip via Content-Encoding. Test welke compressie de echte SDK's sturen.
- Verwerk items van het type event. Andere types (transaction, session, client_report, attachment en andere) accepteer je met 200 en negeer je, zodat de SDK's niet opnieuw gaan proberen.
- CORS-headers voor de browser-SDK, want die stuurt vanaf andere domeinen.
- Een limiet per project per minuut. Daarboven: 429 met Retry-After en X-Sentry-Rate-Limits. Zet ook een maximum op de grootte van een request.
- Privacy vóór het opslaan: wachtwoorden, tokens, cookies en Authorization-headers eruit filteren, en geen volledig IP-adres opslaan. Maak hier een aparte functie van, met tests.
- Schrijf events in batches weg via een achtergrondtaak, niet per request. Bij het afsluiten schrijf je wat nog in de buffer staat weg.

Groeperen (de basis hoort in fase 1, fase 2 maakt het af):
- Fingerprint = fouttype plus de bovenste in-app frames (bestand en functie, zonder regelnummers). Stuurt de SDK zelf een fingerprint mee, dan gebruik je die.
- Zelfde fingerprint: zelfde issue, de teller gaat omhoog en last seen wordt bijgewerkt.

Dashboard:
- Een lijst met projecten, en het scherm uit Issues.png: open issues met type, bericht, bron (Laravel of Browser), bestand en regel, events, users en last seen. De balkjes voor de laatste 24 uur alleen als dat simpel lukt.
- Een eerste versie van Issue detail.png: de stack trace met de regel van de fout gemarkeerd en de framework-regels ingeklapt.
- Volg het ontwerp precies: kleuren, lettertype, afstanden en de koppen die een conclusie zijn ("14 open issues in Webshop"). Lees de waardes af uit de PNG's en zet ze als CSS-variabelen.
- Onderdelen van latere fases (Pages, Events, Alerts, "Before this error", Release) laat je nog weg. Verzin geen nepdata.
- Login komt in fase 2. Zet het dashboard nu achter een tijdelijke beveiliging, bijvoorbeeld basic auth met een env-variabele, zodat het niet open op internet staat.
- Een project maak je in fase 1 aan met een CLI-commando dat de DSN laat zien.

Bewijs:
- Unit tests voor het lezen van envelopes, gzip, inloggen met de key, het filteren van gevoelige data en de fingerprint. Gebruik als fixtures echte envelopes die je opvangt van de echte SDK's, niet zelf verzonnen JSON.
- Een demo-app in examples/laravel/ met sentry/sentry-laravel, waarin een route een exception gooit. Laat die fout naar onze tool gaan, en laat met een screenshot uit de browser zien dat hij in de lijst staat.
- Hetzelfde voor de browser in examples/browser/, met @sentry/browser.
- Twee keer dezelfde fout geeft één issue met teller 2. Een andere fout geeft een nieuw issue.
- Zet in de README met welke SDK-versies je getest hebt.

# Stop en vraag het mij

- Als de Sentry-documentatie en het concept iets anders zeggen.
- Als je een dependency wilt toevoegen die niet in het goedgekeurde plan stond.
- Bij keuzes die later moeilijk terug te draaien zijn, zoals het datamodel, de URL-structuur of hoe inloggen werkt.
````

---

## Vervolgprompts

Plak elke prompt in een nieuwe sessie. Ze zijn korter, omdat `CLAUDE.md` en het concept de rest vertellen.

### Fase 2: fouten compleet

````text
Lees CLAUDE.md en concept-monitoring-tool.md, en bekijk de PNG's in "Monitoring dashboard-png/". We doen fase 2: fouten compleet. Klaar als onze eigen Laravel-apps erop draaien. Maak eerst een plan en wacht op mijn akkoord.

- Issue-status: open, resolved en ignored. Komt een opgeloste fout terug, dan gaat het issue weer open. Bouw de tabs met tellers, het filter op bron, sorteren en zoeken uit Issues.png.
- Issue detail.png afmaken: Resolve, Ignore, de link kopiëren, de cijfers bovenaan, events per uur, User en Details. Voor Country heb je het IP-adres nodig, en dat slaan we niet op. Stel voor hoe dat kan, bijvoorbeeld een offline GeoIP-database bij binnenkomst en het IP daarna weggooien, of laat het weg. Vraag het mij.
- All projects.png: het overzicht van alle projecten met nieuwe issues, fouten in 24 uur en de nieuwste issues. Ververs elke minuut. De kolommen voor pageviews tonen "Add page tracking" tot fase 3.
- Settings.png: projecten aanmaken met een bewaartermijn en wat je wilt volgen, en per project "Setup and keys" met de DSN en de stappen voor Laravel en de browser.
- Gebruikers en toegang zoals in het concept: de eerste gebruiker via de webpagina en daarna registratie dicht, uitnodigen via een link die verloopt, de rollen admin en lid, en toegang per project. Een lid mag andere projecten ook via de API niet zien. Schrijf daar tests voor. Wachtwoord vergeten: via mail, een admin reset een lid, en een CLI-commando voor de admin zelf.
- Veilig inloggen: een goede wachtwoord-hash (argon2id of bcrypt), httpOnly- en secure-cookies, CSRF-bescherming en een limiet op inlogpogingen. Haal de tijdelijke basic auth uit fase 1 weg.
- Meldingen: nieuw issue, terugkerend issue, piek met een drempel, en een samenvatting per dag, week of maand. Per project in te stellen, via Slack (webhook) en via mail als MAIL_URL er is. Stuur dezelfde melding niet steeds opnieuw. Admins krijgen een melding als de tool zelf een probleem heeft, zoals een bijna volle schijf.
- Bewaartermijn: een dagelijkse taak die oude data weghaalt.
- Health: een /health-endpoint, een statuspagina in Settings en een controle op schijfruimte.
- Maak automatisch een backup vóór de database-updates bij het opstarten.
- Controleer aan het eind dat het compose-bestand uit het concept werkt met één container en één volume.
````

### Fase 3: pagina's

````text
Lees CLAUDE.md en concept-monitoring-tool.md, en bekijk Pages.png en All projects.png. We doen fase 3: pagina's. Klaar als we per pagina de views en de gemiddelde tijd zien. Maak eerst een plan en wacht op mijn akkoord.

- PostHog-endpoints: /i/v0/e/, /batch/ en /flags/?v=2 (voorlopig met een lege lijst flags). Inloggen met het project-token uit de body.
- Zoek met een echte posthog-js uit welke endpoints hij nog meer aanroept, zoals /decide/, remote config onder /array/ en /s/ voor session recording. Geef die een nette lege reactie en zet in de docs welke config-opties uit moeten.
- Ondersteun de compressie die posthog-js gebruikt (gzip-js en base64). Vang echte requests op als fixtures.
- Van $pageview en $pageleave maak je pageviews. De duur is het verschil tussen de twee. Zonder pageleave telt het bezoek niet mee in de gemiddelde tijd, en markeer je het.
- Bots herken je aan de user agent. Ze tellen niet mee in de statistieken, maar je toont het aantal apart.
- Pages.png groepeert URL's tot routes, zoals /products/:slug. Stel voor hoe we dat doen, bijvoorbeeld id's en slugs herkennen plus eigen patronen per project.
- Maak samenvattingen per uur in hourly_stats. Het dashboard leest alleen die samenvattingen. Test met minstens een miljoen events dat het snel blijft, en zet de meetresultaten in je verslag.
- Bouw Pages.png en vul de kolommen voor pageviews in All projects.png. De kolom Errors koppelt fouten aan de pagina waar ze gebeurden.
- Maak een demo in examples/ met posthog-js zonder cookies, zoals het concept adviseert.
````

### Fase 4: versie 2

````text
Lees CLAUDE.md en concept-monitoring-tool.md, en bekijk Issue detail.png. We doen fase 4: versie 2. Klaar als we bij een fout de pagina's ervoor zien. Maak eerst een plan en wacht op mijn akkoord.

- Gebruikersreis: "Before this error" in Issue detail.png. Koppel Sentry en PostHog via dezelfde user id, en laat zien welke pagina's iemand zag vóór de fout, met de tijd per pagina. Maak ook "See all activity of this user".
- Custom events: opslaan en tonen onder Events, met een grafiek. Er is nog geen ontwerp, dus doe een voorstel in de stijl van de andere schermen.
- Releases: sla de release per fout op, en laat zien in welke versie een issue begon.
- Source maps via de upload-API van Sentry, zodat sentry-cli en @sentry/vite-plugin gewoon werken. Zoek uit welke endpoints die gebruiken (chunk-upload, artifact bundles en debug ID's), en test met de echte tools. Maak daarna JavaScript-fouten leesbaar in de stack trace.
````

### Fase 5: delen met anderen

````text
Lees CLAUDE.md en concept-monitoring-tool.md. We doen fase 5: delen met anderen. Klaar als iemand anders de tool in 10 minuten draait. Maak eerst een plan en wacht op mijn akkoord.

- Een handleiding: installeren met docker compose, HTTPS met een voorbeeld voor Caddy, alle instellingen, updaten, backup en terugzetten, en welke SDK-versies getest zijn.
- Backup en terugzetten in Settings.
- Postgres als optie via DATABASE_URL. Draai dezelfde tests tegen SQLite en tegen Postgres.
- Alle data van één gebruiker wissen via de user id, voor de AVG.
- Een publieke image voor amd64 en arm64, met een CI-workflow die test en de image bouwt.
- Test de 10 minuten echt: volg de handleiding stap voor stap in een schone omgeving, en noteer waar je vastliep.
````
