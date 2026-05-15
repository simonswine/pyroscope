packer {
  required_plugins {
    googlecompute = {
      source  = "github.com/hashicorp/googlecompute"
      version = "~> 1"
    }
  }
}

variable "project_id" {
  type        = string
  description = "GCP project ID"
}

variable "zone" {
  type    = string
  default = "us-central1-a"
}

variable "machine_type" {
  type        = string
  default     = "n2-standard-8"
  description = "GCP machine type (8 vCPU, 32 GB fits the benchmark replica counts)"
}

variable "image_tar" {
  type        = string
  description = "Local path to the gzipped Pyroscope Docker image tar (e.g. /tmp/pyroscope-bench.tar.gz)"
}

variable "bench_duration" {
  type    = string
  default = "10m"
}

variable "bench_rate" {
  type    = string
  default = "20"
}

source "googlecompute" "bench" {
  project_id          = var.project_id
  source_image_family = "ubuntu-2204-lts"
  zone                = var.zone
  machine_type        = var.machine_type
  disk_size           = 50
  ssh_username        = "packer"
  # Provision-only: tear the VM down after the build without saving a new image.
  skip_create_image = true
}

build {
  sources = ["source.googlecompute.bench"]

  # Install Docker, kind, kubectl, helm, Go on the fresh VM.
  provisioner "shell" {
    script = "tools/pyrobench/packer/scripts/install-tools.sh"
  }

  # Upload the Pyroscope image tar.
  provisioner "file" {
    source      = var.image_tar
    destination = "/tmp/pyroscope-bench.tar.gz"
  }

  # Upload the benchmark tree (dataset, k8s configs, Go source).
  provisioner "file" {
    source      = "tools/pyrobench/"
    destination = "/opt/bench/"
  }

  # Upload the Helm chart so the benchmark script can deploy without internet access.
  provisioner "file" {
    source      = "operations/pyroscope/helm/pyroscope/"
    destination = "/opt/bench/k8s/chart/"
  }

  # Run the full benchmark suite.
  provisioner "shell" {
    environment_vars = [
      "BENCH_DURATION=${var.bench_duration}",
      "BENCH_RATE=${var.bench_rate}",
    ]
    script = "tools/pyrobench/packer/scripts/run-benchmark.sh"
  }

  # Pull results back to the local machine into bench-results/.
  provisioner "file" {
    source      = "/tmp/bench-results/"
    destination = "bench-results/"
    direction   = "download"
  }
}
