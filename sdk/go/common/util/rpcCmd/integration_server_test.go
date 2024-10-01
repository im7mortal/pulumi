package rpcCmd

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/cmdutil"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	pingpb "github.com/pulumi/pulumi/sdk/v3/go/common/util/rpcCmd/mockGRPC"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
)

var tests = map[string]struct {
	config Config
	give   []string

	f func(s *Server)
}{
	"normal_run": {
		config: Config{},
		give:   []string{"localhost:"},
		f: func(s *Server) {
			s.Run(func(server *grpc.Server) error {
				pingpb.RegisterPingServiceServer(server, &PingServer{})
				return nil
			}, func() {})
		},
	},
}

// PingServer implements the PingService.
type PingServer struct {
	pingpb.UnimplementedPingServiceServer
}

// Ping method returns a "Pong" response.
func (s *PingServer) Ping(ctx context.Context, req *pingpb.PingRequest) (*pingpb.PingResponse, error) {

	return &pingpb.PingResponse{Reply: "Pong"}, nil
}

func TestIntegration(t *testing.T) {

	s, err := NewServer(Config{})

	assert.NoError(t, err)

	// record buffer to find if it writes port correctly
	originalStdout := os.Stdout // Save original stdout
	r, w, _ := os.Pipe()
	os.Stdout = w // Redirect stdout

	finished := make(chan struct{})
	go func() {
		s.Run(func(server *grpc.Server) error {
			pingpb.RegisterPingServiceServer(server, &PingServer{})
			return nil
		}, func() {})
		close(finished)
	}()
	// give the server time to start and write port to the stdout
	time.Sleep(time.Second)

	// server should write port to stdout for 1 second
	w.Close()
	os.Stdout = originalStdout
	var buf bytes.Buffer
	buf.ReadFrom(r)
	fmt.Print(buf.String())

	// look that port was printed to the stdout
	assert.Contains(t, buf.String(), fmt.Sprintf("%d", s.handle.Port), "Expected port information in stdout")

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

	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Errorf("The server supposed to shutdown immediately after cancel signal but after 1 second it's still not finished")
	}

}

func TestSubprocessExit1(t *testing.T) {
	for testCaseId, testCase := range tests {
		t.Run(fmt.Sprintf("Test Case %s", testCaseId), func(t *testing.T) {
			// Run the test in a subprocess
			cmd := exec.Command(os.Args[0], append([]string{"-test.run=TestCmd"}, testCase.give...)...)
			cmd.Env = append(os.Environ(), "TEST_CASE_ID="+testCaseId)

			// Capture stdout dynamically
			stdoutPipe, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatalf("Failed to get stdout pipe: %v", err)
			}

			serverDone := make(chan struct{})
			// Start the command
			go func() {
				if err := cmd.Start(); err != nil { // Use Start() instead of Run() here to avoid blocking
					t.Fatalf("Failed to start command: %v", err)
				}

				// Wait for the subprocess to finish
				err = cmd.Wait()
				if err != nil {
					t.Fatalf("Subprocess finished with error: %v", err)
				}
				close(serverDone)
			}()

			// Read stdout to capture the port number
			portC := make(chan string, 10000)
			go func() {
				scanner := bufio.NewScanner(stdoutPipe)
				for scanner.Scan() {
					line := scanner.Text()
					fmt.Printf("%s\n", line)
					portC <- line
				}
			}()

			// Wait for the port to be captured
			var port string
			select {
			case port = <-portC:
			case <-time.After(2 * time.Second):
				t.Fatal("Timeout waiting for the port to be printed")
			}

			// Check if the port is a valid integer
			_, err = strconv.Atoi(port)
			if err != nil {
				t.Fatalf("Expected port number, got: %s", port)
			}

			// Connect to the gRPC server using the captured port
			conn, err := grpc.Dial(fmt.Sprintf("localhost:%s", port), grpc.WithInsecure())
			assert.NoError(t, err)
			defer conn.Close()

			client := pingpb.NewPingServiceClient(conn)

			// Send a Ping request
			req := &pingpb.PingRequest{Message: "Ping"}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			resp, err := client.Ping(ctx, req)
			assert.NoError(t, err)

			// Assert the response
			assert.Equal(t, "Pong", resp.Reply, "Expected Pong response")

			// Simulate sending the os.Interrupt signal to the subprocess
			err = cmd.Process.Signal(os.Interrupt)
			if err != nil {
				t.Fatalf("Failed to send interrupt signal to the subprocess: %v", err)
			}

			// Wait for the server (subprocess) to shut down
			select {
			case <-serverDone:
				fmt.Println("Server shutdown gracefully after receiving signal")
			case <-time.After(2 * time.Second):
				t.Fatalf("Server did not shutdown after receiving signal")
			}
		})
	}
}

func TestCmd(t *testing.T) {
	var testCaseId string
	// This is the function that will run in the subprocess
	if testCaseId = os.Getenv("TEST_CASE_ID"); testCaseId == "" {
		return
	}
	testCase := tests[testCaseId]

	s, err := NewServer(testCase.config)
	if err != nil {
		cmdutil.Exit(err)
	}

	testCase.f(s)

}
