package rpcCmd

import (
	"bufio"
	"context"
	"fmt"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/cmdutil"
	"github.com/spf13/pflag"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"net"
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

	ENGINE_ADDR = "ENGINE_ADDR"
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

func findPluginPathValue(args []string) (bool, string) {
	flagSet := pflag.NewFlagSet("", pflag.ContinueOnError)
	flagSet.ParseErrorsWhitelist.UnknownFlags = true
	flagSet.Parse(args)
	if len(flagSet.Args()) >= 2 {
		return true, flagSet.Args()[1]
	}

	return false, ""
}

var standardFunc = func(s *Server) {
	s.Run(func(server *grpc.Server) error {
		pingpb.RegisterPingServiceServer(server, &PingServer{s: s})
		return nil
	})
}

var tests = map[string]struct {
	config Config
	give   []string

	f func(s *Server)

	timeOutBefore    time.Duration
	checkHealthCheck bool
}{
	"simplest_run": {
		config: Config{},
		give:   []string{ENGINE_ADDR},
		f:      standardFunc,
	},
	"run_with_tracing_plugin_path": {
		config: Config{HealthcheckD: time.Minute},
		give:   []string{ENGINE_ADDR, pluginPath, tracingFlag, "localhost:8989"},
		f:      standardFunc,
	},
	"engine_stopped_healtcheck_shutdown": {
		config:           Config{HealthcheckD: 500 * time.Millisecond},
		give:             []string{ENGINE_ADDR},
		f:                standardFunc,
		timeOutBefore:    2 * time.Second,
		checkHealthCheck: true,
	},
	"healtcheck_valid": {
		config:        Config{HealthcheckD: 500 * time.Millisecond},
		give:          []string{ENGINE_ADDR},
		f:             standardFunc,
		timeOutBefore: 10 * time.Second,
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

func checkExitCode(t *testing.T, err error) {
	if exitError, ok := err.(*exec.ExitError); ok {
		if exitError.ExitCode() != 0 {
			t.Fatalf("Subprocess exited with non-zero exit code: %d", exitError.ExitCode())
		}
	} else if err != nil {
		t.Fatalf("Subprocess finished with error: %v", err)
	}
}

func substituteArg(args []string, sub, val string) []string {
	for i := range args {
		if args[i] == sub {
			args[i] = val
		}
	}
	return args
}

func TestSubprocessExit1(t *testing.T) {
	for testCaseId, testCase := range tests {
		t.Run(fmt.Sprintf("Test Case %s", testCaseId), func(t *testing.T) {

			engAddr, shutdownEngine := StartHealthCheckServer(t)
			defer shutdownEngine()
			substituteArg(testCase.give, ENGINE_ADDR, engAddr)

			// Run the test in a subprocess
			cmd := exec.Command(os.Args[0], append([]string{"-test.run=TestCmd"}, testCase.give...)...)
			cmd.Env = append(os.Environ(), "TEST_CASE_ID="+testCaseId)

			// Capture stdout dynamically
			stdoutPipe, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatalf("Failed to get stdout pipe: %v", err)
			}

			serverDone, notifyServerDone := context.WithCancel(context.Background())

			var errCmd error

			// Start the command
			go func() {
				if err := cmd.Start(); err != nil { // Use Start() instead of Run() here to avoid blocking
					t.Fatalf("Failed to start command: %v", err)
				}

				// Wait for the subprocess to finish
				errCmd = cmd.Wait()

				// Check the exit code
				notifyServerDone()
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

			if set, val := findPluginPathValue(testCase.give); set {
				RequestTheServer(t, client, pluginPathField, val)
			}

			if testCase.config.HealthcheckD != 0 {
				RequestTheServer(t, client, healthCheckIntervalField, testCase.config.HealthcheckD.String())
			}

			// in case we're testing healthCheck scenarios
			if testCase.timeOutBefore != 0 {
				if testCase.checkHealthCheck {
					shutdownEngine()
				}
				time.Sleep(testCase.timeOutBefore)
				if testCase.checkHealthCheck {
					assert.Error(t, serverDone.Err(), "the healthcheck had to be triggered in this scenario")
					checkExitCode(t, errCmd)
					return
				} else {
					assert.NoError(t, serverDone.Err(), "the healthcheck had to be passed in this scenario")
				}
			}

			// Simulate sending the os.Interrupt signal to the subprocess
			err = cmd.Process.Signal(os.Interrupt)
			if err != nil {
				t.Fatalf("Failed to send interrupt signal to the subprocess: %v", err)
			}

			// Wait for the server (subprocess) to shut down
			select {
			case <-serverDone.Done():
				fmt.Println("Server shutdown gracefully after receiving signal")
			case <-time.After(2 * time.Second):
				t.Fatalf("Server did not shutdown after receiving signal")
			}

			checkExitCode(t, errCmd)

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

// HealthCheckImpl

// HealthServer implements the grpc_health_v1.HealthServer interface
type HealthServer struct {
	grpc_health_v1.UnimplementedHealthServer
}

// Check returns the health status of the server
func (s *HealthServer) Check(ctx context.Context, req *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	if req.Service == "" {
		return &grpc_health_v1.HealthCheckResponse{
			Status: grpc_health_v1.HealthCheckResponse_SERVING,
		}, nil
	}
	return nil, status.Errorf(codes.NotFound, "unknown service: %s", req.Service)
}

// Watch is not implemented for this simple example
func (s *HealthServer) Watch(req *grpc_health_v1.HealthCheckRequest, stream grpc_health_v1.Health_WatchServer) error {
	return status.Errorf(codes.Unimplemented, "Watch is not implemented")
}

func StartHealthCheckServer(t *testing.T) (string, func()) {
	// Create a new gRPC server
	grpcServer := grpc.NewServer()

	// Register the health service
	grpc_health_v1.RegisterHealthServer(grpcServer, &HealthServer{})

	// Listen on a TCP port
	listener, err := net.Listen("tcp", "")
	if err != nil {
		t.Fatalf("Failed to listen: %v\n", err)
	}

	// Start the server in a goroutine
	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			t.Fatalf("Failed to serve: %v\n", err)
		}
	}()

	return listener.Addr().String(), func() {
		grpcServer.GracefulStop()
	}

}
