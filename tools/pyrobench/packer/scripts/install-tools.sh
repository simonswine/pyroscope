#!/usr/bin/env bash
# Installs all tools required to run the benchmark on a fresh Ubuntu 22.04 VM.
# Versions are pinned for reproducibility.
set -euo pipefail

GO_VERSION=1.24.3
KIND_VERSION=0.23.0
KUBECTL_VERSION=1.30.0
HELM_VERSION=3.14.4

export DEBIAN_FRONTEND=noninteractive

echo "==> Updating apt"
sudo apt-get update -q

echo "==> Installing Docker"
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker packer
# Apply group change without re-login
newgrp docker <<'DOCKERGRP'
docker info > /dev/null
DOCKERGRP

echo "==> Installing Go ${GO_VERSION}"
curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" \
  | sudo tar -C /usr/local -xz
echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh
export PATH=$PATH:/usr/local/go/bin

echo "==> Installing kind ${KIND_VERSION}"
curl -fsSL "https://kind.sigs.k8s.io/dl/v${KIND_VERSION}/kind-linux-amd64" \
  -o /tmp/kind
sudo install -o root -g root -m 0755 /tmp/kind /usr/local/bin/kind

echo "==> Installing kubectl ${KUBECTL_VERSION}"
curl -fsSL "https://dl.k8s.io/release/v${KUBECTL_VERSION}/bin/linux/amd64/kubectl" \
  -o /tmp/kubectl
sudo install -o root -g root -m 0755 /tmp/kubectl /usr/local/bin/kubectl

echo "==> Installing helm ${HELM_VERSION}"
curl -fsSL "https://get.helm.sh/helm-v${HELM_VERSION}-linux-amd64.tar.gz" \
  | sudo tar -C /usr/local/bin --strip-components=1 -xz linux-amd64/helm

echo "==> Tool versions"
docker --version
go version
kind --version
kubectl version --client
helm version --short
