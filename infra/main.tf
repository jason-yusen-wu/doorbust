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
