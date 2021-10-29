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

// Tool to generate pilot/pkg/config/kube/crdclient/types.gen.go
// Example run command:
// REPO_ROOT=`pwd` go generate ./pilot/pkg/config/kube/crdclient/...
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"log"
	"os"
	"path"
	"text/template"

	"istio.io/istio/pkg/config/schema/collection"
	"istio.io/istio/pkg/config/schema/collections"
	"istio.io/istio/pkg/config/schema/gvk"
	"istio.io/istio/pkg/test/env"
)

// ConfigData is data struct to feed to types.go template.
type ConfigData struct {
	Namespaced      bool
	VariableName    string
	APIImport       string
	ClientImport    string
	ClientGroupPath string
	ClientTypePath  string
	Kind            string
	StatusAPIImport string
	StatusKind      string

	// Support gateway-api, which require a custom client and the Spec suffix
	Client     string
	TypeSuffix string
}

// MakeConfigData prepare data for code generation for the given schema.
func MakeConfigData(schema collection.Schema) ConfigData {
	out := ConfigData{
		Namespaced:      !schema.Resource().IsClusterScoped(),
		VariableName:    schema.VariableName(),
		APIImport:       apiImport[schema.Resource().ProtoPackage()],
		ClientImport:    clientGoImport[schema.Resource().ProtoPackage()],
		ClientGroupPath: clientGoAccessPath[schema.Resource().ProtoPackage()],
		ClientTypePath:  clientGoTypePath[schema.Resource().Plural()],
		Kind:            schema.Resource().Kind(),
		Client:          "ic",
		StatusAPIImport: apiImport[schema.Resource().StatusPackage()],
		StatusKind:      schema.Resource().StatusKind(),
	}
	if schema.Resource().Group() == gvk.GatewayClass.Group {
		out.Client = "sc"
		out.TypeSuffix = "Spec"
	}
	log.Printf("Generating Istio type %s for %s/%s CRD\n", out.VariableName, out.APIImport, out.Kind)
	return out
}

var (
	// Mapping from istio/api path import to api import path
	apiImport = map[string]string{
		"istio.io/api/networking/v1alpha3":      "networkingv1alpha3",
		"istio.io/api/networking/v1beta1":       "networkingv1beta1",
		"istio.io/api/security/v1beta1":         "securityv1beta1",
		"istio.io/api/telemetry/v1alpha1":       "telemetryv1alpha1",
		"sigs.k8s.io/gateway-api/apis/v1alpha2": "gatewayv1alpha2",
		"istio.io/api/meta/v1alpha1":            "metav1alpha1",
		"istio.io/api/extensions/v1alpha1":      "extensionsv1alpha1",
	}
	// Mapping from istio/api path import to client go import path
	clientGoImport = map[string]string{
		"istio.io/api/networking/v1alpha3":      "clientnetworkingv1alpha3",
		"istio.io/api/networking/v1beta1":       "clientnetworkingv1beta1",
		"istio.io/api/security/v1beta1":         "clientsecurityv1beta1",
		"istio.io/api/telemetry/v1alpha1":       "clienttelemetryv1alpha1",
		"sigs.k8s.io/gateway-api/apis/v1alpha2": "gatewayv1alpha2",
		"istio.io/api/extensions/v1alpha1":      "clientextensionsv1alpha1",
	}
	// Translates an api import path to the top level path in client-go
	clientGoAccessPath = map[string]string{
		"istio.io/api/networking/v1alpha3":      "NetworkingV1alpha3",
		"istio.io/api/networking/v1beta1":       "NetworkingV1beta1",
		"istio.io/api/security/v1beta1":         "SecurityV1beta1",
		"istio.io/api/telemetry/v1alpha1":       "TelemetryV1alpha1",
		"sigs.k8s.io/gateway-api/apis/v1alpha2": "GatewayV1alpha2",
		"istio.io/api/extensions/v1alpha1":      "ExtensionsV1alpha1",
	}
	// Translates a plural type name to the type path in client-go
	// TODO: can we automatically derive this? I don't think we can, its internal to the kubegen
	clientGoTypePath = map[string]string{
		"destinationrules":       "DestinationRules",
		"envoyfilters":           "EnvoyFilters",
		"gateways":               "Gateways",
		"serviceentries":         "ServiceEntries",
		"sidecars":               "Sidecars",
		"proxyconfigs":           "ProxyConfigs",
		"virtualservices":        "VirtualServices",
		"workloadentries":        "WorkloadEntries",
		"workloadgroups":         "WorkloadGroups",
		"authorizationpolicies":  "AuthorizationPolicies",
		"peerauthentications":    "PeerAuthentications",
		"requestauthentications": "RequestAuthentications",
		"gatewayclasses":         "GatewayClasses",
		"httproutes":             "HTTPRoutes",
		"tcproutes":              "TCPRoutes",
		"tlsroutes":              "TLSRoutes",
		"referencepolicies":      "ReferencePolicies",
		"telemetries":            "Telemetries",
		"wasmplugins":            "WasmPlugins",
	}
)

func main() {
	templateFile := flag.String("template", path.Join(env.IstioSrc, "pilot/pkg/config/kube/crdclient/gen/types.go.tmpl"), "Template file")
	outputFile := flag.String("output", "", "Output file. Leave blank to go to stdout")
	flag.Parse()

	tmpl := template.Must(template.ParseFiles(*templateFile))

	// Prepare to generate types for mock schema and all Istio schemas
	typeList := []ConfigData{}
	for _, s := range collections.PilotGatewayAPI.All() {
		typeList = append(typeList, MakeConfigData(s))
	}
	var buffer bytes.Buffer
	if err := tmpl.Execute(&buffer, typeList); err != nil {
		log.Fatal(fmt.Errorf("template: %v", err))
	}

	// Format source code.
	out, err := format.Source(buffer.Bytes())
	if err != nil {
		log.Fatal(err)
	}
	// Output
	if outputFile == nil || *outputFile == "" {
		fmt.Println(string(out))
	} else if err := os.WriteFile(*outputFile, out, 0o644); err != nil {
		panic(err)
	}
}
