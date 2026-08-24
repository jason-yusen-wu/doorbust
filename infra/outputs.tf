output "public_ip" {
  value = aws_instance.doorbust.public_ip
}

output "ssh_command" {
  value = "ssh -i infra/deploy_key ec2-user@${aws_instance.doorbust.public_ip}"
}

output "app_url" {
  value = "http://${aws_instance.doorbust.public_ip}:8080"
}
