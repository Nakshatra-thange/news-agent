#!/usr/bin/env bash
# Bootstraps Synergy's local PostgreSQL: creates the `synergy` login role and
# the `synergy` and `synergy_test` databases (owned by that role), then writes
# DATABASE_URL and TEST_DATABASE_URL into the git-ignored .env file.
#
# Safe to re-run: existing objects are kept, and an application password
# already present in .env is reused rather than rotated.
#
# Superuser authentication: psql prompts for the superuser password on your
# terminal (once), or uses PGPASSWORD / ~/.pgpass if set. The password is
# never stored or printed by this script.
#
# Environment overrides: PGHOST (localhost), PGPORT (5432),
# PG_SUPERUSER (postgres), PSQL (path to psql).
set -euo pipefail

cd "$(dirname "$0")/.."

PGHOST="${PGHOST:-localhost}"
PGPORT="${PGPORT:-5432}"
PG_SUPERUSER="${PG_SUPERUSER:-postgres}"
APP_ROLE="synergy"
APP_DB="synergy"
TEST_DB="synergy_test"
ENV_FILE=".env"

PSQL="${PSQL:-$(command -v psql || true)}"
if [[ -z "$PSQL" && -x /Library/PostgreSQL/17/bin/psql ]]; then
  PSQL=/Library/PostgreSQL/17/bin/psql
fi
if [[ -z "$PSQL" ]]; then
  echo "error: psql not found; set PSQL=/path/to/psql" >&2
  exit 1
fi

# Reuse the app password from an existing .env so re-runs don't break it.
APP_PW=""
if [[ -f "$ENV_FILE" ]]; then
  APP_PW="$(sed -n "s|^DATABASE_URL=postgres://${APP_ROLE}:\([^@]*\)@.*|\1|p" "$ENV_FILE" | head -n1)"
fi
if [[ -z "$APP_PW" || "$APP_PW" == "CHANGE_ME" ]]; then
  APP_PW="$(openssl rand -hex 24)" # hex: URL-safe, no quoting issues
fi

echo "Connecting to PostgreSQL at ${PGHOST}:${PGPORT} as superuser '${PG_SUPERUSER}'..."

# Everything runs in ONE psql session (one password prompt). Values are passed
# via stdin \set lines, never argv, so the app password is not visible in the
# process list. -q keeps psql from echoing statements.
{
  printf '\\set app_role %s\n' "$APP_ROLE"
  printf '\\set app_db %s\n' "$APP_DB"
  printf '\\set test_db %s\n' "$TEST_DB"
  printf "\\\\set app_pw '%s'\n" "$APP_PW"
  cat <<'SQL'
\set ON_ERROR_STOP on
SELECT format('CREATE ROLE %I LOGIN', :'app_role')
 WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'app_role') \gexec
ALTER ROLE :"app_role" WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD :'app_pw';
SELECT format('CREATE DATABASE %I OWNER %I', :'app_db', :'app_role')
 WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = :'app_db') \gexec
SELECT format('CREATE DATABASE %I OWNER %I', :'test_db', :'app_role')
 WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = :'test_db') \gexec
ALTER DATABASE :"app_db" OWNER TO :"app_role";
ALTER DATABASE :"test_db" OWNER TO :"app_role";
SQL
} | "$PSQL" -X -q -h "$PGHOST" -p "$PGPORT" -U "$PG_SUPERUSER" -d postgres >/dev/null

echo "Role '${APP_ROLE}' and databases '${APP_DB}', '${TEST_DB}' are ready."

# Write the URLs into .env (created from .env.example if missing), replacing
# any existing (or commented-out) definitions.
if [[ ! -f "$ENV_FILE" ]]; then
  cp .env.example "$ENV_FILE"
fi
chmod 600 "$ENV_FILE"
tmp="$(mktemp)"
grep -vE '^#? ?(TEST_)?DATABASE_URL=' "$ENV_FILE" >"$tmp" || true
{
  cat "$tmp"
  echo "DATABASE_URL=postgres://${APP_ROLE}:${APP_PW}@${PGHOST}:${PGPORT}/${APP_DB}?sslmode=disable"
  echo "TEST_DATABASE_URL=postgres://${APP_ROLE}:${APP_PW}@${PGHOST}:${PGPORT}/${TEST_DB}?sslmode=disable"
} >"$ENV_FILE"
rm -f "$tmp"

# Verify the application role can log in to both databases.
for db in "$APP_DB" "$TEST_DB"; do
  PGPASSWORD="$APP_PW" "$PSQL" -X -q -At -h "$PGHOST" -p "$PGPORT" -U "$APP_ROLE" -d "$db" \
    -c "SELECT 'login ok: ' || current_user || '@' || current_database()"
done
echo "Wrote DATABASE_URL and TEST_DATABASE_URL to ${ENV_FILE} (password not shown)."
