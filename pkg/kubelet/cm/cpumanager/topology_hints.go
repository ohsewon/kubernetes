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

package cpumanager

import (
	"k8s.io/api/core/v1"
	"k8s.io/klog"
	"math"

	"k8s.io/kubernetes/pkg/kubelet/cm/cpumanager/topology"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpuset"
	"k8s.io/kubernetes/pkg/kubelet/cm/topologymanager"
	"k8s.io/kubernetes/pkg/kubelet/cm/topologymanager/socketmask"
)

func (m *manager) GetTopologyHints(pod v1.Pod, container v1.Container) []topologymanager.TopologyHint {
	var cpuHints []topologymanager.TopologyHint
	for resourceObj, amountObj := range container.Resources.Requests {
		resource := string(resourceObj)
		requested := int(amountObj.Value())
		if resource != "cpu" {
			continue
		}

		klog.Infof("[cpumanager] Guaranteed CPUs detected: %v", requested)

		topo, err := topology.Discover(m.machineInfo)
		if err != nil {
			klog.Infof("[cpu manager] error discovering topology")
			continue
		}
		var assignableCPUs cpuset.CPUSet
		containerID, _ := findContainerIDByName(&pod.Status, container.Name)
		if cset, ok := m.state.GetCPUSet(containerID); ok {
			klog.Infof("[cpumanager] Reusing pre-assigned CPUSet: %v", cset)
			assignableCPUs = cset
		} else {
			// Otherwise, calculate the assignable CPUs from the topology.
			assignableCPUs = m.getAssignableCPUs(topo)
		}
		klog.Infof("AssignableCPUs: %v", assignableCPUs)
		cpuAccum := newCPUAccumulator(topo, assignableCPUs, requested)

		//Can we always assume NumSockets and Socket Numbers are the same?
		socketCount := topo.NumSockets
		klog.Infof("[cpumanager] Number of sockets on machine (available and unavailable): %v", socketCount)
		cpuHintsTemp := getCPUMask(socketCount, cpuAccum, requested)
		CPUsPerSocket := topo.CPUsPerSocket()
		cpuHints = getPreferred(cpuHintsTemp, requested, CPUsPerSocket)
		klog.Infof("CPUHints: %v", cpuHints)
	}
	return cpuHints
}

func (m *manager) getAssignableCPUs(topo *topology.CPUTopology) cpuset.CPUSet {
	allCPUs := topo.CPUDetails.CPUs()
	klog.Infof("[cpumanager] Shared CPUs: %v", allCPUs)
	reservedCPUs := m.nodeAllocatableReservation[v1.ResourceCPU]
	reservedCPUsFloat := float64(reservedCPUs.MilliValue()) / 1000
	numReservedCPUs := int(math.Ceil(reservedCPUsFloat))
	reserved, _ := takeByTopology(topo, allCPUs, numReservedCPUs)
	klog.Infof("[cpumanager] Reserved CPUs: %v", reserved)
	assignableCPUs := m.state.GetDefaultCPUSet().Difference(reserved)
	klog.Infof("[cpumanager] Assignable CPUs (Shared - Reserved): %v", assignableCPUs)
	return assignableCPUs
}

func getCPUMask(socketCount int, cpuAccum *cpuAccumulator, requested int) []topologymanager.TopologyHint {
	CPUsInSocketSize := make([]int, socketCount)
	var cpuHints []topologymanager.TopologyHint
	var totalCPUs int = 0
	for i := 0; i < socketCount; i++ {
		CPUsInSocket := cpuAccum.details.CPUsInSocket(i)
		klog.Infof("[cpumanager] Assignable CPUs on Socket %v: %v", i, CPUsInSocket)
		CPUsInSocketSize[i] = CPUsInSocket.Size()
		totalCPUs += CPUsInSocketSize[i]
		if CPUsInSocketSize[i] >= requested {
			mask, _ := socketmask.NewSocketMask(i)
			cpuHints = append(cpuHints, topologymanager.TopologyHint{SocketAffinity: mask, Preferred: true})
		}
	}
	if totalCPUs >= requested {
		crossSocketMask, crossSocket := buildCrossSocketMask(socketCount, CPUsInSocketSize)
		if crossSocket {
			cpuHints = append(cpuHints, topologymanager.TopologyHint{SocketAffinity: crossSocketMask, Preferred: true})
		}

	}
	klog.Infof("[cpumanager] Number of Assignable CPUs per Socket: %v", CPUsInSocketSize)
	klog.Infof("[cpumanager] Topology Affinities for pod: %v", cpuHints)
	return cpuHints
}

func buildCrossSocketMask(socketCount int, CPUsInSocketSize []int) (socketmask.SocketMask, bool) {
	var socketNums []int
	crossSocket := true
	for i := 0; i < socketCount; i++ {
		if CPUsInSocketSize[i] > 0 {
			socketNums = append(socketNums, i)
		}
	}
	if len(socketNums) == 1 {
		crossSocket = false
	}
	mask, _ := socketmask.NewSocketMask(socketNums...)
	return mask, crossSocket
}

func getPreferred(cpuHints []topologymanager.TopologyHint, requested int, CPUsPerSocket int) []topologymanager.TopologyHint {
	bestHint := cpuHints[0].SocketAffinity.Count()
	for r := range cpuHints {
		if cpuHints[r].SocketAffinity.Count() < bestHint {
			bestHint = cpuHints[r].SocketAffinity.Count()
		}
	}
	for r := range cpuHints {
		if cpuHints[r].SocketAffinity.Count() > bestHint {
			cpuHints[r].Preferred = false
			klog.Infof("Set Preferred to false: (there are more preferable hints) %v", cpuHints[r])
		} else if cpuHints[r].SocketAffinity.Count() == bestHint && requested >= CPUsPerSocket {
			cpuHints[r].Preferred = false
			klog.Infof("Set Preferred to false (could never be satisfied on one socket): %v", cpuHints[r])
		}
	}
	return cpuHints
}
