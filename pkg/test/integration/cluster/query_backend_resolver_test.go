package cluster

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	_ "google.golang.org/grpc/balancer/roundrobin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func TestQueryBackendResolverDistributesRequests(t *testing.T) {
	var addresses []string
	for _, name := range []string{"backend-0", "backend-1", "backend-2"} {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		server := grpc.NewServer()
		healthServer := health.NewServer()
		healthServer.SetServingStatus(name, healthpb.HealthCheckResponse_SERVING)
		healthpb.RegisterHealthServer(server, healthServer)
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(server.Stop)
		addresses = append(addresses, listener.Addr().String())
	}
	conn, err := grpc.NewClient(queryBackendResolverScheme+":///"+strings.Join(addresses, ","),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultServiceConfig(`{"loadBalancingPolicy":"round_robin"}`))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	client := healthpb.NewHealthClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Each service name exists on just one replica. Successful checks for all
	// names prove that the resolver and balancer reach every backend.
	for _, name := range []string{"backend-0", "backend-1", "backend-2"} {
		require.Eventually(t, func() bool {
			response, err := client.Check(ctx, &healthpb.HealthCheckRequest{Service: name}, grpc.WaitForReady(true))
			return err == nil && response.GetStatus() == healthpb.HealthCheckResponse_SERVING
		}, 3*time.Second, 10*time.Millisecond)
	}
}

func TestQueryBackendResolverOverridesResourceExhaustedRetryBackoff(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var attempts atomic.Int32
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if attempts.Add(1) <= 3 {
			return nil, status.Error(codes.ResourceExhausted, "concurrency limit exceeded")
		}
		return handler(ctx, req)
	}))
	healthServer := health.NewServer()
	healthServer.SetServingStatus("backend", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(server, healthServer)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	defaultConfig := `{
		"loadBalancingPolicy": "round_robin",
		"methodConfig": [{
			"name": [{"service": ""}],
			"waitForReady": true,
			"retryPolicy": {
				"MaxAttempts": 500,
				"InitialBackoff": "1s",
				"MaxBackoff": "2s",
				"BackoffMultiplier": 1.1,
				"RetryableStatusCodes": ["UNAVAILABLE", "RESOURCE_EXHAUSTED"]
			}
		}]
	}`
	conn, err := grpc.NewClient(queryBackendResolverScheme+":///"+listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultServiceConfig(defaultConfig),
		grpc.WithMaxCallAttempts(500))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })

	client := healthpb.NewHealthClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	response, err := client.Check(ctx, &healthpb.HealthCheckRequest{Service: "backend"})
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.Equal(t, healthpb.HealthCheckResponse_SERVING, response.GetStatus())
	require.Equal(t, int32(4), attempts.Load())
	require.Less(t, elapsed, 250*time.Millisecond)
}
