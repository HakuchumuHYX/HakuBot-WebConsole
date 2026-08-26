#!/usr/bin/env bash
set -euo pipefail

environment_file=/etc/hermes-dashboard.env
hermes_python=${HERMES_PYTHON:-/usr/local/lib/hermes-agent/venv/bin/python}
public_url=${1:-}

if [[ ${EUID} -ne 0 ]]; then
  echo "Run this script as root." >&2
  exit 1
fi
if [[ ! -x ${hermes_python} ]]; then
  echo "Hermes Python runtime not found: ${hermes_python}" >&2
  exit 1
fi
if [[ -z ${public_url} ]]; then
  read -r -p "Public Hermes URL (for example https://console.example:54321/hermes): " public_url
fi
if [[ ! ${public_url} =~ ^https://[^[:space:]/]+(:[0-9]+)?/hermes$ ]]; then
  echo "The public URL must be an HTTPS origin ending in /hermes." >&2
  exit 1
fi

read -r -p "Hermes Dashboard username: " username
if [[ ! ${username} =~ ^[A-Za-z0-9._@-]{1,64}$ ]]; then
  echo "Username may contain only letters, digits, dot, underscore, @, and hyphen." >&2
  exit 1
fi
password_hash=$("${hermes_python}" -c '
import getpass
from plugins.dashboard_auth.basic import hash_password
password = getpass.getpass("Hermes Dashboard password: ")
confirm = getpass.getpass("Confirm password: ")
if not password or password != confirm:
    raise SystemExit("Passwords are empty or do not match.")
print(hash_password(password))
')
session_secret=$("${hermes_python}" -c 'import secrets; print(secrets.token_urlsafe(32))')

umask 077
tmp_file=$(mktemp "${environment_file}.XXXXXX")
trap 'rm -f "${tmp_file}"' EXIT
{
  printf 'HERMES_DASHBOARD_PUBLIC_URL=%s\n' "${public_url}"
  printf 'HERMES_DASHBOARD_BASIC_AUTH_USERNAME=%s\n' "${username}"
  printf 'HERMES_DASHBOARD_BASIC_AUTH_PASSWORD_HASH=%s\n' "${password_hash}"
  printf 'HERMES_DASHBOARD_BASIC_AUTH_SECRET=%s\n' "${session_secret}"
} >"${tmp_file}"
install -o root -g root -m 0600 "${tmp_file}" "${environment_file}"

echo "Wrote ${environment_file} with a password hash and session secret; no plaintext password was saved."
