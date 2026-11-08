#!/usr/bin/env bash
# Один замер: сбрасывает pg_stat_statements, запускает k6, сохраняет отчёт и самые дорогие запросы.
#
# Постоянная нагрузка:  loadtest/run.sh search --rate 50 --duration 5m --label search-before
# Поиск насыщения:      loadtest/run.sh search --stages 50:1m,100:1m,200:1m --label search-ramp
#
# BASE_URL задаёт адрес шлюза, если k6 запущен на другой машине.
# PGHOST_CMD задаёт команду psql, если база на другой машине.
set -euo pipefail

scenario="${1:?Укажи сценарий: search, polling или login}"
shift
rate=20 duration=5m stages="" label="$scenario-run"
while (($#)); do
  case "$1" in
    --rate) rate="$2"; shift 2 ;;
    --duration) duration="$2"; shift 2 ;;
    --stages) stages="$2"; shift 2 ;;
    --label) label="$2"; shift 2 ;;
    *) echo "Неизвестный параметр: $1" >&2; exit 2 ;;
  esac
done

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
results="$root/loadtest/results"
mkdir --parents "$results"
base_url="${BASE_URL:-http://localhost:8080}"
psql_cmd="${PGHOST_CMD:-docker compose --project-directory $root exec --no-TTY postgres psql --username=user --dbname=app}"

$psql_cmd --quiet --command='SELECT pg_stat_statements_reset();' >/dev/null

docker run --rm --name "k6-$label" --network host --user "$(id --user)" \
  --volume "$root/loadtest/k6:/scripts:ro" \
  --volume "$results:/results" \
  --env BASE_URL="$base_url" --env SCENARIO="$scenario" --env RATE="$rate" \
  --env DURATION="$duration" --env STAGES="$stages" --env LABEL="$label" \
  grafana/k6:1.0.0 run --quiet /scripts/scenarios.js

$psql_cmd --command="
  SELECT calls,
         round(mean_exec_time::numeric, 2) AS mean_ms,
         round(max_exec_time::numeric, 2) AS max_ms,
         round(total_exec_time::numeric, 0) AS total_ms,
         left(regexp_replace(query, '\s+', ' ', 'g'), 110) AS query
  FROM pg_stat_statements
  WHERE query NOT ILIKE '%pg_stat_statements%'
  ORDER BY total_exec_time DESC
  LIMIT 8;" | tee "$results/$label.queries.txt"
