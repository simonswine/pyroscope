#!/usr/bin/env bash
# Runs the full benchmark suite on the provisioned GCP VM.
# Expects: Docker image at /tmp/pyroscope-bench.tar.gz
#          Benchmark tree at /opt/bench/
#          Helm chart at /opt/bench/k8s/chart/
set -euo pipefail

export PATH=$PATH:/usr/local/go/bin

BENCH_DURATION=${BENCH_DURATION:-10m}
BENCH_RATE=${BENCH_RATE:-20}
RESULTS=/tmp/bench-results
mkdir -p "$RESULTS"

# Convert duration string (e.g. 10m, 300s) to seconds for kubectl wait timeout
duration_to_seconds() {
  local d="$1"
  if [[ "$d" =~ ^([0-9]+)m$ ]]; then echo $(( ${BASH_REMATCH[1]} * 60 ))
  elif [[ "$d" =~ ^([0-9]+)s$ ]]; then echo "${BASH_REMATCH[1]}"
  elif [[ "$d" =~ ^([0-9]+)h$ ]]; then echo $(( ${BASH_REMATCH[1]} * 3600 ))
  else echo 600
  fi
}

DURATION_SECS=$(duration_to_seconds "$BENCH_DURATION")

# ── Load image ────────────────────────────────────────────────────────────────
echo "==> Loading Pyroscope image"
docker load < /tmp/pyroscope-bench.tar.gz

# ── kind cluster ─────────────────────────────────────────────────────────────
echo "==> Creating kind cluster"
kind create cluster --name pyroscope-bench --wait 60s
kubectl cluster-info --context kind-pyroscope-bench

# Load image into kind so pods can pull it without a registry
kind load docker-image pyroscope:bench --name pyroscope-bench

# ── Prometheus ────────────────────────────────────────────────────────────────
echo "==> Deploying Prometheus"
kubectl create namespace monitoring
kubectl apply -n monitoring -f /opt/bench/k8s/prometheus/rbac.yaml
kubectl apply -n monitoring -f /opt/bench/k8s/prometheus/rules/
kubectl apply -n monitoring -f /opt/bench/k8s/prometheus/prometheus.yaml
kubectl rollout status deployment/prometheus -n monitoring --timeout=120s

# ── Pyroscope ────────────────────────────────────────────────────────────────
echo "==> Deploying Pyroscope (microservices)"
kubectl create namespace pyroscope-bench
helm install pyroscope /opt/bench/k8s/chart \
  --namespace pyroscope-bench \
  --values /opt/bench/k8s/values-bench.yaml \
  --set pyroscope.image.tag=bench \
  --wait --timeout 5m

# ── Load generator ────────────────────────────────────────────────────────────
echo "==> Building load generator"
go build -o /usr/local/bin/load-generator /opt/bench/load-generator/main.go

echo "==> Running load generator (${BENCH_DURATION} at ${BENCH_RATE} profiles/sec)"

# Run the load generator as a k8s Job so metrics are scrapeable by Prometheus
kubectl apply -f - <<EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: load-gen
  namespace: pyroscope-bench
  labels:
    app: load-gen
spec:
  backoffLimit: 0
  template:
    metadata:
      labels:
        app: load-gen
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "2112"
    spec:
      restartPolicy: Never
      hostNetwork: false
      containers:
        - name: load-gen
          image: pyroscope:bench
          imagePullPolicy: Never
          command:
            - /bin/sh
            - -c
            - |
              cp /usr/local/bin/load-generator /tmp/load-generator
              /tmp/load-generator \
                --target http://pyroscope-distributor.pyroscope-bench.svc:4040 \
                --dataset /opt/bench/dataset \
                --duration ${BENCH_DURATION} \
                --rate ${BENCH_RATE}
          volumeMounts:
            - name: load-gen-bin
              mountPath: /usr/local/bin/load-generator
              subPath: load-generator
            - name: dataset
              mountPath: /opt/bench/dataset
      volumes:
        - name: load-gen-bin
          hostPath:
            path: /usr/local/bin/load-generator
        - name: dataset
          hostPath:
            path: /opt/bench/dataset
EOF

echo "==> Waiting for load generator to complete (timeout: $((DURATION_SECS + 120))s)"
kubectl wait --for=condition=Complete job/load-gen \
  -n pyroscope-bench --timeout=$(( DURATION_SECS + 120 ))s

# ── Correctness check ─────────────────────────────────────────────────────────
echo "==> Running correctness check"
go build -o /usr/local/bin/correctness /opt/bench/correctness/main.go

FRONTEND_IP=$(kubectl get svc pyroscope-query-frontend \
  -n pyroscope-bench -o jsonpath='{.spec.clusterIP}')

/usr/local/bin/correctness \
  --target "http://${FRONTEND_IP}:4040" \
  --manifest /opt/bench/dataset/manifest.json \
  --output "$RESULTS/correctness.json"

# ── Metrics report ────────────────────────────────────────────────────────────
echo "==> Exporting metrics report"
go build -o /usr/local/bin/bench-report /opt/bench/report/main.go

PROM_IP=$(kubectl get svc prometheus \
  -n monitoring -o jsonpath='{.spec.clusterIP}')

/usr/local/bin/bench-report \
  --prometheus "http://${PROM_IP}:9090" \
  --output "$RESULTS/metrics.json"

/usr/local/bin/bench-report \
  --input "$RESULTS/metrics.json" \
  --format markdown \
  | tee "$RESULTS/summary.md"

echo ""
echo "==> Benchmark complete. Results written to $RESULTS"
