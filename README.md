Workers-Marketplace-Platform

## Architecture

![Component architecture](docs/images/Component_diagram.png)

### Storage Usage

- `usermanagement-service` stores profile photos, worker skill evidence, identity documents, and report attachments in MinIO/S3.
- `booking-service` stores completion evidence and review photos in MinIO/S3.
- `chat-service` stores chat attachments in MinIO/S3.
- `chat-service` uses Redis for WebSocket pub/sub between service instances.
- `PostgreSQL + PostGIS` is the shared relational and geospatial database.

### Main Communication Types

- Frontend talks to backend through `api-gateway` over HTTP.
- `api-gateway` routes public API calls to the correct service.
- `booking-service` uses gRPC for required internal calls to `usermanagement-service` and `payment-service`.
- `notification-service` is called through internal HTTP endpoints by services that need to create notifications.
- 2GIS is used from the frontend for maps, routing, geocoding, and map rendering.
- Stripe is used by `payment-service` for payments and webhooks.

## Running locally

```bash
docker compose up --detach --wait --build   # Postgres + PostGIS, Redis, all services, gateway on :8080
```

MinIO is behind the `storage` profile (`docker compose --profile storage up ...`), so file uploads need it enabled.
CI (`.github/workflows/ci.yml`) runs `go vet`, `go build` and builds every service image on each pull request.

## Load testing

Tooling lives in `loadtest/`:

- `seed.sql` generates synthetic data: 10,000 workers, 20,000 customers, 220,000 bookings, 200,000 reviews (`unseed.sql` removes it).
- `k6/scenarios.js` has three scenarios driven by a constant arrival rate (requests per second, not virtual users, so a slow server cannot reduce its own load):
  `search` (`GET /api/geo/workers/nearby`), `polling` (`GET /api/bookings/my`), `login` (`POST /auth/login`).
- `run.sh` resets `pg_stat_statements`, runs k6 and saves the k6 summary plus the most expensive queries to `loadtest/results/`.

```bash
docker compose exec --no-TTY postgres psql --username=user --dbname=app < loadtest/seed.sql
loadtest/run.sh polling --rate 50 --duration 5m --label before-polling
loadtest/run.sh search --stages 50:1m,100:1m,200:1m --label search-ramp   # find the saturation point
```

When k6 and the stack share one machine, the machine itself can become the bottleneck. `monitor.sh` wraps a run,
samples CPU, memory, load average, temperature (when a sensor exists; WSL has none), power source and the busiest container
into `loadtest/results/<label>.system.csv`, and stops the test (exit code 3) if CPU stays saturated, memory runs out,
the machine overheats or the battery runs low:

```bash
loadtest/monitor.sh -- loadtest/run.sh polling --rate 50 --duration 5m --label after-polling
loadtest/monitor.sh --interval 2 --max-cpu 90 --cpu-seconds 30 -- loadtest/run.sh login --rate 100
```

### Results (local)

Measured locally on WSL2 (16 vCPU, 7.6 GB RAM), k6 and the whole stack on the same machine, 5 minutes per run, constant rate.
Short ramps showed saturation around 160 rps for search and polling and around 320 rps for login,
but at 80/90/190 rps the 5-minute runs already dropped requests, so the comparison uses lower rates.
All runs below finished with 0 errors and 0 dropped requests.

| Scenario | Rate | Version | p50 | p95 | p99 |
|---|---|---|---|---|---|
| Worker search | 40 rps | before | 39.8 ms | 51.1 ms | 161.8 ms |
| | | after DB fixes | 10.4 ms | 14.4 ms | 17.4 ms |
| Booking polling | 50 rps | before | 45.9 ms | 916.4 ms | 1087.7 ms |
| | | after DB fixes | 11.2 ms | 878.5 ms | 1621.0 ms |
| | | after gRPC fix | **2.8 ms** | **3.4 ms** | **3.9 ms** |
| Login | 100 rps | before | 64.4 ms | 73.3 ms | 78.2 ms |
| | | after DB fixes | 52.0 ms | 68.4 ms | 74.5 ms |

Login is dominated by bcrypt by design and was not optimized.

### What was changed and why

Found with `pg_stat_statements` and `EXPLAIN ANALYZE` (commit `b6df90a6`):

1. **Schema change on every read.** Reading a customer or worker profile ran `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`
   (15,001 calls in a 5-minute polling run). `ALTER TABLE` takes an exclusive lock, so concurrent requests queued behind it.
   Now it runs once per process.
2. **Search grouped all reviews.** The nearby-workers query aggregated all 200,000 reviews on every call.
   A `LEFT JOIN LATERAL` with an index on `reviews (worker_profile_id) INCLUDE (rating)` aggregates only the matched workers:
   mean query time went from 40.8 ms to 7.9 ms.
3. **Missing indexes for polling.** The bookings list and the pending-offer expiry `UPDATE` (run on every poll) did sequential scans.
   Indexes on `service_requests (customer_profile_id)`, `bookings (request_id)` and a partial index for `price_pending` bookings
   cut the database time of the polling run from about 675 s to about 4.4 s.

After that the polling median dropped but p95/p99 stayed near one second, so the tail was no longer in the database.
`booking-service` dialed a new gRPC connection to `usermanagement-service` for every request (DNS, TCP and HTTP/2 setup each time,
and a failed attempt waits for the gRPC reconnect backoff of about 1 s). Reusing one connection (commit `4fc363ee`) removed the tail.

### Running on AWS

`infra/terraform/` creates two `m6i` instances in one availability zone (app + database, and k6), a security group open only to your IP,
and an AWS Budget email alert. The app instance clones this fork, starts the stack and loads the seed data.

```bash
cd infra/terraform
cp terraform.tfvars.example terraform.tfvars   # set operator_cidr and budget_email
terraform init
terraform apply                                 # git_ref defaults to the loadtest-baseline tag ("before")
```

Wait until `/home/ubuntu/ready` exists on the app instance, then on the k6 instance:

```bash
export BASE_URL=http://<app_private_ip>:8080
export PGHOST_CMD="docker run --rm --interactive postgres:15 psql postgresql://user:pass@<app_private_ip>:5433/app"
loadtest/run.sh polling --rate 50 --duration 5m --label aws-before-polling
```

For the "after" runs check out the optimized commit on the app instance and rebuild:
`git fetch && git checkout loadtest-optimized && docker compose up --detach --wait --build`.

When finished, copy `loadtest/results/` from the k6 instance and run `terraform destroy`.
