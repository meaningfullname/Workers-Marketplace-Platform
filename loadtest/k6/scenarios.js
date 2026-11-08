// Три сценария: search (поиск работников), polling (поллинг бронирований), login.
// Нагрузка задаётся числом запросов в секунду, а не числом пользователей: так медленный
// сервер не снижает нагрузку сам себе, и хвосты p95/p99 не занижаются.
//
// Переменные: SCENARIO, RATE, DURATION, либо STAGES для поиска точки насыщения
// (например "50:1m,100:1m,200:1m"), BASE_URL, LABEL.
import http from 'k6/http';
import { check, fail } from 'k6';
import exec from 'k6/execution';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const SCENARIO = __ENV.SCENARIO || 'search';
const LABEL = __ENV.LABEL || `${SCENARIO}-run`;
const RATE = Number(__ENV.RATE || 20);
const DURATION = __ENV.DURATION || '5m';
const STAGES = __ENV.STAGES || '';
const PASSWORD = 'loadtest-password';
const TOKEN_USERS = 200; // Сколько клиентов логинятся заранее. Токен живёт 15 минут.

if (!['search', 'polling', 'login'].includes(SCENARIO)) {
  throw new Error(`Неизвестный SCENARIO: ${SCENARIO}`);
}

function executor() {
  const common = { timeUnit: '1s', preAllocatedVUs: 200, maxVUs: 2000, exec: SCENARIO };
  if (STAGES) {
    return {
      ...common,
      executor: 'ramping-arrival-rate',
      startRate: 1,
      stages: STAGES.split(',').map((stage) => {
        const [target, duration] = stage.split(':');
        return { target: Number(target), duration };
      }),
    };
  }
  return { ...common, executor: 'constant-arrival-rate', rate: RATE, duration: DURATION };
}

export const options = {
  scenarios: { [SCENARIO]: executor() },
  summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
  discardResponseBodies: true,
  thresholds: {
    // Пороги всегда выполняются: они нужны, чтобы k6 посчитал метрики только по сценарию,
    // без логинов из setup(). Иначе подготовка исказит p95 и p99.
    [`http_req_duration{scenario:${SCENARIO}}`]: ['p(95)>=0'],
    [`http_req_failed{scenario:${SCENARIO}}`]: ['rate>=0'],
    [`http_reqs{scenario:${SCENARIO}}`]: ['count>=0'],
  },
};

function login(email) {
  return http.post(
    `${BASE_URL}/auth/login`,
    JSON.stringify({ email, password: PASSWORD }),
    {
      headers: { 'Content-Type': 'application/json' },
      responseType: 'text',
      tags: { name: 'POST /auth/login' },
    },
  );
}

export function setup() {
  if (SCENARIO === 'login') {
    return { tokens: [] };
  }
  const tokens = [];
  for (let i = 1; i <= TOKEN_USERS; i += 1) {
    const response = login(`customer${i * 50}@loadtest.local`);
    if (response.status !== 200) {
      fail(`Логин при подготовке не прошёл: ${response.status} ${response.body}`);
    }
    tokens.push(response.json('access_token'));
  }
  return { tokens };
}

function tokenFor(data) {
  return data.tokens[exec.scenario.iterationInTest % data.tokens.length];
}

export function search(data) {
  const category = 1 + Math.floor(Math.random() * 8);
  const latitude = (51.05 + Math.random() * 0.2).toFixed(6);
  const longitude = (71.3 + Math.random() * 0.3).toFixed(6);
  const response = http.get(
    `${BASE_URL}/api/geo/workers/nearby?category_id=${category}&latitude=${latitude}&longitude=${longitude}&radius_meters=5000`,
    {
      headers: { Authorization: `Bearer ${tokenFor(data)}` },
      tags: { name: 'GET /api/geo/workers/nearby' },
    },
  );
  check(response, { 'статус 200': (r) => r.status === 200 });
}

export function polling(data) {
  const response = http.get(`${BASE_URL}/api/bookings/my`, {
    headers: { Authorization: `Bearer ${tokenFor(data)}` },
    tags: { name: 'GET /api/bookings/my' },
  });
  check(response, { 'статус 200': (r) => r.status === 200 });
}

export function login_() {
  const user = 1 + (exec.scenario.iterationInTest % 20000);
  const response = login(`customer${user}@loadtest.local`);
  check(response, { 'статус 200': (r) => r.status === 200 });
}

export { login_ as login };

export function handleSummary(data) {
  const tag = `{scenario:${SCENARIO}}`;
  const trend = data.metrics[`http_req_duration${tag}`].values;
  const dropped = data.metrics.dropped_iterations ? data.metrics.dropped_iterations.values.count : 0;
  const line = [
    `сценарий=${LABEL}`,
    `запросов=${data.metrics[`http_reqs${tag}`].values.count}`,
    `пропущено=${dropped}`,
    `ошибок=${(data.metrics[`http_req_failed${tag}`].values.rate * 100).toFixed(2)}%`,
    `p50=${trend.med.toFixed(1)}мс`,
    `p95=${trend['p(95)'].toFixed(1)}мс`,
    `p99=${trend['p(99)'].toFixed(1)}мс`,
  ].join(' ');
  return {
    stdout: `${line}\n`,
    [`/results/${LABEL}.json`]: JSON.stringify(data, null, 2),
  };
}
