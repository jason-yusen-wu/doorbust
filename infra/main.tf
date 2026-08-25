data "aws_vpc" "default" {
  default = true
}

data "aws_subnets" "default" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default.id]
  }
}

data "aws_ami" "al2023" {
  most_recent = true
  owners      = ["amazon"]

  filter {
    name   = "name"
    values = ["al2023-ami-*-x86_64"]
  }
  filter {
    name   = "virtualization-type"
    values = ["hvm"]
  }
}

data "aws_caller_identity" "current" {}

# --- SSH access ---

resource "tls_private_key" "deploy" {
  algorithm = "ED25519"
}

resource "aws_key_pair" "deploy" {
  key_name   = "doorbust-deploy"
  public_key = tls_private_key.deploy.public_key_openssh
}

resource "local_file" "deploy_private_key" {
  content         = tls_private_key.deploy.private_key_openssh
  filename        = "${path.module}/deploy_key"
  file_permission = "0600"
}

# --- Network ---

resource "aws_security_group" "doorbust" {
  name        = "doorbust"
  description = "doorbust benchmarking box: SSH + app port from allowed_cidr only"
  vpc_id      = data.aws_vpc.default.id

  ingress {
    description = "SSH"
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = [var.allowed_cidr]
  }

  ingress {
    description = "doorbust API"
    from_port   = 8080
    to_port     = 8080
    protocol    = "tcp"
    cidr_blocks = [var.allowed_cidr]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

# --- IAM: instance role can only read /doorbust/* SSM params ---

resource "aws_iam_role" "instance" {
  name = "doorbust-instance"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "ssm_read" {
  name = "doorbust-ssm-read"
  role = aws_iam_role.instance.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = ["ssm:GetParameter", "ssm:GetParametersByPath"]
      Resource = [
        "arn:aws:ssm:${var.region}:${data.aws_caller_identity.current.account_id}:parameter/doorbust",
        "arn:aws:ssm:${var.region}:${data.aws_caller_identity.current.account_id}:parameter/doorbust/*",
      ]
    }]
  })
}

resource "aws_iam_instance_profile" "instance" {
  name = "doorbust-instance"
  role = aws_iam_role.instance.name
}

# --- Non-secret config, readable by the instance role above ---
# The Neon DSN (SecureString) is NOT managed here — put it in SSM yourself
# (see the plan's verification steps) so it never enters Terraform state.

resource "aws_ssm_parameter" "cognito_issuer_url" {
  name  = "/doorbust/COGNITO_ISSUER_URL"
  type  = "String"
  value = var.cognito_issuer_url
}

resource "aws_ssm_parameter" "cognito_client_id" {
  name  = "/doorbust/COGNITO_CLIENT_ID"
  type  = "String"
  value = var.cognito_client_id
}

# --- The instance itself ---

resource "aws_instance" "doorbust" {
  ami                    = data.aws_ami.al2023.id
  instance_type          = var.instance_type
  subnet_id              = data.aws_subnets.default.ids[0]
  vpc_security_group_ids = [aws_security_group.doorbust.id]
  key_name               = aws_key_pair.deploy.key_name
  iam_instance_profile   = aws_iam_instance_profile.instance.name

  user_data = templatefile("${path.module}/templates/user_data.sh.tpl", {
    region           = var.region
    doorbust_service = file("${path.module}/../deploy/doorbust.service")
  })

  tags = {
    Name = "doorbust-benchmark"
  }
}

# --- CI/CD: ECR registry + GitHub Actions OIDC deploy role ---
#
# GitHub Actions builds the image, pushes it here, and then drives the deploy
# through SSM Run Command rather than SSH. That matters because the security
# group above admits only var.allowed_cidr: a GitHub-hosted runner's IP is
# unpredictable and would be rejected. SSM is outbound-only from the instance,
# so nothing has to be opened for it.

resource "aws_ecr_repository" "doorbust" {
  name = "doorbust"

  # Tags are git SHAs. Immutability means a deployed SHA permanently refers to
  # exactly one image, so a rollback to an old tag can't silently get new bits.
  image_tag_mutability = "IMMUTABLE"

  image_scanning_configuration {
    scan_on_push = true
  }
}

# Free Tier gives 500 MB of private-registry storage. The image is Alpine plus a
# static CGO_ENABLED=0 binary (~20-30 MB) and tags share the base layer, so
# keeping 5 lands well under the cap while still allowing a 5-deep rollback.
resource "aws_ecr_lifecycle_policy" "doorbust" {
  repository = aws_ecr_repository.doorbust.name

  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Keep only the 5 most recent images"
      selection = {
        tagStatus   = "any"
        countType   = "imageCountMoreThan"
        countNumber = 5
      }
      action = { type = "expire" }
    }]
  })
}

# The instance pulls from ECR, so its role needs read access to the repo.
# GetAuthorizationToken is account-wide by design — AWS does not scope it.
resource "aws_iam_role_policy" "ecr_pull" {
  name = "doorbust-ecr-pull"
  role = aws_iam_role.instance.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = "ecr:GetAuthorizationToken"
        Resource = "*"
      },
      {
        Effect = "Allow"
        Action = [
          "ecr:BatchGetImage",
          "ecr:GetDownloadUrlForLayer",
          "ecr:BatchCheckLayerAvailability",
        ]
        Resource = aws_ecr_repository.doorbust.arn
      },
    ]
  })
}

# Lets the preinstalled SSM agent register the box as a managed node. Without
# this, `aws ssm send-command` finds no target — note this is separate from the
# ssm:GetParameter grant above, which is about reading config, not remote exec.
resource "aws_iam_role_policy_attachment" "ssm_core" {
  role       = aws_iam_role.instance.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

# --- GitHub OIDC: short-lived credentials instead of a stored access key ---

data "tls_certificate" "github" {
  url = "https://token.actions.githubusercontent.com"
}

resource "aws_iam_openid_connect_provider" "github" {
  url             = "https://token.actions.githubusercontent.com"
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = [data.tls_certificate.github.certificates[0].sha1_fingerprint]
}

resource "aws_iam_role" "github_actions" {
  name = "doorbust-github-actions"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Federated = aws_iam_openid_connect_provider.github.arn }
      Action    = "sts:AssumeRoleWithWebIdentity"
      Condition = {
        StringEquals = {
          "token.actions.githubusercontent.com:aud" = "sts.amazonaws.com"
          # Load-bearing: without a sub condition, ANY repo on GitHub could
          # assume this role. This pins to the `production` environment, and
          # that environment's deployment branch policy allows only `main` —
          # so a pull request cannot obtain a token with this subject at all.
          # See var.github_oidc_subject for the claim format's two gotchas.
          "token.actions.githubusercontent.com:sub" = var.github_oidc_subject
        }
      }
    }]
  })
}

resource "aws_iam_role_policy" "github_actions" {
  name = "doorbust-github-actions"
  role = aws_iam_role.github_actions.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = "ecr:GetAuthorizationToken"
        Resource = "*"
      },
      {
        Effect = "Allow"
        Action = [
          "ecr:BatchCheckLayerAvailability",
          "ecr:InitiateLayerUpload",
          "ecr:UploadLayerPart",
          "ecr:CompleteLayerUpload",
          "ecr:PutImage",
          "ecr:BatchGetImage",
          "ecr:GetDownloadUrlForLayer",
        ]
        Resource = aws_ecr_repository.doorbust.arn
      },
      {
        # Describe* calls don't support resource-level permissions. The workflow
        # uses this to find the instance by tag, since Terraform state is local
        # and `terraform output` isn't available to a runner.
        Effect   = "Allow"
        Action   = "ec2:DescribeInstances"
        Resource = "*"
      },
      {
        Effect = "Allow"
        Action = "ssm:SendCommand"
        Resource = [
          aws_instance.doorbust.arn,
          "arn:aws:ssm:${var.region}::document/AWS-RunShellScript",
        ]
      },
      {
        Effect = "Allow"
        Action = [
          "ssm:GetCommandInvocation",
          "ssm:ListCommandInvocations",
        ]
        Resource = "*"
      },
    ]
  })
}
