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

variable "github_oidc_subject" {
  description = <<-EOT
    Exact `sub` claim on the OIDC token GitHub Actions presents. This is what
    stops any other repo on GitHub from assuming the deploy role, so it must
    match byte-for-byte. Two things shape it, and both are easy to get wrong:

      * A job that references an `environment:` gets `:environment:<name>`,
        NOT `:ref:refs/heads/<branch>`. The environment form takes precedence.
      * Repos created after 2026-07-15 use immutable subject claims, which
        embed numeric owner and repo IDs: `owner@<owner-id>/repo@<repo-id>`.
        These survive renames, which is why they are preferred.

    Do not hand-assemble this. Read the real value from a failed (or
    successful) assume-role call:

      aws cloudtrail lookup-events --region us-east-2 \
        --lookup-attributes AttributeKey=EventName,AttributeValue=AssumeRoleWithWebIdentity \
        --max-results 1 --query 'Events[0].CloudTrailEvent' --output text \
        | jq -r .userIdentity.userName

    Never widen this to a StringLike wildcard on the owner segment —
    `repo:jason-yusen-wu*/...` would also match `jason-yusen-wu-evil`.
  EOT
  type        = string
  default     = "repo:jason-yusen-wu@286712433/doorbust@1336413790:environment:production"
}
