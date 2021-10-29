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

package controller

import (
	"reflect"
	"testing"
	"time"

	coreV1 "k8s.io/api/core/v1"
	mcs "sigs.k8s.io/mcs-api/pkg/apis/v1alpha1"

	"istio.io/api/label"
	"istio.io/istio/pilot/pkg/serviceregistry/kube"
	"istio.io/istio/pkg/config/labels"
)

func TestGetLocalityFromTopology(t *testing.T) {
	cases := []struct {
		name     string
		topology map[string]string
		locality string
	}{
		{
			"all standard kubernetes labels",
			map[string]string{
				NodeRegionLabelGA: "region",
				NodeZoneLabelGA:   "zone",
			},
			"region/zone",
		},
		{
			"all standard kubernetes labels and Istio custom labels",
			map[string]string{
				NodeRegionLabelGA:          "region",
				NodeZoneLabelGA:            "zone",
				label.TopologySubzone.Name: "subzone",
			},
			"region/zone/subzone",
		},
		{
			"missing zone",
			map[string]string{
				NodeRegionLabelGA: "region",
			},
			"region",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := getLocalityFromTopology(tt.topology)
			if !reflect.DeepEqual(tt.locality, got) {
				t.Fatalf("Expected %v, got %v", tt.topology, got)
			}
		})
	}
}

func TestEndpointSliceFromMCSShouldBeIgnored(t *testing.T) {
	const (
		ns      = "nsa"
		svcName = "svc1"
		appName = "prod-app"
	)

	controller, fx := NewFakeControllerWithOptions(FakeControllerOptions{Mode: EndpointSliceOnly})
	defer controller.Stop()

	node := generateNode("node1", map[string]string{
		NodeZoneLabel:              "zone1",
		NodeRegionLabel:            "region1",
		label.TopologySubzone.Name: "subzone1",
	})
	addNodes(t, controller, node)

	pod := generatePod("128.0.0.1", "pod1", ns, "svcaccount", "node1",
		map[string]string{"app": appName}, map[string]string{})
	pods := []*coreV1.Pod{pod}
	addPods(t, controller, fx, pods...)

	createService(controller, svcName, ns, nil,
		[]int32{8080}, map[string]string{"app": appName}, t)
	if ev := fx.Wait("service"); ev == nil {
		t.Fatal("Timeout creating service")
	}

	// Ensure that the service is available.
	hostname := kube.ServiceHostname(svcName, ns, controller.opts.DomainSuffix)
	svc := controller.GetService(hostname)
	if svc == nil {
		t.Fatal("failed to get service")
	}

	// Create an endpoint that indicates it's an MCS endpoint for the service.
	svc1Ips := []string{"128.0.0.1"}
	portNames := []string{"tcp-port"}
	createEndpoints(t, controller, svcName, ns, portNames, svc1Ips, nil, map[string]string{
		mcs.LabelServiceName: svcName,
	})
	if ev := fx.WaitForDuration("eds", 2*time.Second); ev != nil {
		t.Fatalf("Received unexpected EDS event")
	}

	// Ensure that getting by port returns no ServiceInstances.
	instances := controller.InstancesByPort(svc, svc.Ports[0].Port, labels.Collection{})
	if len(instances) != 0 {
		t.Fatalf("should be 0 instances: len(instances) = %v", len(instances))
	}
}
