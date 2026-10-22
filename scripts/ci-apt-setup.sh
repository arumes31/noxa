#!/usr/bin/env bash
set -euo pipefail
# Some hosted runners stall on azure.archive.ubuntu.com. Keep the same signed
# Ubuntu suites/components and keyrings, using the official archive instead.
for source in /etc/apt/sources.list /etc/apt/sources.list.d/*.list /etc/apt/sources.list.d/*.sources; do
  if [[ -f "$source" ]]; then
    sudo sed -i 's|http://azure.archive.ubuntu.com/ubuntu|https://archive.ubuntu.com/ubuntu|g; s|https://azure.archive.ubuntu.com/ubuntu|https://archive.ubuntu.com/ubuntu|g' "$source"
  fi
done
printf '%s\n' 'Acquire::Retries "3";' 'Acquire::http::Timeout "30";' 'Acquire::https::Timeout "30";' | sudo tee /etc/apt/apt.conf.d/99-noxa-ci-network >/dev/null
