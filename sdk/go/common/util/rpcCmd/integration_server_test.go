package rpcCmd

import (
	"bufio"
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

const (
	tracingFlag              = "--tracing"
	engineAddrField          = "EngineAddr"
	pluginPathField          = "PluginPath"
	healthCheckIntervalField = "HealthCheckInterval"
)

func findFlagValue(args []string, flag string) (bool, string) {
	// Iterate over the args slice to find the flag
	for i, arg := range args {
		if arg == flag {
			return true, args[i+1] // it will panic if test input is invalid
		}
	}
	return false, ""
}

var tests = map[string]struct {
	config Config
	give   []string

	f func(s *Server)
}{
	"simplest_run": {
		config: Config{},
		give:   []string{"localhost:"},
		f: func(s *Server) {
			s.Run(func(server *grpc.Server) error {
				pingpb.RegisterPingServiceServer(server, &PingServer{s: s})
				return nil
			})
		},
	},
	"run_with_tracing_plugin_path": {
		config: Config{HealthcheckD: time.Second},
		give:   []string{"localhost:", pluginPath, tracingFlag, "localhost:8989"},
		f: func(s *Server) {
			s.Run(func(server *grpc.Server) error {
				pingpb.RegisterPingServiceServer(server, &PingServer{s: s})
				return nil
			})
		},
	},
}

// PingServer implements the PingService.
type PingServer struct {
	pingpb.UnimplementedPingServiceServer

	s *Server
}

// Ping method returns a "Pong" response.
func (s *PingServer) Ping(ctx context.Context, req *pingpb.PingRequest) (*pingpb.PingResponse, error) {
	var msg string
	switch req.Message {
	case "Ping":
		msg = "Pong"
	case tracingFlag:
		msg = s.s.GetTracing()
	case engineAddrField:
		msg = s.s.GetEngineAddress()
	case pluginPathField:
		msg = s.s.GetPluginPath()
	case healthCheckIntervalField:
		msg = s.s.getHealthcheckD().String()
	}
	return &pingpb.PingResponse{Reply: msg}, nil
}

func RequestTheServer(t *testing.T, client pingpb.PingServiceClient, requested, expected string) {

	// Send a Ping request
	req := &pingpb.PingRequest{Message: requested}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	resp, err := client.Ping(ctx, req)
	assert.NoError(t, err)

	// Assert the response
	assert.Equal(t, expected, resp.Reply, fmt.Sprintf("for requested %s expected %s, got %s", requested,
		expected, resp.Reply))

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
					time.Sleep(time.Second)
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
			RequestTheServer(t, client, "Ping", "Pong")

			if set, val := findFlagValue(testCase.give, tracingFlag); set {
				RequestTheServer(t, client, tracingFlag, val)
			}

			if testCase.config.HealthcheckD != 0 {
				RequestTheServer(t, client, healthCheckIntervalField, testCase.config.HealthcheckD.String())
			}

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
