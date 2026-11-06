-- Удаляет только данные из loadtest/seed.sql. Порядок важен: заявки и бронирования защищены RESTRICT.
\set ON_ERROR_STOP on
BEGIN;

CREATE TEMP TABLE load_requests ON COMMIT DROP AS
SELECT sr.request_id
FROM service_requests sr
JOIN customer_profiles cp ON cp.customer_profile_id = sr.customer_profile_id
JOIN users u ON u.user_id = cp.user_id
WHERE u.email LIKE '%@loadtest.local';

DELETE FROM reviews WHERE booking_id IN (
  SELECT booking_id FROM bookings WHERE request_id IN (SELECT request_id FROM load_requests)
);
DELETE FROM bookings WHERE request_id IN (SELECT request_id FROM load_requests);
DELETE FROM service_requests WHERE request_id IN (SELECT request_id FROM load_requests);
DELETE FROM users WHERE email LIKE '%@loadtest.local';

COMMIT;
