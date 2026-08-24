#!/usr/bin/env bash
# Runs once at instance boot. Installs Docker, and a small script + systemd
# unit that render /etc/doorbust.env from SSM and run the app container.
# Deploying a new image is scripts/deploy.sh's job, not this.
set -euo pipefail

dnf install -y docker jq
systemctl enable --now docker

cat >/usr/local/bin/render-doorbust-env.sh <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail
aws ssm get-parameters-by-path \
  --path /doorbust \
  --with-decryption \
  --region "${region}" \
  --query 'Parameters[*].{n:Name,v:Value}' \
  --output json \
  | jq -r '.[] | "\(.n | split("/") | last)=\(.v)"' \
  > /etc/doorbust.env
chmod 600 /etc/doorbust.env
SCRIPT
chmod +x /usr/local/bin/render-doorbust-env.sh

cat >/etc/systemd/system/doorbust.service <<'UNIT'
${doorbust_service}
UNIT

systemctl daemon-reload
systemctl enable doorbust
# Not started here — there's no image loaded yet. scripts/deploy.sh loads the
# image and starts/restarts the service.
