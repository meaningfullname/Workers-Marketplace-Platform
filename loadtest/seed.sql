-- Синтетические данные для нагрузочных тестов: 10 000 работников, 20 000 клиентов,
-- 220 000 бронирований, 200 000 отзывов. Адреса почты на домене loadtest.local.
-- Повторный запуск ничего не меняет. Удаление: loadtest/unseed.sql.
\set ON_ERROR_STOP on

CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;

SELECT EXISTS (SELECT 1 FROM users WHERE email LIKE '%@loadtest.local') AS already_seeded \gset
\if :already_seeded
  \echo 'Данные уже есть, пропускаю.'
  \quit
\endif

BEGIN;

-- Одинаковое зерно даёт одинаковые данные при каждом запуске.
SELECT setseed(0.42);

-- Один хеш на всех: bcrypt с той же стоимостью 10, что и в сервисе. Логин проверяет его как настоящий.
CREATE TEMP TABLE seed_hash ON COMMIT DROP AS
SELECT crypt('loadtest-password', gen_salt('bf', 10)) AS password_hash;

CREATE TEMP TABLE seed_workers ON COMMIT DROP AS
WITH new_users AS (
  INSERT INTO users (full_name, email, password_hash, role, status)
  SELECT format('Load Worker %s', i), format('worker%s@loadtest.local', i), h.password_hash, 'worker', 'active'
  FROM generate_series(1, 10000) AS i CROSS JOIN seed_hash h
  RETURNING user_id, email
),
points AS MATERIALIZED (
  SELECT
    user_id,
    split_part(split_part(email, '@', 1), 'worker', 2)::int AS idx,
    round((51.05 + random() * 0.20)::numeric, 6) AS latitude,
    round((71.30 + random() * 0.30)::numeric, 6) AS longitude,
    random() AS verified_roll,
    random() AS available_roll
  FROM new_users
),
new_profiles AS (
  INSERT INTO worker_profiles (
    user_id, verification_status, current_latitude, current_longitude, is_available, current_location
  )
  SELECT
    user_id,
    (CASE WHEN verified_roll < 0.9 THEN 'verified' ELSE 'pending' END)::verification_status,
    latitude,
    longitude,
    available_roll < 0.8,
    ST_SetSRID(ST_MakePoint(longitude::float8, latitude::float8), 4326)::geography
  FROM points
  RETURNING worker_profile_id, user_id
)
SELECT p.idx, np.worker_profile_id, 1 + (p.idx % 8) AS category_id
FROM points p JOIN new_profiles np USING (user_id);

-- У каждого работника основной навык, у половины ещё один.
INSERT INTO worker_skills (worker_profile_id, category_id, experience_level, price_base, is_verified)
SELECT
  worker_profile_id,
  category_id,
  (ARRAY['junior', 'middle', 'senior'])[1 + floor(random() * 3)::int]::experience_level,
  round((3000 + random() * 17000)::numeric, 2),
  random() < 0.9
FROM seed_workers
UNION ALL
SELECT
  worker_profile_id,
  1 + ((category_id + 2) % 8),
  (ARRAY['junior', 'middle', 'senior'])[1 + floor(random() * 3)::int]::experience_level,
  round((3000 + random() * 17000)::numeric, 2),
  random() < 0.9
FROM seed_workers
WHERE idx % 2 = 0;

CREATE TEMP TABLE seed_customers ON COMMIT DROP AS
WITH new_users AS (
  INSERT INTO users (full_name, email, password_hash, role, status)
  SELECT format('Load Customer %s', i), format('customer%s@loadtest.local', i), h.password_hash, 'customer', 'active'
  FROM generate_series(1, 20000) AS i CROSS JOIN seed_hash h
  RETURNING user_id, email
),
new_profiles AS (
  INSERT INTO customer_profiles (user_id, address)
  SELECT user_id, 'Astana, load test street' FROM new_users
  RETURNING customer_profile_id, user_id
)
SELECT
  split_part(split_part(u.email, '@', 1), 'customer', 2)::int AS idx,
  np.customer_profile_id
FROM new_users u JOIN new_profiles np USING (user_id);

-- План заказов: идентификаторы берём заранее, чтобы связать заявку, бронирование и отзыв.
-- random() в квадрате даёт перекос: как и в жизни, у части работников заказов намного больше.
CREATE TEMP TABLE seed_orders ON COMMIT DROP AS
WITH picks AS MATERIALIZED (
  SELECT
    n,
    1 + floor(random() * 20000)::int AS customer_idx,
    1 + floor(power(random(), 2) * 10000)::int AS worker_idx,
    now() - random() * interval '365 days' AS created_at,
    round((51.05 + random() * 0.20)::numeric, 6) AS latitude,
    round((71.30 + random() * 0.30)::numeric, 6) AS longitude,
    1 + floor(random() * 5)::int AS rating
  FROM generate_series(1, 220000) AS n
)
SELECT
  p.*,
  c.customer_profile_id,
  w.worker_profile_id,
  w.category_id,
  nextval(pg_get_serial_sequence('service_requests', 'request_id'))::int AS request_id,
  nextval(pg_get_serial_sequence('bookings', 'booking_id'))::int AS booking_id
FROM picks p
JOIN seed_customers c ON c.idx = p.customer_idx
JOIN seed_workers w ON w.idx = p.worker_idx;

INSERT INTO service_requests (
  request_id, customer_profile_id, category_id, description, address,
  latitude, longitude, status, created_at, location
)
SELECT
  request_id, customer_profile_id, category_id, 'Load test request', 'Astana, load test street',
  latitude, longitude,
  (CASE WHEN n <= 200000 THEN 'completed' ELSE 'accepted' END)::request_status,
  created_at,
  ST_SetSRID(ST_MakePoint(longitude::float8, latitude::float8), 4326)::geography
FROM seed_orders;

INSERT INTO bookings (
  booking_id, request_id, worker_profile_id, scheduled_time, start_time, end_time,
  status, final_price, customer_confirmed, created_at
)
SELECT
  booking_id, request_id, worker_profile_id, created_at, created_at, created_at + interval '2 hours',
  (CASE WHEN n <= 200000 THEN 'completed' ELSE 'scheduled' END)::booking_status,
  round((3000 + random() * 17000)::numeric, 2),
  n <= 200000,
  created_at
FROM seed_orders;

INSERT INTO reviews (booking_id, customer_profile_id, worker_profile_id, rating, comment, created_at)
SELECT booking_id, customer_profile_id, worker_profile_id, rating, 'Load test review', created_at + interval '1 day'
FROM seed_orders
WHERE n <= 200000;

COMMIT;

ANALYZE;

SELECT 'users' AS table_name, count(*) FROM users
UNION ALL SELECT 'worker_profiles', count(*) FROM worker_profiles
UNION ALL SELECT 'customer_profiles', count(*) FROM customer_profiles
UNION ALL SELECT 'bookings', count(*) FROM bookings
UNION ALL SELECT 'reviews', count(*) FROM reviews;
