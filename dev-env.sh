# shellcheck shell=bash
# Sourced by the run*.sh scripts. config/config.env may hold 1Password secret
# references (op://vault/item/field). `op run` resolves them with your own
# 1Password account (desktop app, Touch ID) and docker receives each variable
# by name from that environment, so resolved values are never written to disk.
DEV_ENV_FILE=./config/config.env

if [[ -n "${OP_SERVICE_ACCOUNT_TOKEN:-}" ]]; then
  echo "dev-env: unset OP_SERVICE_ACCOUNT_TOKEN; dev resolves secrets with your own 1Password account" >&2
  exit 1
fi

# -e NAME for every variable in the env file (names never contain spaces).
# shellcheck disable=SC2034 # used by the run scripts that source this file
DOCKER_ENV_FLAGS=$(sed -nE 's/^([A-Za-z_][A-Za-z0-9_]*)=.*/-e \1/p' "$DEV_ENV_FILE")

# dev_compose_override FILE writes a compose override that passes every env-file
# variable through by name to each service in docker-compose.dev.yml. It holds
# names only, no values.
dev_compose_override() {
  local svc
  {
    echo "services:"
    for svc in $(docker compose -f docker-compose.dev.yml config --services); do
      printf '  %s:\n    environment:\n' "$svc"
      sed -nE 's/^([A-Za-z_][A-Za-z0-9_]*)=.*/      - \1/p' "$DEV_ENV_FILE"
    done
  } >"$1"
}
