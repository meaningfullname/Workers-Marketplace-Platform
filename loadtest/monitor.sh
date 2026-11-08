#!/usr/bin/env bash
# Следит за машиной, пока идёт нагрузочный тест, и останавливает тест, если машина перегружена.
#
#   loadtest/monitor.sh -- loadtest/run.sh polling --rate 50 --duration 5m --label after-polling
#   loadtest/monitor.sh --interval 2 --max-cpu 90 -- loadtest/run.sh login --rate 100
#
# Примерно каждые --interval секунд (сам docker stats занимает около 2 с) пишет строку в loadtest/results/<label>.system.csv:
# загрузка процессора и памяти, load average, температура (если есть датчик),
# питание и самый загруженный контейнер. Процент контейнера считается от одного ядра:
# 100% значит, что контейнер занял ядро целиком. В конце печатает итог.
#
# Тест останавливается, если:
#   - процессор загружен на --max-cpu % и больше дольше --cpu-seconds секунд подряд:
#     k6 и сервисы делят одни ядра, и замер на насыщенной машине уже ничего не значит;
#   - занято --max-mem % памяти и больше: дальше Linux начнёт убивать процессы;
#   - температура достигла --max-temp °C (только если датчик есть, в WSL его нет);
#   - ноутбук работает от батареи и заряд упал ниже --min-battery %.
# Код выхода: как у теста, или 3, если тест остановил монитор.
set -euo pipefail

interval=5 max_cpu=95 cpu_seconds=60 max_mem=90 max_temp=90 min_battery=20 out=""
while (($#)); do
  case "$1" in
    --interval) interval="$2"; shift 2 ;;
    --max-cpu) max_cpu="$2"; shift 2 ;;
    --cpu-seconds) cpu_seconds="$2"; shift 2 ;;
    --max-mem) max_mem="$2"; shift 2 ;;
    --max-temp) max_temp="$2"; shift 2 ;;
    --min-battery) min_battery="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    --) shift; break ;;
    *) echo "Неизвестный параметр: $1 (команду теста пиши после --)" >&2; exit 2 ;;
  esac
done
(($#)) || { echo "Укажи команду теста после --" >&2; exit 2; }

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ -z "$out" ]]; then
  label="run-$(date +%Y%m%d-%H%M%S)"
  args=("$@")
  for i in "${!args[@]}"; do
    [[ "${args[$i]}" == "--label" ]] && label="${args[$((i + 1))]:-$label}"
  done
  mkdir --parents "$root/loadtest/results"
  out="$root/loadtest/results/$label.system.csv"
fi

# Загрузка процессора считается по разнице счётчиков /proc/stat между двумя замерами.
read_cpu() { awk '/^cpu / { idle = $5 + $6; total = 0; for (i = 2; i <= 9; i++) total += $i; print total, idle }' /proc/stat; }
mem_pct() { awk '/^MemTotal/ { t = $2 } /^MemAvailable/ { a = $2 } END { printf "%d", (t - a) * 100 / t }' /proc/meminfo; }

# Самая высокая температура среди датчиков. Пусто, если датчиков нет (WSL, многие виртуалки).
temp_c() {
  local max="" t
  for f in /sys/class/thermal/thermal_zone*/temp /sys/class/hwmon/hwmon*/temp*_input; do
    [[ -r "$f" ]] || continue
    t=$(($(cat "$f" 2>/dev/null || echo 0) / 1000))
    ((t > 0)) && [[ -z "$max" || "$t" -gt "$max" ]] && max="$t"
  done
  echo "$max"
}

# "ac", "battery:81" или пусто, если это не ноутбук.
power() {
  local online
  for f in /sys/class/power_supply/*/online; do
    [[ -r "$f" ]] || continue
    online=$(cat "$f")
    if ((online == 1)); then echo ac; return; fi
  done
  for f in /sys/class/power_supply/*/capacity; do
    [[ -r "$f" ]] && { echo "battery:$(cat "$f")"; return; }
  done
}

top_container() {
  docker stats --no-stream --format '{{.CPUPerc}} {{.Name}}' 2>/dev/null \
    | sed 's/%//; s/workers-marketplace-platform-//; s/-1$//' \
    | sort --numeric-sort --reverse | head --lines=1 | awk '{ printf "%s %s%%", $2, $1 }'
}

start_power=$(power)
if [[ "$start_power" == battery:* ]]; then
  echo "Внимание: ноутбук работает от батареи (${start_power#battery:}%). Под нагрузкой процессор может сбрасывать частоту, и замер будет хуже. Лучше подключить зарядку."
fi

echo "time,cpu_pct,mem_pct,load1,temp_c,power,top_container" > "$out"

# setsid запускает тест в отдельной группе процессов, чтобы при остановке завершить его целиком,
# вместе с docker run и k6.
setsid "$@" &
test_pid=$!
stop_reason=""

stop_test() {
  stop_reason="$1"
  echo
  echo "Монитор останавливает тест: $stop_reason"
  kill -TERM -- "-$test_pid" 2>/dev/null || true
}
trap 'kill -TERM -- "-$test_pid" 2>/dev/null || true' INT TERM

read -r prev_total prev_idle < <(read_cpu)
busy_since="" max_cpu_seen=0 max_mem_seen=0 max_temp_seen="" sum_cpu=0 samples=0

while kill -0 "$test_pid" 2>/dev/null; do
  sleep "$interval"
  kill -0 "$test_pid" 2>/dev/null || break

  read -r total idle < <(read_cpu)
  dt=$((total - prev_total))
  cpu=0
  ((dt > 0)) && cpu=$(((dt - (idle - prev_idle)) * 100 / dt))
  prev_total=$total prev_idle=$idle

  mem=$(mem_pct)
  load1=$(cut --delimiter=' ' --fields=1 /proc/loadavg)
  temp=$(temp_c)
  pwr=$(power)
  top=$(top_container)
  now=$(date +%H:%M:%S)

  echo "$now,$cpu,$mem,$load1,$temp,$pwr,$top" >> "$out"
  printf '[монитор %s] CPU %3d%%  память %3d%%  load %s%s  %s\n' \
    "$now" "$cpu" "$mem" "$load1" "${temp:+  ${temp}°C}" "$top"

  samples=$((samples + 1)) sum_cpu=$((sum_cpu + cpu))
  ((cpu > max_cpu_seen)) && max_cpu_seen=$cpu
  ((mem > max_mem_seen)) && max_mem_seen=$mem
  [[ -n "$temp" ]] && [[ -z "$max_temp_seen" || "$temp" -gt "$max_temp_seen" ]] && max_temp_seen=$temp

  if ((cpu >= max_cpu)); then
    busy_since=${busy_since:-$SECONDS}
    if ((SECONDS - busy_since >= cpu_seconds)); then
      stop_test "процессор загружен на ${max_cpu}% и больше уже $((SECONDS - busy_since)) с"; break
    fi
  else
    busy_since=""
  fi
  if ((mem >= max_mem)); then stop_test "занято ${mem}% памяти"; break; fi
  if [[ -n "$temp" ]] && ((temp >= max_temp)); then stop_test "температура ${temp}°C"; break; fi
  if [[ "$pwr" == battery:* ]] && ((${pwr#battery:} < min_battery)); then
    stop_test "заряд батареи ${pwr#battery:}%"; break
  fi
done

set +e
wait "$test_pid"
test_status=$?
set -e

echo
echo "Итог монитора ($samples замеров каждые ${interval} с, файл $out):"
if ((samples > 0)); then
  echo "  процессор: в среднем $((sum_cpu / samples))%, максимум ${max_cpu_seen}%"
  echo "  память: максимум ${max_mem_seen}%"
  [[ -n "$max_temp_seen" ]] && echo "  температура: максимум ${max_temp_seen}°C"
  ((max_cpu_seen >= max_cpu)) && echo "  Процессор упирался в потолок: k6 и сервисы мешали друг другу, задержки в этом замере могут быть завышены."
fi
if [[ -n "$stop_reason" ]]; then
  echo "  Тест остановлен монитором: $stop_reason"
  exit 3
fi
exit "$test_status"
