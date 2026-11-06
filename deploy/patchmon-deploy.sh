#!/bin/bash
# Restricted deploy entry point for the PatchMon LXC (fork-only, not upstream).
#
# Install on the PatchMon container as /usr/local/sbin/patchmon-deploy (root:root, 0755)
# and bind the deploy key to it in /root/.ssh/authorized_keys:
#
#   command="/usr/local/sbin/patchmon-deploy",restrict ssh-ed25519 AAAA... claude-lxc-patchmon-deploy
#
# The key can then run only these actions (passed as the SSH command):
#   deploy <sha256>             read the new server binary from stdin, verify, back up, install, restart
#   rollback                    restore patchmon-server.bak and restart
#   status                      service state and checksums of current and backup server binary
#   logs [n]                    last n journal lines (default 50, max 1000)
#   backup-db                   pg_dump into /root/patchmon-db-backups (keeps the last 10)
#   agent <name> <sha256>       read one agent binary from stdin into the agents directory
#   agents                      checksums of the agent binaries in the agents directory
set -euo pipefail

BIN=/opt/patchmon/patchmon-server
ENV_FILE=/opt/patchmon/.env
AGENTS_DIR=/opt/patchmon/agents
SVC=patchmon-server
BACKUP_DIR=/root/patchmon-db-backups
MAX_SIZE=$((200 * 1024 * 1024))

read -r action arg1 arg2 extra <<<"${SSH_ORIGINAL_COMMAND:-}" || true
[ -z "${extra:-}" ] || { echo "too many arguments" >&2; exit 2; }

restart_and_report() {
	systemctl start "$SVC"
	sleep 5
	if systemctl is-active --quiet "$SVC"; then
		echo "service: active"
	else
		echo "service: NOT active" >&2
	fi
	journalctl -u "$SVC" -n 30 --no-pager
	systemctl is-active --quiet "$SVC"
}

case "${action:-}" in
deploy)
	[[ "${arg1:-}" =~ ^[0-9a-f]{64}$ ]] && [ -z "${arg2:-}" ] || { echo "usage: deploy <sha256>" >&2; exit 2; }
	tmp="$(mktemp /tmp/patchmon-server.XXXXXX)"
	trap 'rm -f "$tmp"' EXIT
	head -c "$((MAX_SIZE + 1))" >"$tmp"
	[ "$(stat -c %s "$tmp")" -le "$MAX_SIZE" ] || { echo "binary too large" >&2; exit 1; }
	echo "$arg1  $tmp" | sha256sum -c --quiet - || { echo "checksum mismatch, nothing changed" >&2; exit 1; }
	cp -p "$BIN" "$BIN.bak"
	systemctl stop "$SVC"
	install -m 755 "$tmp" "$BIN"
	echo "installed $arg1"
	restart_and_report
	;;
rollback)
	[ -f "$BIN.bak" ] || { echo "no backup at $BIN.bak" >&2; exit 1; }
	systemctl stop "$SVC"
	install -m 755 "$BIN.bak" "$BIN"
	echo "rolled back to $(sha256sum "$BIN" | cut -d' ' -f1)"
	restart_and_report
	;;
status)
	systemctl status "$SVC" --no-pager || true
	sha256sum "$BIN" "$BIN.bak" 2>/dev/null || true
	;;
logs)
	n="${arg1:-50}"
	[[ "$n" =~ ^[0-9]{1,4}$ ]] && [ "$n" -le 1000 ] || { echo "usage: logs [1-1000]" >&2; exit 2; }
	journalctl -u "$SVC" -n "$n" --no-pager
	;;
backup-db)
	db_url="$(grep -E '^DATABASE_URL=' "$ENV_FILE" | head -n1 | cut -d= -f2- | tr -d "\"'")"
	[ -n "$db_url" ] || { echo "DATABASE_URL not found in $ENV_FILE" >&2; exit 1; }
	# pg_dump rejects the Prisma-style ?schema= parameter
	db_url="$(printf '%s' "$db_url" | sed -E 's/([?&])schema=[^&]*&?/\1/; s/[?&]$//')"
	mkdir -p -m 700 "$BACKUP_DIR"
	out="$BACKUP_DIR/patchmon-$(date +%Y%m%d-%H%M%S).sql.gz"
	pg_dump --no-owner "$db_url" | gzip >"$out"
	ls -1t "$BACKUP_DIR"/patchmon-*.sql.gz | tail -n +11 | xargs -r rm -f
	ls -lh "$out"
	;;
agent)
	[[ "${arg1:-}" =~ ^patchmon-agent-(linux|freebsd)-(amd64|386|arm64|arm)$|^patchmon-agent-windows-(amd64|arm64)\.exe$ ]] \
		|| { echo "usage: agent <name> <sha256>" >&2; exit 2; }
	[[ "${arg2:-}" =~ ^[0-9a-f]{64}$ ]] || { echo "usage: agent <name> <sha256>" >&2; exit 2; }
	[ -d "$AGENTS_DIR" ] || { echo "$AGENTS_DIR does not exist" >&2; exit 1; }
	dst="$AGENTS_DIR/$arg1"
	tmp="$(mktemp "$AGENTS_DIR/.$arg1.XXXXXX")"
	trap 'rm -f "$tmp"' EXIT
	head -c "$((MAX_SIZE + 1))" >"$tmp"
	[ "$(stat -c %s "$tmp")" -le "$MAX_SIZE" ] || { echo "binary too large" >&2; exit 1; }
	echo "$arg2  $tmp" | sha256sum -c --quiet - || { echo "checksum mismatch, nothing changed" >&2; exit 1; }
	chmod 755 "$tmp"
	[ -f "$dst" ] && cp -p "$dst" "$dst.bak"
	mv -f "$tmp" "$dst"
	echo "installed $arg1 $arg2"
	;;
agents)
	sha256sum "$AGENTS_DIR"/patchmon-agent-* 2>/dev/null || echo "no agent binaries in $AGENTS_DIR"
	;;
*)
	echo "allowed: deploy <sha256> | rollback | status | logs [n] | backup-db | agent <name> <sha256> | agents" >&2
	exit 2
	;;
esac
