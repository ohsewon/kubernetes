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

func (m *manager) GetTopologyHints(pod v1.Pod, container v1.Container) ([]topologymanager.TopologyHint, bool) {
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
		}

		assignableCPUs := m.getAssignableCPUs(topo)
		klog.Infof("AssignableCPUs: %v", assignableCPUs)
		cpuAccum := newCPUAccumulator(topo, assignableCPUs, requested)

		//Can we always assume NumSockets and Socket Numbers are the same?
		socketCount := topo.NumSockets
		klog.Infof("[cpumanager] Number of sockets on machine (available and unavailable): %v", socketCount)
		cpuHints = getCPUMask(socketCount, cpuAccum, requested)
		admit := calculateIfCPUHasSocketAffinity(cpuHints)

		klog.Infof("CPUHints; %v, Admit: %v", cpuHints, admit)
	}
	return cpuHints, true
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
			cpuHints = append(cpuHints, topologymanager.TopologyHint{SocketMask: mask})
		}
	}
	if totalCPUs >= requested {
		crossSocketMask, crossSocket := buildCrossSocketMask(socketCount, CPUsInSocketSize)
		if crossSocket {
			cpuHints = append(cpuHints, topologymanager.TopologyHint{SocketMask: crossSocketMask})
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

func calculateIfCPUHasSocketAffinity(cpuHints []topologymanager.TopologyHint) bool {
	admit := false
	for _, hint := range cpuHints {
		if hint.SocketMask.Count() == 1 {
			admit = true
			break
		}
	}
	return admit
}
