#!/usr/bin/env bash
# Builds the doorbust image and ships it to the Terraform-provisioned EC2
# instance. Run `terraform -chdir=infra apply` first — this reads its output
# for the instance IP and SSH key.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
infra_dir="$repo_root/infra"
ssh_key="$infra_dir/deploy_key"

ip="$(terraform -chdir="$infra_dir" output -raw public_ip)"
ssh_opts=(-i "$ssh_key" -o StrictHostKeyChecking=accept-new)

echo "==> building image (linux/amd64 — the EC2 AMI is x86_64, regardless of what architecture you're building on)"
docker build --platform linux/amd64 -t doorbust:latest "$repo_root"

echo "==> shipping image to ec2-user@$ip"
docker save doorbust:latest | gzip | ssh "${ssh_opts[@]}" "ec2-user@$ip" 'gunzip | sudo docker load'

echo "==> restarting doorbust.service"
ssh "${ssh_opts[@]}" "ec2-user@$ip" 'sudo systemctl restart doorbust'

echo "==> done: http://$ip:8080"
