/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package topologymanager

import (
	"reflect"
	"testing"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/kubernetes/pkg/kubelet/cm/topologymanager/socketmask"
	"k8s.io/kubernetes/pkg/kubelet/lifecycle"
)

func TestNewManager(t *testing.T) {
	tcases := []struct {
		name       string
		policyType string
	}{
		{
			name:       "Policy is set preferred",
			policyType: "preferred",
		},
		{
			name:       "Policy is set to strict",
			policyType: "strict",
		},
		{
			name:       "Policy is set to unknown",
			policyType: "unknown",
		},
	}

	for _, tc := range tcases {
		mngr := NewManager(tc.policyType)

		if _, ok := mngr.(Manager); !ok {
			t.Errorf("result is not Manager type")
		}
	}
}

type mockHintProvider struct {
	th TopologyHints
}

func (m *mockHintProvider) GetTopologyHints(pod v1.Pod, container v1.Container) TopologyHints {
	return m.th
}

func TestGetAffinity(t *testing.T) {
	tcases := []struct {
		name          string
		containerName string
		podUID        string
		expected      TopologyHints
	}{
		{
			name:          "case1",
			containerName: "nginx",
			podUID:        "0aafa4c4-38e8-11e9-bcb1-a4bf01040474",
			expected:      TopologyHints{},
		},
	}
	for _, tc := range tcases {
		mngr := manager{}
		actual := mngr.GetAffinity(tc.podUID, tc.containerName)
		if !reflect.DeepEqual(actual, tc.expected) {
			t.Errorf("Expected Affinity in result to be %v, got %v", tc.expected, actual)
		}
	}
}

func TestCalculateTopologyAffinity(t *testing.T) {
	tcases := []struct {
		name     string
		hp       []HintProvider
		expected TopologyHints
	}{
		{
			name: "TopologyHints not set",
			hp:   []HintProvider{},
			expected: TopologyHints{
				Affinity:       true,
				SocketAffinity: nil,
			},
		},
		{
			name: "TopologyHints set with Affinity as true and SocketAffinity as nil",
			hp: []HintProvider{
				&mockHintProvider{
					TopologyHints{
						Affinity:       true,
						SocketAffinity: nil,
					},
				},
			},
			expected: TopologyHints{
				Affinity:       false,
				SocketAffinity: nil,
			},
		},
		{
			name: "TopologyHints set with Affinity as false and SocketAffinity as not nil",
			hp: []HintProvider{
				&mockHintProvider{
					TopologyHints{
						Affinity:       false,
						SocketAffinity: []socketmask.SocketMask{{1, 0}, {0, 1}, {1, 1}},
					},
				},
			},
			expected: TopologyHints{
				Affinity:       false,
				SocketAffinity: []socketmask.SocketMask{{1, 0}, {0, 1}, {1, 1}},
			},
		},
		{
			name: "TopologyHints set with Affinity as true and SocketAffinity as not nil",
			hp: []HintProvider{
				&mockHintProvider{
					TopologyHints{
						Affinity:       true,
						SocketAffinity: []socketmask.SocketMask{{1, 0}, {0, 1}, {1, 1}},
					},
				},
			},
			expected: TopologyHints{
				Affinity:       true,
				SocketAffinity: []socketmask.SocketMask{{1, 0}, {0, 1}, {1, 1}},
			},
		},
	}

	for _, tc := range tcases {
		mngr := manager{}
		mngr.hintProviders = tc.hp
		actual := mngr.calculateTopologyAffinity(v1.Pod{}, v1.Container{})
		if !reflect.DeepEqual(actual.Affinity, tc.expected.Affinity) {
			t.Errorf("Expected Affinity in result to be %v, got %v", tc.expected.Affinity, actual.Affinity)
		}
	}
}

func TestAddContainer(t *testing.T) {
	testCases := []struct {
		name        string
		containerID string
		podUID      types.UID
	}{
		{
			name:        "Case1",
			containerID: "nginx",
			podUID:      "0aafa4c4-38e8-11e9-bcb1-a4bf01040474",
		},
		{
			name:        "Case2",
			containerID: "Busy_Box",
			podUID:      "b3ee37fc-39a5-11e9-bcb1-a4bf01040474",
		},
	}
	mngr := manager{}
	mngr.podMap = make(map[string]string)
	for _, tc := range testCases {
		pod := v1.Pod{}
		pod.UID = tc.podUID
		err := mngr.AddContainer(&pod, tc.containerID)
		if err != nil {
			t.Errorf("Expected error to be nil but got: %v", err)
		}
		if val, ok := mngr.podMap[tc.containerID]; ok {
			if reflect.DeepEqual(val, pod.UID) {
				t.Errorf("Error occurred")
			}
		} else {
			t.Errorf("Error occurred, Pod not added to podMap")
		}
	}
}

func TestRemoveContainer(t *testing.T) {
	testCases := []struct {
		name        string
		containerID string
		podUID      types.UID
	}{
		{
			name:        "Case1",
			containerID: "nginx",
			podUID:      "0aafa4c4-38e8-11e9-bcb1-a4bf01040474",
		},
		{
			name:        "Case2",
			containerID: "Busy_Box",
			podUID:      "b3ee37fc-39a5-11e9-bcb1-a4bf01040474",
		},
	}
	var len1, len2 int
	mngr := manager{}
	mngr.podMap = make(map[string]string)
	for _, tc := range testCases {
		mngr.podMap[tc.containerID] = string(tc.podUID)
		len1 = len(mngr.podMap)
		err := mngr.RemoveContainer(tc.containerID)
		len2 = len(mngr.podMap)
		if err != nil {
			t.Errorf("Expected error to be nil but got: %v", err)
		}
		if len1-len2 != 1 {
			t.Errorf("Remove Pod resulted in error")
		}
	}

}
func TestAddHintProvider(t *testing.T) {
	var len1 int
	tcases := []struct {
		name string
		hp   []HintProvider
	}{
		{
			name: "Add HintProvider",
			hp: []HintProvider{
				&mockHintProvider{
					TopologyHints{
						Affinity:       true,
						SocketAffinity: nil,
					},
				},
			},
		},
	}
	mngr := manager{}
	for _, tc := range tcases {
		mngr.hintProviders = []HintProvider{}
		len1 = len(mngr.hintProviders)
		mngr.AddHintProvider(tc.hp[0])
	}
	len2 := len(mngr.hintProviders)
	if len2-len1 != 1 {
		t.Errorf("error")
	}
}

func TestAdmit(t *testing.T) {
	tcases := []struct {
		name     string
		result   lifecycle.PodAdmitResult
		qosClass v1.PodQOSClass
		expected bool
	}{
		{
			name:     "QOSClass set as Gauranteed",
			result:   lifecycle.PodAdmitResult{},
			qosClass: "Guaranteed",
			expected: true,
		},
		{
			name:     "QOSClass set as Burstable",
			result:   lifecycle.PodAdmitResult{},
			qosClass: "Burstable",
			expected: true,
		},
		{
			name:     "QOSClass set as BestEffort",
			result:   lifecycle.PodAdmitResult{},
			qosClass: "BestEffort",
			expected: true,
		},
	}
	for _, tc := range tcases {
		man := manager{}
		man.podTopologyHints = make(map[string]containers)
		podAttr := lifecycle.PodAdmitAttributes{}
		pod := v1.Pod{}
		pod.Status.QOSClass = tc.qosClass
		podAttr.Pod = &pod
		//c := make(containers)
		actual := man.Admit(&podAttr)
		if reflect.DeepEqual(actual, tc.result) {
			t.Errorf("Error occured, expected Admit in result to be %v got %v", tc.result, actual.Admit)
		}
	}
}
