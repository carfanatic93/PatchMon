# CLAUDE.md – PatchMon-Fork von Sebastian

Dieses Dokument fasst den Stand und die Arbeitsweise für diesen Fork zusammen. Bitte vor Änderungen, Merges oder Builds lesen.

## 1. Überblick

- **Fork** von `PatchMon/PatchMon` (Go-Backend + React-Frontend + Go-Agent, AGPL-3.0). Dieser Fork liegt unter `github.com/carfanatic93/PatchMon`.
- **Remotes:** `origin` = eigener Fork, `upstream` = Original. Der Push zu `upstream` ist deaktiviert (Push-URL `DISABLED`, in jedem neuen Klon prüfen: `git remote -v`). **Es darf nie etwas ans Original gehen.**
  - Auf dem Mac: `origin` per HTTPS mit Personal Access Token.
  - Auf dem Claude-LXC: `origin` = `git@github-patchmon:carfanatic93/PatchMon.git` (SSH-Alias mit Deploy-Key `~/.ssh/id_ed25519_patchmon`).
- Gearbeitet wird auf `main` des Forks (Feature-Branches werden gemerged). `main` verfolgt `origin/main`.
- **Zweck des Forks:** wiederkehrende, automatische Update-Läufe ("jeden Sonntag um 12 Uhr fällige Updates installieren").

## 2. Was der Fork ergänzt: Recurring Patch Schedules

Zeitpläne stehen unter **Patching → Schedules** (`/patching?tab=schedules`, auch in der Seitenleiste).

- Wiederholung: wöchentlich/täglich/monatlich mit Uhrzeit oder eigener 5-Feld-Cron-Ausdruck (keine `@every`-Deskriptoren, keine 6 Felder). Zeitzone als IANA-Name, Standard = Organisations-Zeitzone. Vorschau der nächsten Läufe.
- Ziel: alle Hosts, Host-Gruppen oder einzelne Hosts.
- Ablauf: Ein Job läuft **jede Minute** und prüft fällige Zeitpläne. Pro Ziel-Host wird ein `patch_all`-Lauf angelegt (Policy-Name `Schedule: <Name>`) und sofort eingereiht. Übersprungen werden: Hosts ohne offene Updates, Hosts mit queued/running/pending_approval-Lauf, inaktive Hosts, Windows-Hosts.
- Atomares Claimen: `UPDATE ... WHERE next_run_at = <alter Wert>` stellt sicher, dass ein Zeitplan pro Fälligkeit höchstens einmal läuft.
- **Misfire-Grace 1 h:** Ist ein Lauf mehr als 1 h überfällig (z. B. Serverausfall), wird er übersprungen und der Zeitplan springt zum nächsten Termin.
- "Run now" führt sofort aus, ohne `next_run_at` zu ändern. Jeder Zeitplan speichert das Ergebnis des letzten Laufs (queued / up to date / busy / failed).
- Rechte: Verwalten `can_manage_patching`, Ansehen `can_view_hosts`; Modul `patching_policies` (im Self-Hosting ohne Einschränkung freigeschaltet).
- API: `GET/POST /api/v1/patching/schedules`, `PUT/DELETE /api/v1/patching/schedules/{id}`, `POST /api/v1/patching/schedules/{id}/run-now`, `GET /api/v1/patching/schedules/preview?cron_expr=&timezone=`.
- Nicht umgesetzt: "nur Security-Updates" (bräuchte Agent-Änderung, `patch_all` installiert alles).

### Berührte Dateien (Konfliktstellen bei Upstream-Merges)

Neu (konfliktfrei): 
- `server-source-code/internal/migrate/migrations/000059_v2-1-5_patch_schedules.{up,down}.sql` (ursprünglich `000049`, nach Nummernkonflikt mit Upstream umbenannt)
- `server-source-code/internal/queue/patch_schedule_worker.go` (+ `_test.go`, Integrationstest nur mit `PATCHMON_TEST_DATABASE_URL`)
- `server-source-code/internal/handler/patch_schedules.go` (+ `_test.go`)
- `server-source-code/internal/store/patch_schedules.go`
- `server-source-code/internal/sqlc/queries/patch_schedules.sql`, `internal/db/patch_schedules.sql.go`
- `frontend/src/components/patching/SchedulesTab.jsx`
- Doku-Abschnitt "Recurring Patch Schedules" in `docs/patchmon-admin-guide.md`
- `deploy/patchmon-deploy.sh` (Deploy-Script für die Instanz, siehe Abschnitt 4)

Kleine Edits in bestehenden Dateien (hier können Konflikte entstehen):
- `internal/handler/patching.go` (Feld `schedules`), `internal/server/router.go` (Store + 6 Routen), `internal/queue/server.go` (Queue `patch-schedules`, Mux-Handler, Scheduler-Eintrag `* * * * *`), `internal/queue/jobs.go` (Konstanten)
- `internal/sqlc/schema/schema.sql`, `internal/db/models.go`, `internal/db/querier.go`
- `frontend/src/pages/Patching.jsx` (Tab + URL-Sync-Effekt inkl. `schedules`), `frontend/src/components/Layout.jsx` (Menüpunkt), `frontend/src/utils/patchingApi.js`

## 3. Sicherheits-Updates der Abhängigkeiten

Gepatcht: Go → 1.26.10 in beiden `go.mod` (GO-2026-6809 in `crypto/x509`; die CI nimmt die Go-Version aus `go.mod`), `golang.org/x/crypto` → 0.56.0 (Server und Agent), `golang.org/x/mod` → 0.40.0, `npm audit fix`, `vitest`/`@vitest/ui` → 4.1.11, `react-router-dom` → 6.30.6.
Bewusst offen: `react-router`-Advisories (Fix nur in Major 7, Projekt ist reine SPA ohne SSR), GO-2026-5932 (kein Fix, wird nicht aufgerufen). Der Hook `npm audit --audit-level=high` läuft damit grün.

## 4. Deployment der Instanz (Proxmox-LXC, **kein Docker**)

- Instanz: Proxmox-LXC aus den Community-Scripts (Debian 13, x86_64), Container 108 `patchmon`, IP 192.168.177.144.
- Installation: `/opt/patchmon/` mit `patchmon-server` (Binary), `agents/`, `ssg-content/`; systemd-Service **`patchmon-server`**.
- **Das Community-Update-Script nicht benutzen**, es ersetzt das Binary durch die Originalversion und entfernt die Zeitpläne.
- Claude arbeitet auf einem eigenen LXC **`claude-code` (192.168.177.143)** und spielt über einen eingeschränkten SSH-Schlüssel selbst ein (siehe unten). Eine Root-Shell auf 108 hat Claude nicht.
- Setup-Befehle für 108 immer mit `hostname` gegenprüfen (muss `patchmon` sein, nicht `claude-code`).

### Binary bauen auf dem Claude-LXC (linux/amd64, Frontend eingebettet)

Werkzeuge liegen benutzerlokal (kein passwortloses sudo): Go in `~/.local/go`, Node 22 in `~/.local/node`, `golangci-lint` in `~/.local/bin`, `govulncheck` in `~/go/bin`. `make` und `gh` sind per apt installiert.

```bash
cd /home/claude/projekte/patchmon-fork
export PATH="$HOME/.local/go/bin:$HOME/go/bin:$HOME/.local/node/bin:$HOME/.local/bin:$PATH"
VERSION=$(git describe --tags --abbrev=0 | sed 's/^v//')   # nicht niedriger als die Version der Instanz
npm run build --workspace=frontend
rm -rf server-source-code/cmd/server/static/frontend/dist
mkdir -p server-source-code/cmd/server/static/frontend
cp -r frontend/dist server-source-code/cmd/server/static/frontend/
mkdir -p server-prebuilt
(cd server-source-code && GOTOOLCHAIN=local GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false \
  -ldflags="-s -w -X github.com/PatchMon/PatchMon/server-source-code/internal/config.DefaultVersion=${VERSION}" \
  -o ../server-prebuilt/patchmon-server-linux-amd64 ./cmd/server)
sha256sum server-prebuilt/patchmon-server-linux-amd64
govulncheck -mode=binary server-prebuilt/patchmon-server-linux-amd64
```

### Einspielen vom Claude-LXC (eingeschränkter Deploy-Schlüssel)

Auf 108 liegt `deploy/patchmon-deploy.sh` als `/usr/local/sbin/patchmon-deploy`. Der Schlüssel `~/.ssh/id_ed25519_patchmon_deploy` ist in `/root/.ssh/authorized_keys` per `command="/usr/local/sbin/patchmon-deploy",restrict` daran gebunden und kann nur diese Aktionen ausführen:

```bash
DEPLOY="ssh -i $HOME/.ssh/id_ed25519_patchmon_deploy -o IdentitiesOnly=yes root@192.168.177.144"
$DEPLOY status                     # Dienststatus, Prüfsummen von Binary und .bak
$DEPLOY backup-db                  # pg_dump nach /root/patchmon-db-backups (die letzten 10 bleiben)
F=server-prebuilt/patchmon-server-linux-amd64
$DEPLOY "deploy $(sha256sum $F | cut -d' ' -f1)" < $F   # prüft Prüfsumme, legt .bak an, installiert, startet neu, zeigt Log
$DEPLOY "logs 100"                 # journalctl des Dienstes (max. 1000 Zeilen)
$DEPLOY rollback                   # .bak zurückspielen und neu starten
```

Ablauf bei einem Update: `backup-db` → `deploy` → Log prüfen (Migration, `service: active`, Agents verbinden sich). Ändert sich `deploy/patchmon-deploy.sh`, muss Sebastian es auf 108 neu installieren.

### Binary bauen auf dem Mac (alter Weg)

```bash
cd /Users/sebastian/GitProjekte/patchmon-fork
export PATH=/opt/homebrew/bin:$PATH
VERSION=<Version>   # nicht niedriger als die Version, die die Instanz zeigt (git describe --tags --abbrev=0)
npm run build --workspace=frontend
rm -rf server-source-code/cmd/server/static/frontend/dist
mkdir -p server-source-code/cmd/server/static/frontend
cp -r frontend/dist server-source-code/cmd/server/static/frontend/
mkdir -p server-prebuilt
(cd server-source-code && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false \
  -ldflags="-s -w -X github.com/PatchMon/PatchMon/server-source-code/internal/config.DefaultVersion=${VERSION}" \
  -o ../server-prebuilt/patchmon-server-linux-amd64 ./cmd/server)
shasum -a 256 server-prebuilt/patchmon-server-linux-amd64
```

Das Binary liegt in `server-prebuilt/` (gitignored). Die Datei danach nicht committen.

### Einspielen von Hand (macht Sebastian, alter Weg)

```bash
scp server-prebuilt/patchmon-server-linux-amd64 root@192.168.177.144:/tmp/patchmon-server.new
# auf dem Container:
sha256sum /tmp/patchmon-server.new          # mit Prüfsumme vergleichen
cp /opt/patchmon/patchmon-server /opt/patchmon/patchmon-server.bak
systemctl stop patchmon-server
install -m 755 /tmp/patchmon-server.new /opt/patchmon/patchmon-server
systemctl start patchmon-server
journalctl -u patchmon-server -n 30 --no-pager   # Migration "applied successfully" prüfen
```

Rollback: Dienst stoppen, `patchmon-server.bak` zurückkopieren, starten. Bei neuen Migrationen vorher `pg_dump` der Datenbank machen (`DATABASE_URL` steht in der `.env` unter `/opt/patchmon`).

## 5. Updates vom Original holen

Wenn Sebastian sagt "hol das Update vom Original":

1. `git fetch upstream` und `git merge upstream/main` (neuen Branch dafür anlegen ist sinnvoll).
2. Konflikte in den oben gelisteten Dateien lösen. **Migrationsnummer prüfen:** meine Migration ist `000059`. Bringt Upstream dieselbe Nummer oder höher, auf die nächste freie umbenennen (`ls server-source-code/internal/migrate/migrations`) und `schema.sql` abgleichen.
3. Bei Konflikten in `go.mod`/`go.sum`/`package-lock.json`: Upstreams Version nehmen, dann `go mod tidy` bzw. `npm install` und `npm audit fix`.
4. **sqlc ist lokal nicht installiert.** Es lief bisher per Docker: `docker run --rm -v "$PWD":/src -w /src sqlc/sqlc:1.30.0 generate` im Ordner `server-source-code`. Falls Docker nicht läuft, `querier.go`/`*.sql.go` von Hand anpassen.
5. Prüfen: Go-Tests, `golangci-lint`, Frontend-Tests, danach Binary bauen (Abschnitt 4).
6. Version prüfen: Der Fork-Stand wurde zuletzt auf Upstream-Tag **v2.1.6** gebracht (Stand dieser Notiz); die eingebettete Version muss zur laufenden Instanz passen.

Aktueller Stand zum Zeitpunkt dieser Notiz: Upstream wurde am 2026-10-16 gemerged und eingespielt. Am 2026-10-22 wurde das Binary mit Go 1.26.10 (Version 2.1.6) vom Claude-LXC per Deploy-Schlüssel auf der Instanz eingespielt (lief ohne Fehler).

## 6. Lokale Entwicklungsumgebung

- **Claude-LXC** (`claude-code`, Debian 12): Werkzeuge siehe Abschnitt 4. Kein Docker und kein sqlc, daher dort keine sqlc-Generierung, kein Integrationstest und kein Dev-Stack. `gh` ist als `carfanatic93` angemeldet (Token: Actions read/write, Contents read-only) für Workflow-Logs und Läufe. Auf 2 CPUs kann der Frontend-Test `HostsUrlFilters` im Commit-Hook über sein 5-s-Timeout laufen. Dann den Commit einfach wiederholen, nicht den Hook umgehen.
- **Mac:** Werkzeuge per Homebrew installiert: `node`, `go`, `golangci-lint`, `govulncheck`. Docker Desktop wird für sqlc, Integrationstests und den Dev-Stack genutzt (muss ggf. erst gestartet werden: `open -a Docker`).
- **Git-Hooks (lefthook)** laufen beim Commit: Biome, Frontend-Tests, Agent-Check, Server-Check (`make check`: fmt, vet, golangci-lint, Tests), `vuln-check` (`npm audit --audit-level=high` + `govulncheck`). Die Hooks nicht umgehen; `LEFTHOOK_EXCLUDE=vuln-check` nur nach ausdrücklichem Ok.
- Der Server-Check braucht ein gebautes Frontend unter `server-source-code/cmd/server/static/frontend/dist` (gitignored).
- Agent-Binaries für Docker-Builds: `make build-all-for-docker` in `agent-source-code` (landet in gitignoriertem `agents-prebuilt/`). Das Produktions-Image erzeugt `docker/build.sh`.
- Dev-Stack: `docker compose -f docker/docker-compose.dev.yml up -d --build` (nur Entwicklung, feste Zugangsdaten, Daten in `docker/compose_dev_data`, jetzt gelöscht). Läuft dann auf http://localhost:3000.
- Integrationstest der Zeitpläne: Postgres per Docker starten und `PATCHMON_TEST_DATABASE_URL=postgres://... go test ./internal/queue -run TestPatchScheduleDispatch -v` setzen.
- Aufräumen nach Builds: `agent-source-code/build`, `agents-prebuilt`, `agents`, `server-prebuilt`, `frontend/dist` und das eingebettete `static/frontend/dist` sind gitignoriert und regenerierbar; `node_modules` bleibt (Hooks brauchen es).

## 7. Arbeitsregeln für Claude in diesem Projekt

- Nichts ans Original senden. Claude darf Feature-Branches selbst nach `origin` pushen (seit 2026-10-22), aber nie nach `upstream`. Das Mergen in `main` macht Sebastian.
- Einspielen auf der Instanz nur über den Deploy-Schlüssel (Abschnitt 4), vorher `backup-db`.
- Der Workflow „Build and Push Docker Images“ ist im Fork auf GitHub deaktiviert (Fork hat keine Tags, kein Docker-Einsatz). Die Workflow-Datei nicht ändern, sonst Merge-Konflikte.
- Software installieren, Dateien löschen oder Hooks entfernen nur nach ausdrücklichem Ok.
- Commits nur auf Wunsch, auf Feature-Branch, mit Zeile `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>` am Ende.
- Die Oberfläche ist englisch, die Kommunikation mit Sebastian deutsch.
- Kleine, gut abgegrenzte Änderungen bevorzugen, damit künftige Upstream-Merges einfach bleiben.
- Memory-Datei `patchmon-fork-workflow.md` im Claude-Memory-Ordner enthält dieselben Eckdaten in Kurzform (nur auf dem Mac; auf dem Claude-LXC heißt sie `lxc-toolchain.md`).
