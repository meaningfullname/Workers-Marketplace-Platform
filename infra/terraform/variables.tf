variable "region" {
  description = "Регион AWS"
  type        = string
  default     = "eu-central-1"
}

variable "operator_cidr" {
  description = "Твой IP с маской /32. Только с него открыт SSH и шлюз"
  type        = string

  validation {
    condition     = can(regex("^\\d+\\.\\d+\\.\\d+\\.\\d+/32$", var.operator_cidr))
    error_message = "Нужен один адрес в виде 203.0.113.10/32."
  }
}

variable "ssh_public_key_path" {
  description = "Путь к публичному SSH-ключу"
  type        = string
  default     = "~/.ssh/id_ed25519.pub"
}

variable "budget_email" {
  description = "Почта для уведомлений о расходах"
  type        = string
}

variable "budget_limit_usd" {
  description = "Месячный бюджет в долларах"
  type        = string
  default     = "10"
}

variable "app_instance_type" {
  description = "Машина с приложением и базой"
  type        = string
  default     = "m6i.xlarge"
}

variable "loadgen_instance_type" {
  description = "Машина с k6"
  type        = string
  default     = "m6i.large"
}

variable "repo_url" {
  description = "Публичный форк, который клонируют обе машины"
  type        = string
  default     = "https://github.com/meaningfullname/Workers-Marketplace-Platform.git"
}

variable "git_ref" {
  description = "Ветка, тег или коммит для первого замера"
  type        = string
  default     = "loadtest-baseline"
}
