package cluster

import (
	"fmt"
	"strings"

	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/resolver/manual"
)

const queryBackendResolverScheme = "cluster-query-backends"

func init() {
	resolver.Register(queryBackendResolverBuilder{})
}

// queryBackendResolverBuilder exposes the harness's static backend list to the
// query client's round_robin balancer. Keep this in the harness so benchmarked
// revisions need no production client or configuration changes.
type queryBackendResolverBuilder struct{}

func (queryBackendResolverBuilder) Scheme() string { return queryBackendResolverScheme }

func (queryBackendResolverBuilder) Build(target resolver.Target, cc resolver.ClientConn, opts resolver.BuildOptions) (resolver.Resolver, error) {
	var addresses []resolver.Address
	for _, address := range strings.Split(target.Endpoint(), ",") {
		if address == "" {
			return nil, fmt.Errorf("empty query-backend address in %q", target.Endpoint())
		}
		addresses = append(addresses, resolver.Address{Addr: address})
	}
	builder := manual.NewBuilderWithScheme(queryBackendResolverScheme)
	serviceConfig := cc.ParseServiceConfig(queryBackendServiceConfig)
	if serviceConfig.Err != nil {
		return nil, fmt.Errorf("parse query-backend service config: %w", serviceConfig.Err)
	}
	builder.InitialState(resolver.State{
		Addresses:     addresses,
		ServiceConfig: serviceConfig,
	})
	return builder.Build(target, cc, opts)
}

// queryBackendServiceConfig overrides the default 1s-2s gRPC retry backoff used
// by querybackendclient so in-process concurrency-limit rejections retry within
// milliseconds instead of pinning benchmark latencies to 1s+ backoff timers.
const queryBackendServiceConfig = `{
	"loadBalancingPolicy": "round_robin",
	"methodConfig": [{
		"name": [{"service": ""}],
		"waitForReady": true,
		"retryPolicy": {
			"MaxAttempts": 500,
			"InitialBackoff": "0.005s",
			"MaxBackoff": "0.02s",
			"BackoffMultiplier": 1.1,
			"RetryableStatusCodes": [
				"UNAVAILABLE",
				"RESOURCE_EXHAUSTED"
			]
		},
		"timeout": "30s"
	}]
}`
