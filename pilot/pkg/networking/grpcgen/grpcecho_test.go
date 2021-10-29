// Copyright Istio Authors
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
//
package grpcgen_test

import (
	"context"
	"fmt"
	"math"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	//  To install the xds resolvers and balancers.
	_ "google.golang.org/grpc/xds"

	networking "istio.io/api/networking/v1alpha3"
	"istio.io/istio/pilot/pkg/xds"
	"istio.io/istio/pkg/config"
	"istio.io/istio/pkg/config/protocol"
	"istio.io/istio/pkg/config/schema/collections"
	"istio.io/istio/pkg/test/echo/client"
	"istio.io/istio/pkg/test/echo/common"
	"istio.io/istio/pkg/test/echo/proto"
	"istio.io/istio/pkg/test/echo/server/endpoint"
	"istio.io/istio/pkg/test/util/retry"
)

const grpcEchoPort = 14058

type echoCfg struct {
	version   string
	namespace string
}

type configGenTest struct {
	*testing.T
	endpoints []endpoint.Instance
	ds        *xds.FakeDiscoveryServer
}

// newConfigGenTest creates a FakeDiscoveryServer that listens for gRPC on grpcXdsAddr
// For each of the given servers, we serve echo (only supporting Echo, no ForwardEcho) and
// create a corresponding WorkloadEntry. The WorkloadEntry will have the given format:
//
//    meta:
//      name: echo-{generated portnum}-{server.version}
//      namespace: {server.namespace or "default"}
//      labels: {"app": "grpc", "version": "{server.version}"}
//    spec:
//      address: {grpcEchoHost}
//      ports:
//        grpc: {generated portnum}
func newConfigGenTest(t *testing.T, discoveryOpts xds.FakeOptions, servers ...echoCfg) *configGenTest {
	if runtime.GOOS == "darwin" && len(servers) > 1 {
		// TODO always skip if this breaks anywhere else
		t.Skip("cannot use 127.0.0.2-255 on OSX without manual setup")
	}

	cgt := &configGenTest{T: t}
	wg := sync.WaitGroup{}
	for i, s := range servers {
		if s.namespace == "" {
			s.namespace = "default"
		}
		// TODO this breaks without extra ifonfig aliases on OSX, and probably elsewhere
		host := fmt.Sprintf("127.0.0.%d", i+1)
		nodeID := fmt.Sprintf("sidecar~%s~echo-%s.%s~cluster.local", host, s.version, s.namespace)
		bootstrapBytes, err := bootstrapForTest(nodeID, s.namespace)
		if err != nil {
			t.Fatal(err)
		}

		ep, err := endpoint.New(endpoint.Config{
			Port: &common.Port{
				Name:             "grpc",
				Port:             grpcEchoPort,
				Protocol:         protocol.GRPC,
				XDSServer:        true,
				XDSTestBootstrap: bootstrapBytes,
			},
			ListenerIP: host,
			Version:    s.version,
		})
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		if err := ep.Start(func() {
			wg.Done()
		}); err != nil {
			t.Fatal(err)
		}
		cgt.endpoints = append(cgt.endpoints, ep)
		discoveryOpts.Configs = append(discoveryOpts.Configs, makeWE(s, host, grpcEchoPort))
		t.Cleanup(func() {
			if err := ep.Close(); err != nil {
				t.Errorf("failed to close endpoint %s: %v", host, err)
			}
		})
	}
	discoveryOpts.ListenerBuilder = func() (net.Listener, error) {
		return net.Listen("tcp", grpcXdsAddr)
	}
	cgt.ds = xds.NewFakeDiscoveryServer(t, discoveryOpts)
	// we know onReady will get called because there are internal timeouts for this
	wg.Wait()
	return cgt
}

func makeWE(s echoCfg, host string, port int) config.Config {
	ns := "default"
	if s.namespace != "" {
		ns = s.namespace
	}
	return config.Config{
		Meta: config.Meta{
			Name:             fmt.Sprintf("echo-%d-%s", port, s.version),
			Namespace:        ns,
			GroupVersionKind: collections.IstioNetworkingV1Alpha3Workloadentries.Resource().GroupVersionKind(),
			Labels: map[string]string{
				"app":     "echo",
				"version": s.version,
			},
		},
		Spec: &networking.WorkloadEntry{
			Address: host,
			Ports:   map[string]uint32{"grpc": uint32(port)},
		},
	}
}

func (t *configGenTest) dialEcho(addr string) *client.Instance {
	resolver := resolverForTest(t, "default")
	out, err := client.New(addr, nil, grpc.WithResolvers(resolver))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTrafficShifting(t *testing.T) {
	tt := newConfigGenTest(t, xds.FakeOptions{
		KubernetesObjectString: `
apiVersion: v1
kind: Service
metadata:
  labels:
    app: echo-app
  name: echo-app
  namespace: default
spec:
  clusterIP: 1.2.3.4
  selector:
    app: echo
  ports:
  - name: grpc
    targetPort: grpc
    port: 7070
`,
		ConfigString: `
apiVersion: networking.istio.io/v1alpha3
kind: DestinationRule
metadata:
  name: echo-dr
  namespace: default
spec:
  host: echo-app.default.svc.cluster.local
  subsets:
    - name: v1
      labels:
        version: v1
    - name: v2
      labels:
        version: v2
---
apiVersion: networking.istio.io/v1alpha3
kind: VirtualService
metadata:
  name: echo-vs
  namespace: default
spec:
  hosts:
  - echo-app.default.svc.cluster.local
  http:
  - route:
    - destination:
        host: echo-app.default.svc.cluster.local
        subset: v1
      weight: 20
    - destination:
        host: echo-app.default.svc.cluster.local
        subset: v2
      weight: 80

`,
	}, echoCfg{version: "v1"}, echoCfg{version: "v2"})

	retry.UntilSuccessOrFail(tt.T, func() error {
		cw := tt.dialEcho("xds:///echo-app.default.svc.cluster.local:7070")
		distribution := map[string]int{}
		for i := 0; i < 100; i++ {
			res, err := cw.Echo(context.Background(), &proto.EchoRequest{Message: "needle"})
			if err != nil {
				return err
			}
			distribution[res.Version]++
		}

		if err := expectAlmost(distribution["v1"], 20); err != nil {
			return err
		}
		if err := expectAlmost(distribution["v2"], 80); err != nil {
			return err
		}
		return nil
	}, retry.Timeout(5*time.Second), retry.Delay(0))
}

func TestMtls(t *testing.T) {
	// TODO this is eagerly resolved in gRPC making it difficult to force with os.Setenv
	if !strings.EqualFold(os.Getenv("GRPC_XDS_EXPERIMENTAL_SECURITY_SUPPORT"), "true") {
		t.Skip("Must set GRPC_XDS_EXPERIMENTAL_SECURITY_SUPPORT outside the test")
	}
	tt := newConfigGenTest(t, xds.FakeOptions{
		KubernetesObjectString: `
apiVersion: v1
kind: Service
metadata:
  labels:
    app: echo-app
  name: echo-app
  namespace: default
spec:
  clusterIP: 1.2.3.4
  selector:
    app: echo
  ports:
  - name: grpc
    targetPort: grpc
    port: 7070
`,
		ConfigString: `
apiVersion: networking.istio.io/v1alpha3
kind: DestinationRule
metadata:
  name: echo-dr
  namespace: default
spec:
  host: echo-app.default.svc.cluster.local
  trafficPolicy:
    tls:
      mode: ISTIO_MUTUAL
---
apiVersion: security.istio.io/v1beta1
kind: PeerAuthentication
metadata:
  name: default
  namespace: default
spec:
  mtls:
    mode: STRICT
`,
	}, echoCfg{version: "v1"})

	// ensure we can make 10 consecutive successful requests
	retry.UntilSuccessOrFail(tt.T, func() error {
		cw := tt.dialEcho("xds:///echo-app.default.svc.cluster.local:7070")
		for i := 0; i < 10; i++ {
			_, err := cw.Echo(context.Background(), &proto.EchoRequest{Message: "needle"})
			if err != nil {
				return err
			}
		}
		return nil
	}, retry.Timeout(5*time.Second), retry.Delay(0))
}

func TestFault(t *testing.T) {
	tt := newConfigGenTest(t, xds.FakeOptions{
		KubernetesObjectString: `
apiVersion: v1
kind: Service
metadata:
  labels:
    app: echo-app
  name: echo-app
  namespace: default
spec:
  clusterIP: 1.2.3.4
  selector:
    app: echo
  ports:
  - name: grpc
    targetPort: grpc
    port: 7070
`,
		ConfigString: `
apiVersion: networking.istio.io/v1alpha3
kind: VirtualService
metadata:
  name: echo-delay
spec:
  hosts:
  - echo-app.default.svc.cluster.local
  http:
  - fault:
      delay:
        percent: 100
        fixedDelay: 1s
    route:
    - destination:
        host: echo-app.default.svc.cluster.local
`,
	}, echoCfg{version: "v1"})
	c := tt.dialEcho("xds:///echo-app.default.svc.cluster.local:7070")

	// without a delay it usually takes ~500us
	st := time.Now()
	_, err := c.Echo(context.Background(), &proto.EchoRequest{})
	duration := time.Since(st)
	if err != nil {
		t.Fatal(err)
	}
	if duration < time.Second {
		t.Fatalf("expected to take over 1s but took %v", duration)
	}

	// TODO test timeouts, aborts
}

func expectAlmost(got, want int) error {
	if math.Abs(float64(want-got)) > 10 {
		return fmt.Errorf("expected within %d of %d but got %d", 10, want, got)
	}
	return nil
}
