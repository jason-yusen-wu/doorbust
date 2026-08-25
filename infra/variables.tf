variable "region" {
  description = "AWS region. Matches the existing Cognito user pool's region."
  type        = string
  default     = "us-east-2"
}

variable "allowed_cidr" {
  description = "CIDR allowed to reach SSH (22) and the app (8080) — set this to your own IP (e.g. \"1.2.3.4/32\"), not 0.0.0.0/0."
  type        = string
}

variable "instance_type" {
  description = "t3.micro is Free Tier eligible (750 hrs/month for 12 months on a new account) — good enough to validate the deploy pipeline and light load tests. It's only 1 GiB RAM, so for a real benchmarking session bump this to something like t3.medium or t3.large in terraform.tfvars (no longer free, but still cheap for a short on-demand run) — and switch off the t-family entirely (e.g. m6i.large) if CPU credits throttle a sustained load test."
  type        = string
  default     = "t3.micro"
}

variable "cognito_issuer_url" {
  description = "Cognito user pool issuer URL — not secret, stored as a plain SSM String param."
  type        = string
}

variable "cognito_client_id" {
  description = "Cognito app client ID — not secret, stored as a plain SSM String param."
  type        = string
}

variable "github_repo" {
  description = "GitHub repo allowed to assume the CI deploy role, as \"owner/name\". Used only in the OIDC trust policy's sub condition — this is what stops any other repo on GitHub from assuming the role."
  type        = string
  default     = "jason-yusen-wu/doorbust"
}
