# Продолжение bootstrap только для машины с приложением.
cd /home/ubuntu/app

cat > .env <<EOF
JWT_SECRET=$(openssl rand -hex 32)
GATEWAY_SHARED_SECRET=$(openssl rand -hex 32)
EOF
chown ubuntu:ubuntu .env
chmod 600 .env

docker compose up --detach --wait --build
docker compose exec --no-TTY postgres psql --username=user --dbname=app < loadtest/seed.sql

touch /home/ubuntu/ready
