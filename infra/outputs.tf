output "public_ip" {
  value = aws_instance.doorbust.public_ip
}

output "ssh_command" {
  value = "ssh -i infra/deploy_key ec2-user@${aws_instance.doorbust.public_ip}"
}

output "app_url" {
  value = "http://${aws_instance.doorbust.public_ip}:8080"
}

# The three below are what get pasted into the repo's GitHub Actions variables.

output "instance_id" {
  value = aws_instance.doorbust.id
}

output "ecr_repository_url" {
  value = aws_ecr_repository.doorbust.repository_url
}

output "github_actions_role_arn" {
  value = aws_iam_role.github_actions.arn
}
