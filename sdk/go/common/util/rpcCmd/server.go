package rpcCmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/common/util/cmdutil"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/rpcutil"
	"github.com/spf13/pflag"
	"google.golang.org/grpc"
)

const DefaultHealthCheck = 5 * time.Minute

type Server struct {
	// Flag is the FlagSet containing registered rpcCmd flags. By default, it's set to pflag.ExitOnError.
	// If a Flag is provided in the config, that one is used, but with rpcCmd flags registered as well.
	Flag *pflag.FlagSet

	// tracing is a Zipkin-compatible tracing endpoint.
	tracing string

	// engineAddr  is the address to access the Pulumi host.
	engineAddr string

	// pluginPath is the path to the plugin source.
	pluginPath string

	config      Config
	grpcOptions []grpc.ServerOption

	// only for testing
	handle        rpcutil.ServeHandle
	cancelChannel chan bool
}

type Config struct {
	// Flag allows specifying a custom FlagSet if behavior different from the default flag.ExitOnError is required.
	Flag *pflag.FlagSet

	// TracingName and RootSpanName are required if tracing is enabled.
	TracingName  string
	RootSpanName string

	// Healthcheck interval duration.
	HealthcheckD time.Duration
}

// errW wraps an error with a message.
func errW(err error) error {
	return fmt.Errorf("rpcCmd initialization failed: %w", err)
}

// NewServer creates a new instance of Server.
func NewServer(c Config) (*Server, error) {

	s := &Server{config: c}

	// Server parses flags with a private instance of FlagSet.
	s.Flag = pflag.NewFlagSet("", pflag.ContinueOnError)
	// Filter out unknown flags, caller can register any flags later
	s.Flag.ParseErrorsWhitelist.UnknownFlags = true
	s.registerFlags()
	if err := s.Flag.Parse(os.Args[1:]); err != nil {
		return nil, errW(err)
	}
	// Set arguments.
	args := s.Flag.Args()
	if len(args) == 0 {
		return nil, errW(fmt.Errorf("missing required engine RPC address argument"))
	}
	s.engineAddr = args[0]

	// plugin path is the third argument.
	if len(args) >= 2 {
		s.pluginPath = args[1]
	}

	// rpcCmd has already parsed private flags; it needs to register them again for parsing on the caller side.
	s.Flag = getMockFlagSet(s.config.Flag)

	return s, nil
}

func getMockFlagSet(f *pflag.FlagSet) *pflag.FlagSet {
	s := Server{}
	s.Flag = pflag.NewFlagSet(os.Args[0], pflag.ExitOnError)
	if f != nil {
		s.Flag = f
	}
	s.registerFlags()
	return s.Flag
}

// registerFlags registers flags related to RPC server logic.
func (s *Server) registerFlags() {
	s.Flag.StringVar(&s.tracing, "tracing", "", "Emit tracing to a Zipkin-compatible tracing endpoint")
}

// getHealthcheckD returns the health check duration.
func (s *Server) getHealthcheckD() time.Duration {
	if s.config.HealthcheckD != 0 {
		return s.config.HealthcheckD
	}
	return DefaultHealthCheck
}

// InitFunc defines the type of function passed to rpcutil.ServeWithOptions.
type InitFunc func(*grpc.Server) error
type FinishFunc func()

// Run executes the RPC command.
func (s *Server) Run(iFunc InitFunc, fFunc FinishFunc) {
	var err error

	// Ensure the finish function is executed.
	// Do not intercept panic; this runs as a separate command so the panic will be shown.
	defer func() {
		fFunc()
		if err != nil {
			cmdutil.Exit(err)
		}
	}()

	if s.config.TracingName != "" {
		// TracingName and RootSpanName are required if tracing is enabled.
		if s.config.TracingName == "" || s.config.RootSpanName == "" {
			err = errW(fmt.Errorf("missing required tracing configuration: TracingName or RootSpanName. " +
				"Provide them in Config"))
		}
		cmdutil.InitTracing(s.config.TracingName, s.config.RootSpanName, s.GetTracing())
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	// Map the context's Done channel to the rpcutil boolean cancel channel.
	s.cancelChannel = make(chan bool)
	go func() {
		<-ctx.Done()
		cancel() // Deregister handler so we don't catch another interrupt.
		close(s.cancelChannel)
	}()
	err = rpcutil.Healthcheck(ctx, s.engineAddr, s.getHealthcheckD(), cancel)
	if err != nil {
		err = fmt.Errorf("Error starting server: %w\n", err)
		return
	}

	// Fire up a gRPC server, letting the kernel choose a free port.
	s.handle, err = rpcutil.ServeWithOptions(rpcutil.ServeOptions{
		Cancel:  s.cancelChannel,
		Init:    iFunc,
		Options: s.getGrpcOptions(),
	})
	if err != nil {
		err = fmt.Errorf("could not start language host RPC server: %w", err)
		return
	}

	// Print the port so that the spawner knows how to reach the server.
	fmt.Fprintf(os.Stdout, "%d\n", s.handle.Port)

	// Wait for the server to stop serving. If an error occurs, it will be handled in defer.
	if err = <-s.handle.Done; err != nil {
		err = fmt.Errorf("could not start language host RPC server: %w", err)
	}
}

// GetEngineAddress returns the engine address for the server.
func (s *Server) GetEngineAddress() string {
	return s.engineAddr
}

// GetPluginPath returns the plugin path for the server.
func (s *Server) GetPluginPath() string {
	return s.pluginPath
}

// GetTracing returns the tracing endpoint.
func (s *Server) GetTracing() string {
	return s.tracing
}

// getGrpcOptions returns gRPC server options.
// tip: if caller wants suppress opentracing options but don't want to provide other option
// tip: then it's possible to send array with one mock grpc.ServerOption implementation
func (s *Server) getGrpcOptions() []grpc.ServerOption {
	if len(s.grpcOptions) == 0 {
		return rpcutil.OpenTracingServerInterceptorOptions(nil)
	}
	return s.grpcOptions
}

// SetGrpcOptions sets gRPC server options.
func (s *Server) SetGrpcOptions(opts []grpc.ServerOption) {
	s.grpcOptions = opts
}

// SetTracingNames sets TracingName and RootSpanName
func (s *Server) SetTracingNames(tracingName, rootSpanName string) {
	s.config.RootSpanName = rootSpanName
	s.config.TracingName = tracingName
}
