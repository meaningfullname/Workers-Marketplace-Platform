terraform {
  required_version = ">= 1.9"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Project = "workers-marketplace-loadtest"
    }
  }
}

data "aws_vpc" "default" {
  default = true
}

data "aws_availability_zones" "available" {
  state = "available"
}

# Обе машины в одной зоне: сеть между ними быстрая и бесплатная, и она не шумит в замерах.
data "aws_subnet" "selected" {
  vpc_id            = data.aws_vpc.default.id
  availability_zone = data.aws_availability_zones.available.names[0]
  default_for_az    = true
}

data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"] # Canonical

  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*"]
  }
}

resource "aws_key_pair" "loadtest" {
  key_name   = "workers-marketplace-loadtest"
  public_key = file(pathexpand(var.ssh_public_key_path))
}

resource "aws_security_group" "loadgen" {
  name_prefix = "wm-loadgen-"
  description = "k6 load generator: SSH only from the operator IP"
  vpc_id      = data.aws_vpc.default.id

  ingress {
    description = "SSH from the operator"
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = [var.operator_cidr]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

# При старте приложение создаёт администратора со стандартным паролем,
# а у базы простой пароль, поэтому наружу ничего не открыто.
resource "aws_security_group" "app" {
  name_prefix = "wm-app-"
  description = "Application: gateway and Postgres reachable only from the operator and k6"
  vpc_id      = data.aws_vpc.default.id

  ingress {
    description = "SSH from the operator"
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = [var.operator_cidr]
  }

  ingress {
    description = "Gateway from the operator"
    from_port   = 8080
    to_port     = 8080
    protocol    = "tcp"
    cidr_blocks = [var.operator_cidr]
  }

  ingress {
    description     = "Gateway from k6"
    from_port       = 8080
    to_port         = 8080
    protocol        = "tcp"
    security_groups = [aws_security_group.loadgen.id]
  }

  ingress {
    description     = "Postgres from k6, used for pg_stat_statements"
    from_port       = 5433
    to_port         = 5433
    protocol        = "tcp"
    security_groups = [aws_security_group.loadgen.id]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

locals {
  bootstrap = templatefile("${path.module}/bootstrap.sh.tftpl", {
    repo_url = var.repo_url
    git_ref  = var.git_ref
  })
}

resource "aws_instance" "app" {
  ami                    = data.aws_ami.ubuntu.id
  instance_type          = var.app_instance_type
  subnet_id              = data.aws_subnet.selected.id
  key_name               = aws_key_pair.loadtest.key_name
  vpc_security_group_ids = [aws_security_group.app.id]

  user_data = join("\n", [local.bootstrap, file("${path.module}/start-app.sh")])

  metadata_options {
    http_tokens = "required"
  }

  root_block_device {
    volume_type = "gp3"
    volume_size = 40
    encrypted   = true
  }

  tags = {
    Name = "wm-app"
  }
}

resource "aws_instance" "loadgen" {
  ami                    = data.aws_ami.ubuntu.id
  instance_type          = var.loadgen_instance_type
  subnet_id              = data.aws_subnet.selected.id
  key_name               = aws_key_pair.loadtest.key_name
  vpc_security_group_ids = [aws_security_group.loadgen.id]

  user_data = local.bootstrap

  metadata_options {
    http_tokens = "required"
  }

  root_block_device {
    volume_type = "gp3"
    volume_size = 20
    encrypted   = true
  }

  tags = {
    Name = "wm-loadgen"
  }
}

# Бюджет только присылает письмо. Сам он ничего не останавливает:
# расходы прекращает terraform destroy.
resource "aws_budgets_budget" "loadtest" {
  name         = "workers-marketplace-loadtest"
  budget_type  = "COST"
  limit_amount = var.budget_limit_usd
  limit_unit   = "USD"
  time_unit    = "MONTHLY"

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 50
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.budget_email]
  }

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "FORECASTED"
    subscriber_email_addresses = [var.budget_email]
  }
}
