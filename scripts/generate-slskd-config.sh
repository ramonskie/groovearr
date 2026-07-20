#!/bin/sh
# Generate slskd.yml from .env credentials
# Usage: .env must exist with SLSKD_SOULSEEK_USERNAME and SLSKD_SOULSEEK_PASSWORD

set -e

if [ ! -f .env ]; then
  echo "ERROR: .env file not found. Copy .env.example and fill in credentials."
  exit 1
fi

# Source .env
. ./.env

if [ -z "$SLSKD_SOULSEEK_USERNAME" ] || [ -z "$SLSKD_SOULSEEK_PASSWORD" ]; then
  echo "ERROR: .env must contain SLSKD_SOULSEEK_USERNAME and SLSKD_SOULSEEK_PASSWORD"
  exit 1
fi

cat > slskd.yml << YAML
# slskd configuration — auto-generated from .env by scripts/generate-slskd-config.sh
# Do not edit manually. Regenerate with: make docker-setup

web:
  port: 5030
  https:
    disabled: true
  authentication:
    disabled: false
    username: slskd
    password: slskd
    api_keys:
      groovearr:
        key: groovearr-test-key-123456789012345
        role: readwrite
        cidr: 0.0.0.0/0,::/0

soulseek:
  username: ${SLSKD_SOULSEEK_USERNAME}
  password: ${SLSKD_SOULSEEK_PASSWORD}
  listen_port: 50300

shares:
  directories:
    - /downloads
    - /music

directories:
  downloads: /downloads
YAML

echo "Generated slskd.yml (user: ${SLSKD_SOULSEEK_USERNAME})"
