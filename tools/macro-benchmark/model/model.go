// Package model defines code-configured macro benchmarks. Artifacts and specs
// are reusable definitions; replays and runs belong to a benchmark instance.
package model

import "time"

// ReplayArtifact describes a single-tenant profilecli replay recording. Checksums
// cover the downloaded bytes, including compression when present.
type ReplayArtifact struct {
	Name string
	URL  string // Remote HTTPS URL; mutually exclusive with Path.
	Path string // Local recording, copied into the bundle.

	SHA256    string // Optional for remote artifacts; required for local files.
	SizeBytes int64  // Remote artifacts without SHA256 require size.
}

// TargetVersion identifies the Pyroscope revision/artifacts under test. Use an
// immutable revision rather than a moving branch for reproducible comparisons.
type TargetVersion = string

// BenchmarkReplay ingests one artifact into an isolated tenant on an instance.
// A write-path change may require a new replay, not just a new query run.
type BenchmarkReplay struct {
	Name         string // Instance-local identifier referenced by BenchmarkRun.Replay.
	Version      TargetVersion
	ArtifactName string // References ReplayArtifact.Name.
	TenantID     string // ArtifactName + 8 random characters, assigned by the runner.
	Timeout      time.Duration
	SettleTime   time.Duration // Initial delay before waiting for compaction to settle.
}

// QueryParams are supplied after replay, when its query window is known.
// Request builders should encode Start/End in the units required by the API
// (Unix milliseconds for querier requests).
type QueryParams struct {
	Start       time.Time
	End         time.Time
	ProfileType string
	Selector    string
}

// QueryCase defines a querier request and its minimal response validation.
// BuildBody replaces YAML variable interpolation with ordinary Go code. It must
// return a fresh JSON-marshalable request on each call, without mutating params.
type QueryCase struct {
	Name             string
	Method           string // Full RPC path, e.g. /querier.v1.QuerierService/Series.
	BuildBody        func(params QueryParams) (any, error)
	RequiredField    string // Top-level JSON response field that must be non-empty.
	MaxResponseBytes int64  // Response size limit in bytes.
}

// BenchmarkSpec is a reusable query suite, independent of a particular replay.
// All cases use the replay's measured time window.
type BenchmarkSpec struct {
	Name        string // Identifier referenced by BenchmarkRun.Spec.
	ProfileType string
	Selector    string
	Cases       []QueryCase
	BenchTime   time.Duration // Measurement duration per case and repetition.
	Count       int           // Number of repetitions.
	Timeout     time.Duration // Overall query benchmark deadline.
}

// BenchmarkRun measures a spec against an existing replay. Version is the query
// target; it may differ from the version that originally ingested the data.
type BenchmarkRun struct {
	Name    string // Unique within the instance; identifies the run's results.
	Version TargetVersion
	Replay  string // References BenchmarkReplay.Name on the same instance.
	Spec    string // References BenchmarkSpec.Name.
}

// BinaryArtifact pins an externally downloaded executable or release archive.
// In particular, MinIO must have a trusted SHA256 before it is executed.
type BinaryArtifact struct {
	URL    string
	SHA256 string
}

// AWSInstanceConfig describes provisioning and SSH access. Discovery may fill
// omitted network/image settings before provisioning; it must not replace
// explicit settings. PrivateIP requires explicitly configured connectivity.
type AWSInstanceConfig struct {
	Region       string
	AMI          string
	InstanceType string
	SubnetID     string
	VPCID        string
	SSHCIDR      string // Runner's IPv4 /32.
	PrivateIP    bool
	KeyPath      string // Local SSH private key path; never bundled.

	CoreCount       int32
	ThreadsPerCore  int32
	VolumeSizeGiB   int32
	VolumeIOPS      int32
	VolumeMiBPerSec int32
}

// ProcessResources specifies CPU affinity and the soft Go runtime memory limit.
// It does not provide cgroup isolation or a hard memory limit.
type ProcessResources struct {
	CPUSet      string // taskset syntax, e.g. "4-14".
	MemoryLimit string // GOMEMLIMIT syntax, e.g. "45GiB"; empty uses runtime defaults.
}

// ClusterConfig describes the in-process V2 integration cluster. All components
// share the cluster process's CPU affinity, memory limit and garbage collector.
type ClusterConfig struct {
	Distributors      int
	SegmentWriters    int
	Metastores        int
	CompactionWorkers int
	QueryFrontends    int
	QueryBackends     int
	Resources         ProcessResources
}

// BenchmarkInstance owns one AWS host and its shared storage. Replays are
// ingested once and can be reused by multiple runs on that host. Replays and
// runs execute serially so measurements do not compete with each other.
//
// These types are definitions only: callers must resolve references, apply
// defaults and validate settings before provisioning or executing anything.
type BenchmarkInstance struct {
	Name    string
	AWS     AWSInstanceConfig
	Timeout time.Duration // Total instance lifetime budget, including replay.
	Bundle  string        // Local prepared bundle directory.
	Results string        // Local results directory.

	Minio               BinaryArtifact
	MinioResources      ProcessResources
	ControllerResources ProcessResources
	ClientResources     ProcessResources // Replay and query benchmark processes.
	Cluster             ClusterConfig

	Replays []BenchmarkReplay
	Runs    []BenchmarkRun
}

// BenchmarkPlan holds reusable definitions alongside the hosts that use them.
// Artifact, spec and instance names must each be unique within their collection.
// Replay and run names are scoped to their owning instance.
type BenchmarkPlan struct {
	Artifacts []ReplayArtifact
	Specs     []BenchmarkSpec
	Instances []BenchmarkInstance
}
