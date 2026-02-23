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
	"fmt"
	"strings"
	"sync"

	"github.com/vllm-project/aibrix/pkg/cache"
	"github.com/vllm-project/aibrix/pkg/types"
	"k8s.io/klog/v2"
)

// PipelineRouter implements multi-strategy routing by applying each strategy as a stage
// that transforms the candidate pod set.
//
// Semantics:
// - Start with S0 = all routable pods (readyPodList).
// - For each stage in ctx.StrategyPipeline: Si = apply(stage, S{i-1}).
// - After all stages, select one target from Sn (currently random).
//
// Compatibility:
// - This router is only used when routing-strategy is explicitly set to "pipeline".
// - All legacy single-strategy routers keep their original execution path.
type PipelineRouter struct {
	initOnce sync.Once
	initErr  error

	cache  cache.Cache
	prefix prefixCacheRouter
}

func NewPipelineRouter() (types.Router, error) {
	// Lazy init: avoid depending on cache initialization during router registry Init().
	return &PipelineRouter{}, nil
}

func (r *PipelineRouter) ensureInit() error {
	r.initOnce.Do(func() {
		c, err := cache.Get()
		if err != nil {
			r.initErr = err
			return
		}
		r.cache = c

		pc, err := NewPrefixCacheRouter()
		if err != nil {
			r.initErr = err
			return
		}
		pcv, ok := pc.(prefixCacheRouter)
		if !ok {
			r.initErr = fmt.Errorf("unexpected prefix-cache router type: %T", pc)
			return
		}
		r.prefix = pcv
	})
	return r.initErr
}

func (r *PipelineRouter) Route(ctx *types.RoutingContext, readyPodList types.PodList) (string, error) {
	if err := r.ensureInit(); err != nil {
		return "", err
	}

	stages := ctx.StrategyPipeline
	if len(stages) == 0 {
		stages = []string{string(RouterRandom)}
	}

	current := readyPodList
	var prefixHashes []uint64
	var usedPrefixCache bool

	for _, stage := range stages {
		stage = strings.TrimSpace(stage)
		if stage == "" {
			continue
		}
		if types.RoutingAlgorithm(stage) == RouterPipeline {
			// Avoid recursion.
			klog.InfoS("pipeline_stage_skipped", "request_id", ctx.RequestID, "stage", stage, "reason", "recursive_pipeline")
			continue
		}

		inLen := current.Len()
		var next types.PodList
		var err error

		switch types.RoutingAlgorithm(stage) {
		case RouterRandom:
			next, err = RandomSelectAsList(current)
		case RouterLeastRequest:
			next, err = LeastRequestSelectAsList(r.cache, current)
		case RouterPrefixCache:
			next, prefixHashes, err = PrefixCacheSelectAsList(&r.prefix, ctx, current)
			if err == nil {
				usedPrefixCache = true
			}
		default:
			klog.InfoS("pipeline_stage_skipped", "request_id", ctx.RequestID, "stage", stage, "reason", "unknown_strategy")
			continue
		}

		outLen := 0
		if next != nil {
			outLen = next.Len()
		}
		klog.V(4).InfoS("pipeline_stage_applied", "request_id", ctx.RequestID, "stage", stage, "input_pods", inLen, "output_pods", outLen)

		// Pipeline semantics: each stage transforms the candidate set.
		// If a stage errors, treat it as a no-op and continue with current candidates.
		if err != nil {
			klog.ErrorS(err, "pipeline_stage_failed", "request_id", ctx.RequestID, "stage", stage, "input_pods", inLen, "output_pods", outLen)
			continue
		}
		if next == nil {
			klog.InfoS("pipeline_stage_noop", "request_id", ctx.RequestID, "stage", stage, "reason", "nil_output")
			continue
		}
		current = next
	}

	finalList, err := RandomSelectAsList(current)
	if err != nil {
		return "", err
	}
	if finalList.Len() == 0 {
		return "", fmt.Errorf("no pods to forward request")
	}
	finalPod := finalList.All()[0]

	// Update prefix cache only after the final decision.
	if usedPrefixCache && len(prefixHashes) > 0 {
		r.prefix.prefixCacheIndexer.AddPrefix(prefixHashes, ctx.Model, finalPod.Name)
	}

	ctx.SetTargetPod(finalPod)
	return ctx.TargetAddress(), nil
}

func (r *PipelineRouter) SubscribedMetrics() []string {
	return []string{}
}
