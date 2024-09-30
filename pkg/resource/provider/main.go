// Copyright 2016-2018, Pulumi Corporation.
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

package provider

import (
	"fmt"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/logging"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/rpcCmd"
	"google.golang.org/grpc"

	"github.com/pulumi/pulumi/sdk/v3/go/common/util/cmdutil"
	pulumirpc "github.com/pulumi/pulumi/sdk/v3/proto/go"
)

// Tracing is the optional command line flag passed to this provider for configuring a  Zipkin-compatible tracing
// endpoint
var tracing string

// Main is the typical entrypoint for a resource provider plugin.  Using it isn't required but can cut down
// significantly on the amount of boilerplate necessary to fire up a new resource provider.
func Main(name string, provMaker func(*HostClient) (pulumirpc.ResourceProviderServer, error)) error {

	// Initialize loggers before going any further.
	logging.InitLogging(false, 0, false)

	rc, err := rpcCmd.NewServer(rpcCmd.Config{
		TracingName:  name,
		RootSpanName: name,
	})
	if err != nil {
		cmdutil.Exit(err)
	}

	var host *HostClient

	host, err = NewHostClient(rc.GetTracing())
	if err != nil {
		return fmt.Errorf("fatal: could not connect to host RPC: %w", err)
	}

	rc.Run(func(srv *grpc.Server) error {
		prov, proverr := provMaker(host)
		if proverr != nil {
			return fmt.Errorf("failed to create resource provider: %v", proverr)
		}
		pulumirpc.RegisterResourceProviderServer(srv, prov)
		return nil
	}, func() {})

	return nil
}
