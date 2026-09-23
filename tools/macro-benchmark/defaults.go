package main

func defaultDatasets() map[string]datasetPreset {
	return map[string]datasetPreset{
		"full-tenant": {
			URL:       "https://storage.googleapis.com/pyroscope-sample-data/3500-services-5min.replay.zst?generation=1790088792036149",
			SizeBytes: 7171041968, CRC32C: "LejvQQ==",
		},
		"high-volume-service": {
			URL:       "https://storage.googleapis.com/pyroscope-sample-data/high-volume-service-2h.replay.zst?generation=1790088492848813",
			SizeBytes: 19319571496, CRC32C: "29OTSg==",
		},
		"checkoutservice": {
			URL:       "https://storage.googleapis.com/pyroscope-sample-data/checkoutservice-1-hour.replay?generation=1784809765684326",
			SizeBytes: 29880411, CRC32C: "qwpAbQ==",
		},
	}
}

// Controller defaults are code, not a mutable configuration file in the checkout.
func defaultRunConfig() runConfig {
	return runConfig{
		TimeoutMinutes: 480,
		VolumeSizeGiB:  500,
		Inputs: inputsConfig{
			BaselineRef:   "HEAD^",
			ComparisonRef: "HEAD",
			TenantID:      "macro-benchmark",
			MinioURL:      "https://github.com/pgsty/silo/releases/download/RELEASE.2026-08-04T00-00-00Z/minio_20260804000000.0.0_linux_amd64.tar.gz",
			MinioSHA256:   "3c1d229209db049618cb9c6d4132fa2882b15cdded337511486e811c187eec08",
			Benchtime:     "5x",
			Count:         5,
		},
	}
}
