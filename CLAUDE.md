# CLAUDE.md – PatchMon-Fork von Sebastian

Dieses Dokument fasst den Stand und die Arbeitsweise für diesen Fork zusammen. Bitte vor Änderungen, Merges oder Builds lesen.

## 1. Überblick

- **Fork** von `PatchMon/PatchMon` (Go-Backend + React-Frontend + Go-Agent, AGPL-3.0). Dieser Fork liegt unter `github.com/carfanatic93/PatchMon`.
- **Remotes:** `origin` = eigener Fork (HTTPS), `upstream` = Original. Der Push zu `upstream` ist deaktiviert (Push-URL `DISABLED`). **Es darf nie etwas ans Original gehen.**
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

Kleine Edits in bestehenden Dateien (hier können Konflikte entstehen):
- `internal/handler/patching.go` (Feld `schedules`), `internal/server/router.go` (Store + 6 Routen), `internal/queue/server.go` (Queue `patch-schedules`, Mux-Handler, Scheduler-Eintrag `* * * * *`), `internal/queue/jobs.go` (Konstanten)
- `internal/sqlc/schema/schema.sql`, `internal/db/models.go`, `internal/db/querier.go`
- `frontend/src/pages/Patching.jsx` (Tab + URL-Sync-Effekt inkl. `schedules`), `frontend/src/components/Layout.jsx` (Menüpunkt), `frontend/src/utils/patchingApi.js`

## 3. Sicherheits-Updates der Abhängigkeiten

Gepatcht: `golang.org/x/crypto` → 0.56.0 (Server und Agent), `golang.org/x/mod` → 0.40.0, `npm audit fix`, `vitest`/`@vitest/ui` → 4.1.11, `react-router-dom` → 6.30.6.
Bewusst offen: `react-router`-Advisories (Fix nur in Major 7, Projekt ist reine SPA ohne SSR), GO-2026-5932 (kein Fix, wird nicht aufgerufen). Der Hook `npm audit --audit-level=high` läuft damit grün.

## 4. Deployment der Instanz (Proxmox-LXC, **kein Docker**)

- Instanz: Proxmox-LXC aus den Community-Scripts (Debian 13, x86_64), Container 108 `patchmon`, IP 192.168.177.144.
- Installation: `/opt/patchmon/` mit `patchmon-server` (Binary), `agents/`, `ssg-content/`; systemd-Service **`patchmon-server`**.
- **Das Community-Update-Script nicht benutzen**, es ersetzt das Binary durch die Originalversion und entfernt die Zeitpläne.
- Claude hat **keinen SSH-Zugriff** auf den Container. Sebastian führt `scp` und die Installation selbst aus.

### Binary bauen (linux/amd64, Frontend eingebettet)

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

### Einspielen (macht Sebastian)

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

Aktueller Stand zum Zeitpunkt dieser Notiz: Upstream wurde am 2026-10-16 gemerged, in `origin` gepusht und vom Benutzer per Binary-Tausch auf der Instanz eingespielt (lief ohne Fehler).

## 6. Lokale Entwicklungsumgebung

- Werkzeuge per Homebrew installiert: `node`, `go`, `golangci-lint`, `govulncheck`. Docker Desktop wird für sqlc, Integrationstests und den Dev-Stack genutzt (muss ggf. erst gestartet werden: `open -a Docker`).
- **Git-Hooks (lefthook)** laufen beim Commit: Biome, Frontend-Tests, Agent-Check, Server-Check (`make check`: fmt, vet, golangci-lint, Tests), `vuln-check` (`npm audit --audit-level=high` + `govulncheck`). Die Hooks nicht umgehen; `LEFTHOOK_EXCLUDE=vuln-check` nur nach ausdrücklichem Ok.
- Der Server-Check braucht ein gebautes Frontend unter `server-source-code/cmd/server/static/frontend/dist` (gitignored).
- Agent-Binaries für Docker-Builds: `make build-all-for-docker` in `agent-source-code` (landet in gitignoriertem `agents-prebuilt/`). Das Produktions-Image erzeugt `docker/build.sh`.
- Dev-Stack: `docker compose -f docker/docker-compose.dev.yml up -d --build` (nur Entwicklung, feste Zugangsdaten, Daten in `docker/compose_dev_data`, jetzt gelöscht). Läuft dann auf http://localhost:3000.
- Integrationstest der Zeitpläne: Postgres per Docker starten und `PATCHMON_TEST_DATABASE_URL=postgres://... go test ./internal/queue -run TestPatchScheduleDispatch -v` setzen.
- Aufräumen nach Builds: `agent-source-code/build`, `agents-prebuilt`, `agents`, `server-prebuilt`, `frontend/dist` und das eingebettete `static/frontend/dist` sind gitignoriert und regenerierbar; `node_modules` bleibt (Hooks brauchen es).

## 7. Arbeitsregeln für Claude in diesem Projekt

- Nichts ans Original senden. Pushen macht Sebastian selbst per HTTPS mit Personal Access Token; vorher fragen, bevor etwas gepusht wird.
- Software installieren, Dateien löschen oder Hooks entfernen nur nach ausdrücklichem Ok.
- Commits nur auf Wunsch, auf Feature-Branch, mit Zeile `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>` am Ende.
- Die Oberfläche ist englisch, die Kommunikation mit Sebastian deutsch.
- Kleine, gut abgegrenzte Änderungen bevorzugen, damit künftige Upstream-Merges einfach bleiben.
- Memory-Datei `patchmon-fork-workflow.md` im Claude-Memory-Ordner enthält dieselben Eckdaten in Kurzform.
