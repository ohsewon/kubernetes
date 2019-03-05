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
	"math"
	"k8s.io/api/core/v1"
	"k8s.io/klog"

	"k8s.io/kubernetes/pkg/kubelet/cm/cpuset"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpumanager/topology"
	"k8s.io/kubernetes/pkg/kubelet/cm/topologymanager"
	"k8s.io/kubernetes/pkg/kubelet/cm/topologymanager/socketmask"
)


func (m *manager) GetTopologyHints(pod v1.Pod, container v1.Container) topologymanager.TopologyHints {
    	var cpuMask []socketmask.SocketMask	
    	for resourceObj, amountObj := range container.Resources.Requests {
        	resource := string(resourceObj)
        	amount := int(amountObj.Value())
        	requested := int64(amount)
        	if resource != "cpu" {
                	continue
            	}
        
        	klog.Infof("[cpumanager] Guaranteed CPUs detected: %v", amount)

            	topo, err := topology.Discover(m.machineInfo)
            	if err != nil {
                    klog.Infof("[cpu manager] error discovering topology")
            	}
	
		assignableCPUs := m.getAssignableCPUs(topo)
        	cpuAccum := newCPUAccumulator(topo, assignableCPUs, amount)     

            	socketCount := topo.NumSockets
            	klog.Infof("[cpumanager] Number of sockets on machine (available and unavailable): %v", socketCount)
		cpuMask = getCPUMask(socketCount, cpuAccum, requested)
    	}  
    	return topologymanager.TopologyHints{ 
        	SocketAffinity: cpuMask,
        	Affinity: true,
    	}
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

func getCPUMask(socketCount int, cpuAccum *cpuAccumulator, requested int64) []socketmask.SocketMask {
	var cpuMask []socketmask.SocketMask
	CPUsInSocketSize := make([]int64, socketCount)
	var mask []int64
	var totalCPUs int64 = 0
      	var cpuMaskTemp [][]int64 
      	for i := 0; i < socketCount; i++ {
        	CPUsInSocket := cpuAccum.details.CPUsInSocket(i)
              	klog.Infof("[cpumanager] Assignable CPUs on Socket %v: %v", i, CPUsInSocket)
		CPUsInSocketSize[i] = int64(CPUsInSocket.Size()) 
              	totalCPUs += CPUsInSocketSize[i]
            	if CPUsInSocketSize[i] >= requested {
                  	for j := 0; j < socketCount; j++ { 
                         	if j == i { 
                                	mask = append(mask, 1)
                           	} else {        
                                	mask = append(mask, 0)
                             	}
                   	}
                 	cpuMaskTemp = append(cpuMaskTemp, mask)
                   	mask = nil
           	}                        
  	}
	if totalCPUs >= requested {
		crossSocketMask := buildCrossSocketMask(socketCount, CPUsInSocketSize)  		
           	cpuMaskTemp = append(cpuMaskTemp, crossSocketMask)
  	}
	klog.Infof("[cpumanager] Number of Assignable CPUs per Socket: %v", CPUsInSocketSize)   
      	klog.Infof("[cpumanager] Topology Affinities for pod: %v", cpuMaskTemp)             
      	for r := range cpuMaskTemp {
          	cpuSocket := socketmask.SocketMask(cpuMaskTemp[r])
            	cpuMask = append(cpuMask, cpuSocket)
   	}
	return cpuMask
}

func buildCrossSocketMask(socketCount int, CPUsInSocketSize []int64) []int64 {
	var mask []int64
	for i := 0; i < socketCount; i++ {
		if CPUsInSocketSize[i] == 0 {
             		mask = append(mask, 0)
           	} else {
           		mask = append(mask, 1)
         	}	
	}
	return mask
}

