// Copyright 2016-2023, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rpcCmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/common/util/cmdutil"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/rpcutil"
	"google.golang.org/grpc"
)

type RpcCmd struct {
	Flag *flag.FlagSet

	// Tracing is public in case we want to trace subcalls
	Tracing string

	EngineAddress string
}

type RpcCmdConfig struct {
	Flag *flag.FlagSet

	// Tracing is public in case we want to trace subcalls
	TracingName  string
	RootSpanName string
}

func (r *RpcCmd) setFlags(f *flag.FlagSet) {
	f.StringVar(&r.Tracing, "tracing", "", "Emit tracing to a Zipkin-compatible tracing endpoint")
}

func errW(err error) error {
	return fmt.Errorf("rpcCmd initializaion failed: %w", err)
}

func NewRpcCmd(c *RpcCmdConfig) (*RpcCmd, error) {

	r := RpcCmd{}

	// we parse flags with private flagset
	localFlag := flag.NewFlagSet("", flag.ContinueOnError)
	r.setFlags(localFlag)
	if err := localFlag.Parse(os.Args[1:]); err != nil {
		return nil, errW(err)
	}

	if r.Tracing != "" && (c.TracingName == "" || c.RootSpanName == "") {
		return nil, errW(fmt.Errorf("missing required tracing configuration: TracingName or RootSpanName"))
	}

	args := localFlag.Args()
	if len(args) == 0 {
		return nil, errW(fmt.Errorf("missing required engine RPC address argument"))
	}

	r.EngineAddress = args[0]

	// return to requester not parsed flag with registered rpc server related flags
	r.Flag = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	if c.Flag != nil {
		r.Flag = c.Flag
	}
	mock := RpcCmd{}
	mock.setFlags(localFlag)

	return &r, nil
}

type InitFunc func(*grpc.Server) error
type FinishFunc func()

func (r *RpcCmd) Run(iFunc InitFunc, fFunc FinishFunc) {
	var err error

	// ensure that we run finish function
	defer func() {
		fFunc()
		if err != nil {
			cmdutil.Exit(err)
		}
	}()

	cmdutil.InitTracing("pulumi-language-go", "pulumi-language-go", r.Tracing)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	// map the context Done channel to the rpcutil boolean cancel channel
	cancelChannel := make(chan bool)
	go func() {
		<-ctx.Done()
		cancel() // deregister handler so we don't catch another interrupt
		close(cancelChannel)
	}()
	err = rpcutil.Healthcheck(ctx, r.EngineAddress, 5*time.Minute, cancel)
	if err != nil {
		err = fmt.Errorf("Error starting server: %w\n", err)
		return
	}

	// Fire up a gRPC server, letting the kernel choose a free port.
	handle, err := rpcutil.ServeWithOptions(rpcutil.ServeOptions{
		Cancel:  cancelChannel,
		Init:    iFunc,
		Options: rpcutil.OpenTracingServerInterceptorOptions(nil),
	})
	if err != nil {
		err = fmt.Errorf("could not start language host RPC server: %w", err)
		return
	}

	// Otherwise, print out the port so that the spawner knows how to reach us.
	fmt.Fprintf(os.Stdout, "%d\n", handle.Port)

	// And finally wait for the server to stop serving.
	if err = <-handle.Done; err != nil {
		err = fmt.Errorf("could not start language host RPC server: %w", err)
	}
}
