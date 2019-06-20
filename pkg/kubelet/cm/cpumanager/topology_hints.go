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
		if resource != "cpu" {
			continue
		}

		// Get a count of how many CPUs have been requested
		requested := int(amountObj.Value())
		klog.Infof("[cpumanager] Guaranteed CPUs detected: %v", requested)

		// Discover topology in order to establish the number
		// of available CPUs per socket.
		topo, err := topology.Discover(m.machineInfo)
		if err != nil {
			klog.Infof("[cpu manager] error discovering topology")
			continue
		}
		reserved := m.getReservedCPUs(topo)
		containerID, _ := findContainerIDByName(&pod.Status, container.Name)
		availableCPUs := m.getAvailableCPUs(containerID, reserved)
		cpuAccum := newCPUAccumulator(topo, availableCPUs, requested)
		socketCount := topo.NumSockets
		cpusPerSocket := getCPUsPerSocket(socketCount, cpuAccum)
		klog.Infof("[cpumanager] Number of Available CPUs per Socket: %v", cpusPerSocket)

		// Build cpuHintsAffinity []TopologyHint from all possible socket combinations where the
		// request for CPUs can be granted, agnostic to what hints are/are not Preferred.
		cpuHintsAffinity := getCPUHintsAffinity(cpusPerSocket, requested)

		if cpuHintsAffinity != nil {
			// Create new CPU Accumulator in order to find mostCPUs.
			cpuAccumStandard := newCPUAccumulator(topo, topo.CPUDetails.CPUs().Difference(reserved), requested)
			// mostCPUs is the largest number of CPUs on any one socket assuming
			// all CPUs (minus reserved CPUs) are available.
			mostCPUs := getMostCPUs(cpuAccumStandard, socketCount)

			// Assign 'Preferred: true/false' values for each hint.
			cpuHints = getCPUHintsPreferred(cpuHintsAffinity, requested, mostCPUs)
		}
	}
	klog.Infof("[cpumanager] Topology Hints for pod: %v", cpuHints)
	return cpuHints
}

func (m *manager) getReservedCPUs(topo *topology.CPUTopology) cpuset.CPUSet {
	allCPUs := topo.CPUDetails.CPUs()
	reservedCPUs := m.nodeAllocatableReservation[v1.ResourceCPU]
	reservedCPUsFloat := float64(reservedCPUs.MilliValue()) / 1000
	numReservedCPUs := int(math.Ceil(reservedCPUsFloat))
	reserved, _ := takeByTopology(topo, allCPUs, numReservedCPUs)
	klog.Infof("[cpumanager] Reserved CPUs: %v", reserved)
	return reserved
}

func (m *manager) getAvailableCPUs(containerID string, reserved cpuset.CPUSet) cpuset.CPUSet {
	var availableCPUs cpuset.CPUSet
	if cset, ok := m.state.GetCPUSet(containerID); ok {
		klog.Infof("[cpumanager] Reusing pre-assigned CPUSet: %v", cset)
		availableCPUs = cset
	} else {
		// Otherwise, calculate the available CPUs from the topology.
		availableCPUs = m.state.GetDefaultCPUSet().Difference(reserved)
	}
	return availableCPUs
}

func getCPUHintsAffinity(cpusPerSocket []int, requested int) []topologymanager.TopologyHint {
	var cpuHints []topologymanager.TopologyHint
	var totalCPUs int = 0
	for i := 0; i < len(cpusPerSocket); i++ {
		totalCPUs += cpusPerSocket[i]
		if cpusPerSocket[i] >= requested {
			mask, _ := socketmask.NewSocketMask(i)
			cpuHints = append(cpuHints, topologymanager.TopologyHint{SocketAffinity: mask})
		}
	}
	if totalCPUs >= requested {
		crossSocketMask, crossSocket := buildCrossSocketMask(len(cpusPerSocket), cpusPerSocket)
		if crossSocket {
			cpuHints = append(cpuHints, topologymanager.TopologyHint{SocketAffinity: crossSocketMask})
		}

	}
	return cpuHints
}

func getCPUHintsPreferred(cpuHints []topologymanager.TopologyHint, requested int, mostCPUs int) []topologymanager.TopologyHint {
	// Check all hints for the hint with the lowest number of sockets (bestHint).
	bestHint := cpuHints[0].SocketAffinity.Count()
	for r := range cpuHints {
		if cpuHints[r].SocketAffinity.Count() < bestHint {
			bestHint = cpuHints[r].SocketAffinity.Count()
		}
	}
	// Iterate over all hints and set 'Preferred: true/false' for each hint
	for r := range cpuHints {
		if cpuHints[r].SocketAffinity.Count() <= bestHint && requested <= mostCPUs {
			// In the event that the hint has a lower or equal number of sockets to bestHint
			// and one or more of those sockets could individually satisfy the number of CPUs
			// requested, assuming all CPUs on the socket (minus reserved CPUs) are available,
			// set hint to 'Preferred: true'.
			cpuHints[r].Preferred = true
		} else {
			// In all other scenarios - 'Preferred: false'.
			// Eg: If the number of sockets in the hint is larger than that of bestHint,
			// then there is a more preferrable hint available.
			// Eg: No socket could ever satisfy the request individually, assuming
			// all CPUs on that socket (minus reserved CPUs) were available.
			cpuHints[r].Preferred = false
		}
	}
	return cpuHints
}

func buildCrossSocketMask(socketCount int, cpusPerSocket []int) (socketmask.SocketMask, bool) {
	var socketNums []int
	crossSocket := true
	for i := 0; i < socketCount; i++ {
		if cpusPerSocket[i] > 0 {
			socketNums = append(socketNums, i)
		}
	}
	if len(socketNums) == 1 {
		crossSocket = false
	}
	mask, _ := socketmask.NewSocketMask(socketNums...)
	return mask, crossSocket
}

func getMostCPUs(cpuAccum *cpuAccumulator, socketCount int) int {
	cpusPerSocket := getCPUsPerSocket(socketCount, cpuAccum)
	mostCPUs := cpusPerSocket[0]
	for r := range cpusPerSocket {
		if cpusPerSocket[r] > mostCPUs {
			mostCPUs = cpusPerSocket[r]
		}
	}
	return mostCPUs
}

func getCPUsPerSocket(socketCount int, cpuAccum *cpuAccumulator) []int {
	CPUsPerSocket := make([]int, socketCount)
	for i := 0; i < socketCount; i++ {
		CPUsInSocket := cpuAccum.details.CPUsInSocket(i)
		CPUsPerSocket[i] = CPUsInSocket.Size()
	}
	return CPUsPerSocket
}
