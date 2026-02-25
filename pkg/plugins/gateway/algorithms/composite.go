/*
Copyright 2026 The Aibrix Team.

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
	"errors"
	"fmt"
	"strings"
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	"github.com/vllm-project/aibrix/pkg/constants"
	"github.com/vllm-project/aibrix/pkg/types"
	"github.com/vllm-project/aibrix/pkg/utils"
)

const RouterComposite types.RoutingAlgorithm = "composite"

var compositeRouterCache sync.Map // map[string]*CompositeRouter, key is normalized chain

func init() {
	RegisterProvider(RouterComposite, CompositeRouterProviderFunc)
}

// CompositeRouter routes requests with a cascaded filter chain.
// All routers except the last one are treated as optional filters.
type CompositeRouter struct {
	routers []types.Router
	chain   []string
}

func (r *CompositeRouter) Route(ctx *types.RoutingContext, pods types.PodList) (string, error) {
	klog.V(4).InfoS("Executing strategy chain", "requestID", ctx.RequestID, "chain", r.chain)

	if len(r.routers) == 0 {
		klog.V(4).InfoS("empty composite chain; falling back to random", "requestID", ctx.RequestID)
		ctx.Algorithm = RouterRandom
		return RandomRouter.Route(ctx, pods)
	}

	original := pods
	candidates := pods
	for i := 0; i < len(r.routers)-1; i++ {
		fr, ok := r.routers[i].(types.RouterList)
		if !ok {
			continue
		}
		filtered, err := fr.Filter(ctx, candidates)
		if err != nil || filtered == nil || filtered.Len() == 0 {
			klog.V(4).InfoS("composite filter produced empty result; falling back to random",
				"requestID", ctx.RequestID,
				"chain", r.chain,
				"stage", i,
				"error", err)
			ctx.Algorithm = RouterRandom
			return RandomRouter.Route(ctx, original)
		}
		candidates = filtered
	}

	return r.routers[len(r.routers)-1].Route(ctx, candidates)
}

// CompositeRouterProviderFunc parses and caches strategy chains.
// It falls back to random when the chain is invalid.
func CompositeRouterProviderFunc(ctx *types.RoutingContext) (types.Router, error) {
	if ctx == nil {
		return RandomRouter, nil
	}

	raw := ""
	if ctx.ReqHeaders != nil {
		raw = ctx.ReqHeaders["routing-strategy"]
	}

	chainKey := raw
	if chainKey == "" || !strings.Contains(chainKey, ",") {
		klog.V(4).InfoS("invalid composite routing-strategy header; falling back to random",
			"requestID", ctx.RequestID,
			"raw", raw)
		ctx.Algorithm = RouterRandom
		return RandomRouter, nil
	}

	if v, ok := compositeRouterCache.Load(chainKey); ok {
		return v.(*CompositeRouter), nil
	}

	parts := strings.Split(chainKey, ",")
	if len(parts) < 2 {
		ctx.Algorithm = RouterRandom
		return RandomRouter, nil
	}

	routers := make([]types.Router, 0, len(parts))
	chain := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			klog.V(4).InfoS("empty routing strategy in chain; falling back to random",
				"requestID", ctx.RequestID,
				"raw", raw)
			ctx.Algorithm = RouterRandom
			return RandomRouter, nil
		}
		if _, ok := Validate(p); !ok {
			klog.V(4).InfoS("unknown routing strategy in chain; falling back to random",
				"requestID", ctx.RequestID,
				"raw", raw,
				"strategy", p)
			ctx.Algorithm = RouterRandom
			return RandomRouter, nil
		}

			tmp := *ctx
		tmp.Algorithm = types.RoutingAlgorithm(p)
		r, err := Select(&tmp)
		if err != nil {
			klog.V(4).InfoS("failed to build router in chain; falling back to random",
				"requestID", ctx.RequestID,
				"raw", raw,
				"strategy", p,
				"error", err)
			ctx.Algorithm = RouterRandom
			return RandomRouter, nil
		}
		routers = append(routers, r)
		chain = append(chain, p)
	}

	cr := &CompositeRouter{routers: routers, chain: chain}
	compositeRouterCache.Store(chainKey, cr)
	return cr, nil
}

// Filter narrows candidates by prefix matching if possible.
// When no prefix match is found, it returns the original candidates (or load-imbalance filtered set).
func (p prefixCacheRouter) Filter(ctx *types.RoutingContext, readyPodList types.PodList) (types.PodList, error) {
	if p.kvSyncRouter != nil {
		return p.kvSyncRouter.Filter(ctx, readyPodList)
	}

	// Mirror the key steps of routeOriginal without selecting a single target.
	tokenizerToUse := (&p).getTokenizerForRequest(ctx, readyPodList)
	tokens, err := tokenizerToUse.TokenizeInputText(ctx.Message)
	if err != nil {
		return nil, err
	}

	readyPods := readyPodList.All()
	readyPodsMap := map[string]struct{}{}
	for _, pod := range readyPods {
		readyPodsMap[pod.Name] = struct{}{}
	}

	leastReqPodList, isLoadImbalanced := getTargetPodListOnLoadImbalance(p.cache, readyPods)
	if isLoadImbalanced {
		if len(leastReqPodList) == 0 {
			return nil, errors.New("no target pod found when load imbalanced")
		}
		readyPodsMap = map[string]struct{}{}
		for _, pod := range leastReqPodList {
			readyPodsMap[pod.Name] = struct{}{}
		}
	}

	matchedPods, _ := p.prefixCacheIndexer.MatchPrefix(tokens, ctx.Model, readyPodsMap)
	if len(matchedPods) > 0 {
		filtered := make([]*v1.Pod, 0, len(matchedPods))
		for _, pod := range readyPods {
			if _, ok := matchedPods[pod.Name]; ok {
				filtered = append(filtered, pod)
			}
		}
		return &utils.PodArray{Pods: filtered}, nil
	}

	if isLoadImbalanced {
		return &utils.PodArray{Pods: leastReqPodList}, nil
	}
	return readyPodList, nil
}

// Filter narrows candidates by KV-sync prefix matching if possible.
func (k *kvSyncPrefixCacheRouter) Filter(ctx *types.RoutingContext, readyPodList types.PodList) (types.PodList, error) {
	tokenizerToUse := k.getTokenizerForRequest(ctx, readyPodList)
	if tokenizerToUse == nil {
		return nil, fmt.Errorf("TokenizerPool not initialized for KV sync router")
	}

	tokens, err := tokenizerToUse.TokenizeInputText(ctx.Message)
	if err != nil {
		return nil, err
	}

	readyPods := readyPodList.All()
	readyPodsMap := map[string]struct{}{}
	for _, pod := range readyPods {
		podKey := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
		readyPodsMap[podKey] = struct{}{}
	}

	leastReqPodList, isLoadImbalanced := getTargetPodListOnLoadImbalance(k.cache, readyPods)
	if isLoadImbalanced {
		if len(leastReqPodList) == 0 {
			return nil, errors.New("no target pod found when load imbalanced")
		}
		readyPodsMap = map[string]struct{}{}
		for _, pod := range leastReqPodList {
			podKey := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
			readyPodsMap[podKey] = struct{}{}
		}
	}

	if k.syncIndexer == nil {
		return nil, fmt.Errorf("sync indexer not available for KV sync routing")
	}

	modelName := ctx.Model
	if modelName == "" && len(readyPods) > 0 {
		modelName = readyPods[0].Labels[constants.ModelLabelName]
	}

	matchedPods, _ := k.syncIndexer.MatchPrefix(modelName, -1, tokens, readyPodsMap)
	if len(matchedPods) > 0 {
		filtered := make([]*v1.Pod, 0, len(matchedPods))
		for _, pod := range readyPods {
			podKey := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
			if _, ok := matchedPods[podKey]; ok {
				filtered = append(filtered, pod)
			}
		}
		return &utils.PodArray{Pods: filtered}, nil
	}

	if isLoadImbalanced {
		return &utils.PodArray{Pods: leastReqPodList}, nil
	}
	return readyPodList, nil
}
