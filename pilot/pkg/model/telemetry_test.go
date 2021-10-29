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

package model

import (
	"reflect"
	"testing"

	listener "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	httpwasm "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/wasm/v3"
	httppb "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	wasmfilter "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/wasm/v3"
	"github.com/gogo/protobuf/types"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/types/known/wrapperspb"

	meshconfig "istio.io/api/mesh/v1alpha1"
	tpb "istio.io/api/telemetry/v1alpha1"
	"istio.io/istio/pilot/pkg/networking"
	"istio.io/istio/pkg/config"
	"istio.io/istio/pkg/config/mesh"
	"istio.io/istio/pkg/config/schema/collection"
	"istio.io/istio/pkg/config/schema/collections"
)

func createTestTelemetries(configs []config.Config, t *testing.T) *Telemetries {
	t.Helper()

	store := &telemetryStore{}
	for _, cfg := range configs {
		store.add(cfg)
	}
	m := mesh.DefaultMeshConfig()
	environment := &Environment{
		IstioConfigStore: MakeIstioStore(store),
		Watcher:          mesh.NewFixedWatcher(&m),
	}
	telemetries, err := getTelemetries(environment)
	if err != nil {
		t.Fatalf("getTelemetries failed: %v", err)
	}
	return telemetries
}

func newTelemetry(ns string, spec config.Spec) config.Config {
	return config.Config{
		Meta: config.Meta{
			GroupVersionKind: collections.IstioTelemetryV1Alpha1Telemetries.Resource().GroupVersionKind(),
			Name:             "default",
			Namespace:        ns,
		},
		Spec: spec,
	}
}

type telemetryStore struct {
	ConfigStore

	data []struct {
		typ config.GroupVersionKind
		ns  string
		cfg config.Config
	}
}

func (ts *telemetryStore) add(cfg config.Config) {
	ts.data = append(ts.data, struct {
		typ config.GroupVersionKind
		ns  string
		cfg config.Config
	}{
		typ: cfg.GroupVersionKind,
		ns:  cfg.Namespace,
		cfg: cfg,
	})
}

func (ts *telemetryStore) Schemas() collection.Schemas {
	return collection.SchemasFor()
}

func (ts *telemetryStore) Get(_ config.GroupVersionKind, _, _ string) *config.Config {
	return nil
}

func (ts *telemetryStore) List(typ config.GroupVersionKind, namespace string) ([]config.Config, error) {
	var configs []config.Config
	for _, data := range ts.data {
		if data.typ == typ {
			if namespace != "" && data.ns == namespace {
				continue
			}
			configs = append(configs, data.cfg)
		}
	}
	return configs, nil
}

func TestAccessLogging(t *testing.T) {
	sidecar := &Proxy{ConfigNamespace: "default", Metadata: &NodeMetadata{Labels: map[string]string{"app": "test"}}}
	envoy := &tpb.Telemetry{
		AccessLogging: []*tpb.AccessLogging{
			{
				Providers: []*tpb.ProviderRef{
					{
						Name: "envoy",
					},
				},
			},
		},
	}
	stackdriver := &tpb.Telemetry{
		AccessLogging: []*tpb.AccessLogging{
			{
				Providers: []*tpb.ProviderRef{
					{
						Name: "stackdriver",
					},
				},
			},
		},
	}
	empty := &tpb.Telemetry{
		AccessLogging: []*tpb.AccessLogging{{}},
	}
	disabled := &tpb.Telemetry{
		AccessLogging: []*tpb.AccessLogging{
			{
				Disabled: &types.BoolValue{Value: true},
			},
		},
	}
	nonExistant := &tpb.Telemetry{
		AccessLogging: []*tpb.AccessLogging{
			{
				Providers: []*tpb.ProviderRef{
					{
						Name: "custom-provider",
					},
				},
			},
		},
	}
	tests := []struct {
		name             string
		cfgs             []config.Config
		proxy            *Proxy
		defaultProviders []string
		want             []string
	}{
		{
			"empty",
			nil,
			sidecar,
			nil,
			nil,
		},
		{
			"default provider only",
			nil,
			sidecar,
			[]string{"envoy"},
			[]string{"envoy"},
		},
		{
			"provider only",
			[]config.Config{newTelemetry("istio-system", envoy)},
			sidecar,
			nil,
			[]string{"envoy"},
		},
		{
			"override default",
			[]config.Config{newTelemetry("istio-system", envoy)},
			sidecar,
			[]string{"stackdriver"},
			[]string{"envoy"},
		},
		{
			"override namespace",
			[]config.Config{newTelemetry("istio-system", envoy), newTelemetry("default", stackdriver)},
			sidecar,
			nil,
			[]string{"stackdriver"},
		},
		{
			"empty config inherits",
			[]config.Config{newTelemetry("istio-system", envoy), newTelemetry("default", empty)},
			sidecar,
			nil,
			[]string{"envoy"},
		},
		{
			"disable config",
			[]config.Config{newTelemetry("istio-system", envoy), newTelemetry("default", disabled)},
			sidecar,
			nil,
			[]string{},
		},
		{
			"disable default",
			[]config.Config{newTelemetry("default", disabled)},
			sidecar,
			[]string{"envoy"},
			[]string{},
		},
		{
			"non existing",
			[]config.Config{newTelemetry("default", nonExistant)},
			sidecar,
			[]string{"envoy"},
			[]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			telemetry := createTestTelemetries(tt.cfgs, t)
			telemetry.meshConfig.DefaultProviders.AccessLogging = tt.defaultProviders
			al := telemetry.AccessLogging(tt.proxy)
			var got []string
			if al != nil {
				got = []string{} // We distinguish between nil vs empty in the test
				for _, p := range al.Providers {
					got = append(got, p.Name)
				}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestTracing(t *testing.T) {
	sidecar := &Proxy{ConfigNamespace: "default", Metadata: &NodeMetadata{Labels: map[string]string{"app": "test"}}}
	envoy := &tpb.Telemetry{
		Tracing: []*tpb.Tracing{
			{
				Providers: []*tpb.ProviderRef{
					{
						Name: "envoy",
					},
				},
			},
		},
	}
	stackdriver := &tpb.Telemetry{
		Tracing: []*tpb.Tracing{
			{
				Providers: []*tpb.ProviderRef{
					{
						Name: "stackdriver",
					},
				},
			},
		},
	}
	empty := &tpb.Telemetry{
		Tracing: []*tpb.Tracing{{}},
	}
	disabled := &tpb.Telemetry{
		Tracing: []*tpb.Tracing{
			{
				DisableSpanReporting: &types.BoolValue{Value: true},
			},
		},
	}
	overidesA := &tpb.Telemetry{
		Tracing: []*tpb.Tracing{
			{
				RandomSamplingPercentage: &types.DoubleValue{Value: 50.0},
				CustomTags: map[string]*tpb.Tracing_CustomTag{
					"foo": {},
					"bar": {},
				},
			},
		},
	}
	overidesB := &tpb.Telemetry{
		Tracing: []*tpb.Tracing{
			{
				RandomSamplingPercentage: &types.DoubleValue{Value: 80.0},
				CustomTags: map[string]*tpb.Tracing_CustomTag{
					"foo": {},
					"baz": {},
				},
			},
		},
	}
	nonExistant := &tpb.Telemetry{
		Tracing: []*tpb.Tracing{
			{
				Providers: []*tpb.ProviderRef{
					{
						Name: "custom-provider",
					},
				},
			},
		},
	}
	tests := []struct {
		name             string
		cfgs             []config.Config
		proxy            *Proxy
		defaultProviders []string
		want             *TracingConfig
	}{
		{
			"empty",
			nil,
			sidecar,
			nil,
			nil,
		},
		{
			"default provider only",
			nil,
			sidecar,
			[]string{"envoy"},
			&TracingConfig{Provider: &meshconfig.MeshConfig_ExtensionProvider{Name: "envoy"}},
		},
		{
			"provider only",
			[]config.Config{newTelemetry("istio-system", envoy)},
			sidecar,
			nil,
			&TracingConfig{Provider: &meshconfig.MeshConfig_ExtensionProvider{Name: "envoy"}},
		},
		{
			"override default",
			[]config.Config{newTelemetry("istio-system", envoy)},
			sidecar,
			[]string{"stackdriver"},
			&TracingConfig{Provider: &meshconfig.MeshConfig_ExtensionProvider{Name: "envoy"}},
		},
		{
			"override namespace",
			[]config.Config{newTelemetry("istio-system", envoy), newTelemetry("default", stackdriver)},
			sidecar,
			nil,
			&TracingConfig{Provider: &meshconfig.MeshConfig_ExtensionProvider{Name: "stackdriver"}},
		},
		{
			"empty config inherits",
			[]config.Config{newTelemetry("istio-system", envoy), newTelemetry("default", empty)},
			sidecar,
			nil,
			&TracingConfig{Provider: &meshconfig.MeshConfig_ExtensionProvider{Name: "envoy"}},
		},
		{
			"disable config",
			[]config.Config{newTelemetry("istio-system", envoy), newTelemetry("default", disabled)},
			sidecar,
			nil,
			&TracingConfig{Provider: &meshconfig.MeshConfig_ExtensionProvider{Name: "envoy"}, Disabled: true},
		},
		{
			"disable default",
			[]config.Config{newTelemetry("default", disabled)},
			sidecar,
			[]string{"envoy"},
			&TracingConfig{Provider: &meshconfig.MeshConfig_ExtensionProvider{Name: "envoy"}, Disabled: true},
		},
		{
			"non existing",
			[]config.Config{newTelemetry("default", nonExistant)},
			sidecar,
			[]string{"envoy"},
			&TracingConfig{Disabled: true},
		},
		{
			"overrides",
			[]config.Config{newTelemetry("istio-system", overidesA)},
			sidecar,
			[]string{"envoy"},
			&TracingConfig{
				Provider:                 &meshconfig.MeshConfig_ExtensionProvider{Name: "envoy"},
				RandomSamplingPercentage: 50.0,
				CustomTags: map[string]*tpb.Tracing_CustomTag{
					"foo": {},
					"bar": {},
				},
			},
		},
		{
			"multi overrides",
			[]config.Config{
				newTelemetry("istio-system", overidesA),
				newTelemetry("default", overidesB),
			},
			sidecar,
			[]string{"envoy"},
			&TracingConfig{
				Provider:                 &meshconfig.MeshConfig_ExtensionProvider{Name: "envoy"},
				RandomSamplingPercentage: 80,
				CustomTags: map[string]*tpb.Tracing_CustomTag{
					"foo": {},
					"baz": {},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			telemetry := createTestTelemetries(tt.cfgs, t)
			telemetry.meshConfig.DefaultProviders.Tracing = tt.defaultProviders
			got := telemetry.Tracing(tt.proxy)
			if got != nil && got.Provider != nil {
				// We don't match on this, just the name for test simplicity
				got.Provider.Provider = nil
			}
			if diff := cmp.Diff(got, tt.want); diff != "" {
				t.Fatalf("got diff %v", diff)
			}
		})
	}
}

func TestTelemetryFilters(t *testing.T) {
	overrides := []*tpb.MetricsOverrides{{
		Match: &tpb.MetricSelector{
			MetricMatch: &tpb.MetricSelector_Metric{
				Metric: tpb.MetricSelector_REQUEST_COUNT,
			},
		},
		TagOverrides: map[string]*tpb.MetricsOverrides_TagOverride{
			"remove": {
				Operation: tpb.MetricsOverrides_TagOverride_REMOVE,
			},
			"add": {
				Operation: tpb.MetricsOverrides_TagOverride_UPSERT,
				Value:     "bar",
			},
		},
	}}
	sidecar := &Proxy{ConfigNamespace: "default", Metadata: &NodeMetadata{Labels: map[string]string{"app": "test"}}}
	emptyPrometheus := &tpb.Telemetry{
		Metrics: []*tpb.Metrics{
			{
				Providers: []*tpb.ProviderRef{{Name: "prometheus"}},
			},
		},
	}
	overridesPrometheus := &tpb.Telemetry{
		Metrics: []*tpb.Metrics{
			{
				Providers: []*tpb.ProviderRef{{Name: "prometheus"}},
				Overrides: overrides,
			},
		},
	}
	emptyStackdriver := &tpb.Telemetry{
		Metrics: []*tpb.Metrics{
			{
				Providers: []*tpb.ProviderRef{{Name: "stackdriver"}},
			},
		},
	}
	overridesStackdriver := &tpb.Telemetry{
		Metrics: []*tpb.Metrics{
			{
				Providers: []*tpb.ProviderRef{{Name: "stackdriver"}},
				Overrides: overrides,
			},
		},
	}
	overridesEmptyProvider := &tpb.Telemetry{
		Metrics: []*tpb.Metrics{
			{
				Overrides: overrides,
			},
		},
	}
	sdLogging := &tpb.Telemetry{
		AccessLogging: []*tpb.AccessLogging{
			{
				Providers: []*tpb.ProviderRef{{Name: "stackdriver"}},
			},
		},
	}
	emptyLogging := &tpb.Telemetry{
		AccessLogging: []*tpb.AccessLogging{
			{},
		},
	}
	tests := []struct {
		name             string
		cfgs             []config.Config
		proxy            *Proxy
		class            networking.ListenerClass
		protocol         networking.ListenerProtocol
		defaultProviders []string
		want             map[string]string
	}{
		{
			"empty",
			nil,
			sidecar,
			networking.ListenerClassSidecarOutbound,
			networking.ListenerProtocolHTTP,
			nil,
			map[string]string{},
		},
		{
			"default prometheus",
			[]config.Config{newTelemetry("istio-system", emptyPrometheus)},
			sidecar,
			networking.ListenerClassSidecarOutbound,
			networking.ListenerProtocolHTTP,
			nil,
			map[string]string{
				"istio.stats": "{}",
			},
		},
		{
			"default provider prometheus",
			[]config.Config{},
			sidecar,
			networking.ListenerClassSidecarOutbound,
			networking.ListenerProtocolHTTP,
			[]string{"prometheus"},
			map[string]string{
				"istio.stats": "{}",
			},
		},
		{
			"prometheus overrides",
			[]config.Config{newTelemetry("istio-system", overridesPrometheus)},
			sidecar,
			networking.ListenerClassSidecarOutbound,
			networking.ListenerProtocolHTTP,
			nil,
			map[string]string{
				"istio.stats": `{"metrics":[{"dimensions":{"add":"bar"},"name":"requests_total","tags_to_remove":["remove"]}]}`,
			},
		},
		{
			"prometheus overrides TCP",
			[]config.Config{newTelemetry("istio-system", overridesPrometheus)},
			sidecar,
			networking.ListenerClassSidecarOutbound,
			networking.ListenerProtocolTCP,
			nil,
			map[string]string{
				"istio.stats": `{"metrics":[{"dimensions":{"add":"bar"},"name":"requests_total","tags_to_remove":["remove"]}]}`,
			},
		},
		{
			"empty stackdriver",
			[]config.Config{newTelemetry("istio-system", emptyStackdriver)},
			sidecar,
			networking.ListenerClassSidecarOutbound,
			networking.ListenerProtocolHTTP,
			nil,
			map[string]string{
				"istio.stackdriver": `{}`,
			},
		},
		{
			"overrides stackdriver",
			[]config.Config{newTelemetry("istio-system", overridesStackdriver)},
			sidecar,
			networking.ListenerClassSidecarOutbound,
			networking.ListenerProtocolHTTP,
			nil,
			map[string]string{
				"istio.stackdriver": `{"metrics_overrides":{"client/request_count":{"tag_overrides":{"add":"bar"}}}}`,
			},
		},
		{
			"namespace empty merge",
			[]config.Config{
				newTelemetry("istio-system", emptyPrometheus),
				newTelemetry("default", emptyStackdriver),
			},
			sidecar,
			networking.ListenerClassSidecarOutbound,
			networking.ListenerProtocolHTTP,
			nil,
			map[string]string{
				"istio.stackdriver": `{}`,
			},
		},
		{
			"namespace overrides merge without provider",
			[]config.Config{
				newTelemetry("istio-system", emptyPrometheus),
				newTelemetry("default", overridesEmptyProvider),
			},
			sidecar,
			networking.ListenerClassSidecarOutbound,
			networking.ListenerProtocolHTTP,
			nil,
			map[string]string{
				"istio.stats": `{"metrics":[{"dimensions":{"add":"bar"},"name":"requests_total","tags_to_remove":["remove"]}]}`,
			},
		},
		{
			"namespace overrides merge with default provider",
			[]config.Config{
				newTelemetry("default", overridesEmptyProvider),
			},
			sidecar,
			networking.ListenerClassSidecarOutbound,
			networking.ListenerProtocolHTTP,
			[]string{"prometheus"},
			map[string]string{
				"istio.stats": `{"metrics":[{"dimensions":{"add":"bar"},"name":"requests_total","tags_to_remove":["remove"]}]}`,
			},
		},
		{
			"namespace overrides default provider",
			[]config.Config{
				newTelemetry("default", emptyStackdriver),
			},
			sidecar,
			networking.ListenerClassSidecarOutbound,
			networking.ListenerProtocolHTTP,
			[]string{"prometheus"},
			map[string]string{
				"istio.stackdriver": `{}`,
			},
		},
		{
			"stackdriver logging",
			[]config.Config{
				newTelemetry("default", sdLogging),
			},
			sidecar,
			networking.ListenerClassSidecarOutbound,
			networking.ListenerProtocolHTTP,
			nil,
			map[string]string{
				"istio.stackdriver": `{"access_logging":"ERRORS_ONLY"}`,
			},
		},
		{
			"stackdriver defaultProviders",
			[]config.Config{
				newTelemetry("default", emptyLogging),
			},
			sidecar,
			networking.ListenerClassSidecarInbound,
			networking.ListenerProtocolHTTP,
			[]string{"stackdriver"},
			map[string]string{
				"istio.stackdriver": `{"disable_host_header_fallback":true,"access_logging":"FULL"}`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			telemetry := createTestTelemetries(tt.cfgs, t)
			telemetry.meshConfig.DefaultProviders.Metrics = tt.defaultProviders
			telemetry.meshConfig.DefaultProviders.AccessLogging = tt.defaultProviders
			got := telemetry.telemetryFilters(tt.proxy, tt.class, tt.protocol)
			res := map[string]string{}
			http, ok := got.([]*httppb.HttpFilter)
			if ok {
				for _, f := range http {
					w := &httpwasm.Wasm{}

					if err := f.GetTypedConfig().UnmarshalTo(w); err != nil {
						t.Fatal(err)
					}
					cfg := &wrapperspb.StringValue{}
					if err := w.GetConfig().GetConfiguration().UnmarshalTo(cfg); err != nil {
						t.Fatal(err)
					}
					res[f.GetName()] = cfg.GetValue()
				}
			}
			tcp, ok := got.([]*listener.Filter)
			if ok {
				for _, f := range tcp {
					w := &wasmfilter.Wasm{}

					if err := f.GetTypedConfig().UnmarshalTo(w); err != nil {
						t.Fatal(err)
					}
					cfg := &wrapperspb.StringValue{}
					if err := w.GetConfig().GetConfiguration().UnmarshalTo(cfg); err != nil {
						t.Fatal(err)
					}
					res[f.GetName()] = cfg.GetValue()
				}
			}
			if diff := cmp.Diff(res, tt.want); diff != "" {
				t.Errorf("got diff: %v", diff)
			}
		})
	}
}
