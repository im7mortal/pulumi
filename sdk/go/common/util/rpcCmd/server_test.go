package rpcCmd

import (
	"context"
	"fmt"
	"testing"
	"time"

	pingpb "github.com/pulumi/pulumi/sdk/v3/go/common/util/rpcCmd/mockGRPC"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
)

// PingServer implements the PingService.
type PingServer struct {
	pingpb.UnimplementedPingServiceServer
}

// Ping method returns a "Pong" response.
func (s *PingServer) Ping(ctx context.Context, req *pingpb.PingRequest) (*pingpb.PingResponse, error) {
	return &pingpb.PingResponse{Reply: "Pong"}, nil
}

func TestPing(t *testing.T) {

	s, err := NewServer(Config{})

	assert.NoError(t, err)

	go s.Run(func(server *grpc.Server) error {
		pingpb.RegisterPingServiceServer(server, &PingServer{})
		return nil
	}, func() {})

	time.Sleep(time.Second)

	// Connect to the gRPC server
	conn, err := grpc.Dial(fmt.Sprintf("localhost:%d", s.handle.Port), grpc.WithInsecure())
	assert.NoError(t, err)
	defer conn.Close()

	client := pingpb.NewPingServiceClient(conn)

	// Send a Ping request
	req := &pingpb.PingRequest{Message: "Ping"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	resp, err := client.Ping(ctx, req)
	if err != nil {
		t.Fatalf("Error while calling Ping: %v", err)
	}

	// Assert the response
	assert.Equal(t, "Pong", resp.Reply, "Expected Pong response")

	close(s.cancelChannel)

}
