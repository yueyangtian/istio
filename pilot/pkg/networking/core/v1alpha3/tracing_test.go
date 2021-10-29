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

package v1alpha3

import (
	"testing"

	envoy_config_core_v3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	tracingcfg "github.com/envoyproxy/go-control-plane/envoy/config/trace/v3"
	hpb "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	tracing "github.com/envoyproxy/go-control-plane/envoy/type/tracing/v3"
	xdstype "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	meshconfig "istio.io/api/mesh/v1alpha1"
	tpb "istio.io/api/telemetry/v1alpha1"
	"istio.io/istio/pilot/pkg/extensionproviders"
	"istio.io/istio/pilot/pkg/model"
	xdsfilters "istio.io/istio/pilot/pkg/xds/filters"
)

func TestConfigureTracing(t *testing.T) {
	clusterName := "testcluster"
	providerName := "foo"

	clusterLookupFn = func(push *model.PushContext, service string, port int) (hostname string, cluster string, err error) {
		return "testhost", clusterName, nil
	}
	defer func() {
		clusterLookupFn = extensionproviders.LookupCluster
	}()

	testcases := []struct {
		name      string
		opts      buildListenerOpts
		inSpec    *model.TracingConfig
		want      *hpb.HttpConnectionManager_Tracing
		wantRfCtx *xdsfilters.RouterFilterContext
	}{
		{
			name:      "no telemetry api",
			opts:      fakeOptsNoTelemetryAPI(),
			want:      fakeTracingConfigNoProvider(55.55, 13, append(defaultTracingTags(), fakeEnvTag)),
			wantRfCtx: nil,
		},
		{
			name:      "only telemetry api (no provider)",
			inSpec:    fakeTracingSpecNoProvider(99.999, false),
			opts:      fakeOptsOnlyZipkinTelemetryAPI(),
			want:      fakeTracingConfigNoProvider(99.999, 0, append(defaultTracingTags(), fakeEnvTag)),
			wantRfCtx: nil,
		},
		{
			name:      "only telemetry api (with provider)",
			inSpec:    fakeTracingSpec(fakeZipkin(), 99.999, false),
			opts:      fakeOptsOnlyZipkinTelemetryAPI(),
			want:      fakeTracingConfig(fakeZipkinProvider(clusterName, providerName), 99.999, 256, append(defaultTracingTags(), fakeEnvTag)),
			wantRfCtx: nil,
		},
		{
			name:      "both tracing enabled (no provider)",
			inSpec:    fakeTracingSpecNoProvider(99.999, false),
			opts:      fakeOptsMeshAndTelemetryAPI(true /* enable tracing */),
			want:      fakeTracingConfigNoProvider(99.999, 13, append(defaultTracingTags(), fakeEnvTag)),
			wantRfCtx: nil,
		},
		{
			name:      "both tracing disabled (no provider)",
			inSpec:    fakeTracingSpecNoProvider(99.999, false),
			opts:      fakeOptsMeshAndTelemetryAPI(false /* no enable tracing */),
			want:      fakeTracingConfigNoProvider(99.999, 13, append(defaultTracingTags(), fakeEnvTag)),
			wantRfCtx: nil,
		},
		{
			name:      "both tracing enabled (with provider)",
			inSpec:    fakeTracingSpec(fakeZipkin(), 99.999, false),
			opts:      fakeOptsMeshAndTelemetryAPI(true /* enable tracing */),
			want:      fakeTracingConfig(fakeZipkinProvider(clusterName, providerName), 99.999, 256, append(defaultTracingTags(), fakeEnvTag)),
			wantRfCtx: nil,
		},
		{
			name:      "both tracing disabled (with provider)",
			inSpec:    fakeTracingSpec(fakeZipkin(), 99.999, false),
			opts:      fakeOptsMeshAndTelemetryAPI(false /* no enable tracing */),
			want:      fakeTracingConfig(fakeZipkinProvider(clusterName, providerName), 99.999, 256, append(defaultTracingTags(), fakeEnvTag)),
			wantRfCtx: nil,
		},
		{
			name:      "basic config (with skywalking provicer)",
			inSpec:    fakeTracingSpec(fakeSkywalking(), 99.999, false),
			opts:      fakeOptsOnlySkywalkingTelemetryAPI(),
			want:      fakeTracingConfig(fakeSkywalkingProvider(clusterName, providerName), 99.999, 0, append(defaultTracingTags(), fakeEnvTag)),
			wantRfCtx: &xdsfilters.RouterFilterContext{StartChildSpan: true},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			hcm := &hpb.HttpConnectionManager{}
			gotRfCtx := configureTracingFromSpec(tc.inSpec, tc.opts, hcm)
			if diff := cmp.Diff(tc.want, hcm.Tracing, protocmp.Transform()); diff != "" {
				t.Fatalf("configureTracing returned unexpected diff (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(gotRfCtx, tc.wantRfCtx); diff != "" {
				t.Fatalf("got filter modifier context is unexpected diff (-want +got):\n%s", diff)
			}
		})
	}
}

func defaultTracingTags() []*tracing.CustomTag {
	return append(buildOptionalPolicyTags(),
		&tracing.CustomTag{
			Tag: "istio.canonical_revision",
			Type: &tracing.CustomTag_Literal_{
				Literal: &tracing.CustomTag_Literal{
					Value: "latest",
				},
			},
		},
		&tracing.CustomTag{
			Tag: "istio.canonical_service",
			Type: &tracing.CustomTag_Literal_{
				Literal: &tracing.CustomTag_Literal{
					Value: "unknown",
				},
			},
		},
		&tracing.CustomTag{
			Tag: "istio.mesh_id",
			Type: &tracing.CustomTag_Literal_{
				Literal: &tracing.CustomTag_Literal{
					Value: "unknown",
				},
			},
		},
		&tracing.CustomTag{
			Tag: "istio.namespace",
			Type: &tracing.CustomTag_Literal_{
				Literal: &tracing.CustomTag_Literal{
					Value: "default",
				},
			},
		})
}

func fakeOptsNoTelemetryAPI() buildListenerOpts {
	var opts buildListenerOpts
	opts.push = &model.PushContext{
		Mesh: &meshconfig.MeshConfig{
			EnableTracing: true,
		},
	}
	opts.proxy = &model.Proxy{
		Metadata: &model.NodeMetadata{
			ProxyConfig: &model.NodeMetaProxyConfig{
				Tracing: &meshconfig.Tracing{
					Sampling:         55.55,
					MaxPathTagLength: 13,
					CustomTags: map[string]*meshconfig.Tracing_CustomTag{
						"test": {
							Type: &meshconfig.Tracing_CustomTag_Environment{
								Environment: &meshconfig.Tracing_Environment{
									Name: "FOO",
								},
							},
						},
					},
				},
			},
		},
	}

	return opts
}

func fakeOptsOnlyZipkinTelemetryAPI() buildListenerOpts {
	var opts buildListenerOpts
	opts.push = &model.PushContext{
		Mesh: &meshconfig.MeshConfig{
			ExtensionProviders: []*meshconfig.MeshConfig_ExtensionProvider{
				{
					Name: "foo",
					Provider: &meshconfig.MeshConfig_ExtensionProvider_Zipkin{
						Zipkin: &meshconfig.MeshConfig_ExtensionProvider_ZipkinTracingProvider{
							Service:      "zipkin",
							Port:         9411,
							MaxTagLength: 256,
						},
					},
				},
			},
		},
	}
	opts.proxy = &model.Proxy{
		Metadata: &model.NodeMetadata{
			ProxyConfig: &model.NodeMetaProxyConfig{},
		},
	}

	return opts
}

func fakeZipkin() *meshconfig.MeshConfig_ExtensionProvider {
	return &meshconfig.MeshConfig_ExtensionProvider{
		Name: "foo",
		Provider: &meshconfig.MeshConfig_ExtensionProvider_Zipkin{
			Zipkin: &meshconfig.MeshConfig_ExtensionProvider_ZipkinTracingProvider{
				Service:      "zipkin",
				Port:         9411,
				MaxTagLength: 256,
			},
		},
	}
}

func fakeOptsMeshAndTelemetryAPI(enableTracing bool) buildListenerOpts {
	var opts buildListenerOpts
	opts.push = &model.PushContext{
		Mesh: &meshconfig.MeshConfig{
			EnableTracing: enableTracing,
			ExtensionProviders: []*meshconfig.MeshConfig_ExtensionProvider{
				{
					Name: "foo",
					Provider: &meshconfig.MeshConfig_ExtensionProvider_Zipkin{
						Zipkin: &meshconfig.MeshConfig_ExtensionProvider_ZipkinTracingProvider{
							Service:      "zipkin",
							Port:         9411,
							MaxTagLength: 256,
						},
					},
				},
			},
		},
	}
	opts.proxy = &model.Proxy{
		Metadata: &model.NodeMetadata{
			ProxyConfig: &model.NodeMetaProxyConfig{
				Tracing: &meshconfig.Tracing{
					Sampling:         55.55,
					MaxPathTagLength: 13,
					CustomTags: map[string]*meshconfig.Tracing_CustomTag{
						"test": {
							Type: &meshconfig.Tracing_CustomTag_Environment{
								Environment: &meshconfig.Tracing_Environment{
									Name: "FOO",
								},
							},
						},
					},
				},
			},
		},
	}

	return opts
}

func fakeSkywalking() *meshconfig.MeshConfig_ExtensionProvider {
	return &meshconfig.MeshConfig_ExtensionProvider{
		Name: "foo",
		Provider: &meshconfig.MeshConfig_ExtensionProvider_Skywalking{
			Skywalking: &meshconfig.MeshConfig_ExtensionProvider_SkyWalkingTracingProvider{
				Service: "skywalking-oap.istio-system.svc.cluster.local",
				Port:    11800,
			},
		},
	}
}

func fakeOptsOnlySkywalkingTelemetryAPI() buildListenerOpts {
	var opts buildListenerOpts
	opts.push = &model.PushContext{
		Mesh: &meshconfig.MeshConfig{
			ExtensionProviders: []*meshconfig.MeshConfig_ExtensionProvider{
				{
					Name: "foo",
					Provider: &meshconfig.MeshConfig_ExtensionProvider_Skywalking{
						Skywalking: &meshconfig.MeshConfig_ExtensionProvider_SkyWalkingTracingProvider{
							Service: "skywalking-oap.istio-system.svc.cluster.local",
							Port:    11800,
						},
					},
				},
			},
		},
	}
	opts.proxy = &model.Proxy{
		Metadata: &model.NodeMetadata{
			ProxyConfig: &model.NodeMetaProxyConfig{},
		},
	}

	return opts
}

func fakeTracingSpecNoProvider(sampling float64, disableReporting bool) *model.TracingConfig {
	return fakeTracingSpec(nil, sampling, disableReporting)
}

func fakeTracingSpec(provider *meshconfig.MeshConfig_ExtensionProvider, sampling float64, disableReporting bool) *model.TracingConfig {
	t := &model.TracingConfig{
		Provider:                 provider,
		Disabled:                 disableReporting,
		RandomSamplingPercentage: sampling,
		CustomTags: map[string]*tpb.Tracing_CustomTag{
			"test": {
				Type: &tpb.Tracing_CustomTag_Environment{
					Environment: &tpb.Tracing_Environment{
						Name: "FOO",
					},
				},
			},
		},
	}
	return t
}

func fakeTracingConfigNoProvider(randomSampling float64, maxLen uint32, tags []*tracing.CustomTag) *hpb.HttpConnectionManager_Tracing {
	return fakeTracingConfig(nil, randomSampling, maxLen, tags)
}

func fakeTracingConfig(provider *tracingcfg.Tracing_Http, randomSampling float64, maxLen uint32, tags []*tracing.CustomTag) *hpb.HttpConnectionManager_Tracing {
	t := &hpb.HttpConnectionManager_Tracing{
		ClientSampling: &xdstype.Percent{
			Value: 100.0,
		},
		OverallSampling: &xdstype.Percent{
			Value: 100.0,
		},
		RandomSampling: &xdstype.Percent{
			Value: randomSampling,
		},
		CustomTags: tags,
	}
	if maxLen != 0 {
		t.MaxPathTagLength = wrapperspb.UInt32(maxLen)
	}
	if provider != nil {
		t.Provider = provider
	}
	return t
}

var fakeEnvTag = &tracing.CustomTag{
	Tag: "test",
	Type: &tracing.CustomTag_Environment_{
		Environment: &tracing.CustomTag_Environment{
			Name: "FOO",
		},
	},
}

func fakeZipkinProvider(expectClusterName, expectProviderName string) *tracingcfg.Tracing_Http {
	fakeZipkinProviderConfig := &tracingcfg.ZipkinConfig{
		CollectorCluster:         expectClusterName,
		CollectorEndpoint:        "/api/v2/spans",
		CollectorEndpointVersion: tracingcfg.ZipkinConfig_HTTP_JSON,
		TraceId_128Bit:           true,
		SharedSpanContext:        wrapperspb.Bool(false),
	}
	fakeZipkinAny, _ := anypb.New(fakeZipkinProviderConfig)
	return &tracingcfg.Tracing_Http{
		Name:       expectProviderName,
		ConfigType: &tracingcfg.Tracing_Http_TypedConfig{TypedConfig: fakeZipkinAny},
	}
}

func fakeSkywalkingProvider(expectClusterName, expectProviderName string) *tracingcfg.Tracing_Http {
	fakeSkywalkingProviderConfig := &tracingcfg.SkyWalkingConfig{
		GrpcService: &envoy_config_core_v3.GrpcService{
			TargetSpecifier: &envoy_config_core_v3.GrpcService_EnvoyGrpc_{
				EnvoyGrpc: &envoy_config_core_v3.GrpcService_EnvoyGrpc{
					ClusterName: expectClusterName,
				},
			},
		},
	}
	fakeSkywalkingAny, _ := anypb.New(fakeSkywalkingProviderConfig)
	return &tracingcfg.Tracing_Http{
		Name:       expectProviderName,
		ConfigType: &tracingcfg.Tracing_Http_TypedConfig{TypedConfig: fakeSkywalkingAny},
	}
}
