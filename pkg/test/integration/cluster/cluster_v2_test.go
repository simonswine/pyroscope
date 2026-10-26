package cluster

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/grafana/pyroscope/v2/pkg/metastore/raftnode/raftnodepb"
)

type testRaftNodeService struct {
	raftnodepb.UnimplementedRaftNodeServiceServer
	promoted *raftnodepb.PromoteToLeaderRequest
}

func (s *testRaftNodeService) NodeInfo(context.Context, *raftnodepb.NodeInfoRequest) (*raftnodepb.NodeInfoResponse, error) {
	return &raftnodepb.NodeInfoResponse{Node: &raftnodepb.NodeInfo{
		ServerId:    "metastore-1",
		LeaderId:    "metastore-1",
		CurrentTerm: 7,
		Peers: []*raftnodepb.NodeInfo_Peer{
			{ServerId: "metastore-0"},
			{ServerId: "metastore-1"},
			{ServerId: "metastore-2"},
		},
	}}, nil
}

func (s *testRaftNodeService) PromoteToLeader(_ context.Context, req *raftnodepb.PromoteToLeaderRequest) (*raftnodepb.PromoteToLeaderResponse, error) {
	s.promoted = req
	return &raftnodepb.PromoteToLeaderResponse{}, nil
}

func TestMetastoreReadyCheckPromotesByServerID(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer()
	service := &testRaftNodeService{}
	raftnodepb.RegisterRaftNodeServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	port := listener.Addr().(*net.TCPAddr).Port
	metastores := []*Component{
		{Target: "metastore", replica: 0},
		{Target: "metastore", replica: 1, grpcPort: port},
		{Target: "metastore", replica: 2, raftPort: 12345},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, metastores[1].metastoreReadyCheck(ctx, metastores, metastores[2]))
	require.Equal(t, "metastore-2", service.promoted.GetServerId())
	require.Equal(t, uint64(7), service.promoted.GetCurrentTerm())
}
