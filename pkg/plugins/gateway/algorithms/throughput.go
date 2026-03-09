/*
Copyright 2024 The Aibrix Team.

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

package routingalgorithms

import (
	"math"
	"math/rand"
	"sort"

	"github.com/vllm-project/aibrix/pkg/cache"
	"github.com/vllm-project/aibrix/pkg/metrics"
	"github.com/vllm-project/aibrix/pkg/types"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

const RouterThroughput types.RoutingAlgorithm = "throughput"

func init() {
	Register(RouterThroughput, NewThroughputRouter)
}

type throughputRouter struct {
	cache cache.Cache
}

func NewThroughputRouter() (types.Router, error) {
	c, err := cache.Get()
	if err != nil {
		return nil, err
	}

	return throughputRouter{
		cache: c,
	}, nil
}

func (r throughputRouter) Route(ctx *types.RoutingContext, readyPodList types.PodList) (string, error) {
	var targetPod *v1.Pod
	minCount := math.MaxFloat64

	readyPods := readyPodList.All()

	for _, pod := range readyPods {
		promptThroughput, err := r.cache.GetMetricValueByPodModel(pod.Name, pod.Namespace, ctx.Model, metrics.AvgPromptThroughputToksPerS)
		if err != nil {
			klog.Error(err)
			continue
		}
		generationThroughput, err := r.cache.GetMetricValueByPodModel(pod.Name, pod.Namespace, ctx.Model, metrics.AvgGenerationThroughputToksPerS)
		if err != nil {
			klog.Error(err)
			continue
		}

		// processing prompt tokens is twice as expensive than generation tokens
		totalThroughput := 2*promptThroughput.GetSimpleValue() + generationThroughput.GetSimpleValue()
		klog.V(4).Infof("pod: %v, podIP: %v, promptThroughput: %v, generationThroughput: %v, totalThroughput: %v",
			pod.Name, pod.Status.PodIP, promptThroughput, generationThroughput, totalThroughput)

		if totalThroughput <= minCount {
			minCount = totalThroughput
			targetPod = pod
		}
	}

	// Use fallback if no valid metrics
	if targetPod == nil {
		var err error
		targetPod, err = SelectRandomPodAsFallback(ctx, readyPods, rand.Intn)
		if err != nil {
			return "", err
		}
	}
	ctx.SetTargetPod(targetPod)
	return ctx.TargetAddress(), nil
}

func (r throughputRouter) SubscribedMetrics() []string {
	return []string{
		metrics.AvgPromptThroughputToksPerS,
		metrics.AvgGenerationThroughputToksPerS,
	}
}

// Reorder implements the Reorderer interface for multi-strategy routing.
// It groups pods by throughput ranges, with higher throughput pods in higher priority groups.
// Within each group, pods are sorted by throughput (descending).
func (r throughputRouter) Reorder(ctx *types.RoutingContext, podGroups routingalgorithms.PodGroups) routingalgorithms.PodGroups {
	var allPods []*v1.Pod
	
	// Collect all pods from all groups
	for _, group := range podGroups {
		allPods = append(allPods, group...)
	}
	
	if len(allPods) == 0 {
		return routingalgorithms.PodGroups{}
	}
	
	// Get throughput metrics for all pods
	type podThroughput struct {
		pod       *v1.Pod
		throughput float64
	}
	
	var podThroughputs []podThroughput
	for _, pod := range allPods {
		promptThroughput, err := r.cache.GetMetricValueByPodModel(pod.Name, pod.Namespace, ctx.Model, metrics.AvgPromptThroughputToksPerS)
		if err != nil {
			klog.V(4).InfoS("failed to get prompt throughput for pod", "pod", pod.Name, "error", err)
			continue
		}
		generationThroughput, err := r.cache.GetMetricValueByPodModel(pod.Name, pod.Namespace, ctx.Model, metrics.AvgGenerationThroughputToksPerS)
		if err != nil {
			klog.V(4).InfoS("failed to get generation throughput for pod", "pod", pod.Name, "error", err)
			continue
		}
		
		// processing prompt tokens is twice as expensive than generation tokens
		totalThroughput := 2*promptThroughput.GetSimpleValue() + generationThroughput.GetSimpleValue()
		
		podThroughputs = append(podThroughputs, podThroughput{
			pod:        pod,
			throughput: totalThroughput,
		})
	}
	
	if len(podThroughputs) == 0 {
		return routingalgorithms.PodGroups{allPods}
	}
	
	// Group by throughput ranges (higher throughput = higher priority)
	// For simplicity, we'll create groups based on throughput quartiles
	var throughputs []float64
	for _, pt := range podThroughputs {
		throughputs = append(throughputs, pt.throughput)
	}
	sort.Float64s(throughputs)
	
	// Create throughput ranges (descending order for higher priority)
	var ranges []struct {
		min, max float64
	}
	
	if len(throughputs) > 0 {
		maxThroughput := throughputs[len(throughputs)-1]
		minThroughput := throughputs[0]
		rangeSize := (maxThroughput - minThroughput) / 4 // 4 groups
		
		for i := 3; i >= 0; i-- {
			min := minThroughput + float64(i)*rangeSize
			max := minThroughput + float64(i+1)*rangeSize
			if i == 3 {
				max = maxThroughput + 1 // Ensure max value is included
			}
			ranges = append(ranges, struct{ min, max float64 }{min, max})
		}
	}
	
	// Assign pods to groups based on throughput ranges
	var result routingalgorithms.PodGroups
	for _, r := range ranges {
		var group []*v1.Pod
		for _, pt := range podThroughputs {
			if pt.throughput >= r.min && pt.throughput < r.max {
				group = append(group, pt.pod)
			}
		}
		if len(group) > 0 {
			result = append(result, group)
		}
	}
	
	// If no groups were created, return all pods in a single group
	if len(result) == 0 {
		result = routingalgorithms.PodGroups{allPods}
	}
	
	klog.V(4).InfoS("throughput reorder completed",
		"requestID", ctx.RequestID,
		"inputGroups", len(podGroups),
		"outputGroups", len(result),
		"totalPods", len(allPods),
	)
	
	return result
}
