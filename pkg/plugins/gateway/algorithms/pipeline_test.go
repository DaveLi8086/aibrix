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
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vllm-project/aibrix/pkg/cache"
	"github.com/vllm-project/aibrix/pkg/metrics"
	"github.com/vllm-project/aibrix/pkg/types"
	"github.com/vllm-project/aibrix/pkg/utils"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func initGlobalCacheWithPodsAndMetricsForPipelineTest(t *testing.T, model string, pods []*v1.Pod, podMetrics map[string]map[string]metrics.MetricValue) {
	t.Helper()

	st := cache.InitForTest()
	st = cache.InitWithPods(st, pods, model)
	st = cache.InitWithPodsMetrics(st, podMetrics)
	_ = cache.InitWithPodsModelMetrics(st, podMetrics)
}

func TestPipelineRouter_LeastRequestThenRandom_Deterministic(t *testing.T) {
	model := fmt.Sprintf("pipeline-test-model-%d", time.Now().UnixNano())
	pods := []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "p-lr-1", Namespace: "default"},
			Status: v1.PodStatus{PodIP: "10.0.0.1", Conditions: []v1.PodCondition{{Type: v1.PodReady, Status: v1.ConditionTrue}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "p-lr-2", Namespace: "default"},
			Status: v1.PodStatus{PodIP: "10.0.0.2", Conditions: []v1.PodCondition{{Type: v1.PodReady, Status: v1.ConditionTrue}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "p-lr-3", Namespace: "default"},
			Status: v1.PodStatus{PodIP: "10.0.0.3", Conditions: []v1.PodCondition{{Type: v1.PodReady, Status: v1.ConditionTrue}}},
		},
	}
	initGlobalCacheWithPodsAndMetricsForPipelineTest(t, model, pods, map[string]map[string]metrics.MetricValue{
		"p-lr-1": {metrics.RealtimeNumRequestsRunning: &metrics.SimpleMetricValue{Value: 0}},
		"p-lr-2": {metrics.RealtimeNumRequestsRunning: &metrics.SimpleMetricValue{Value: 10}},
		"p-lr-3": {metrics.RealtimeNumRequestsRunning: &metrics.SimpleMetricValue{Value: 20}},
	})

	router, err := NewPipelineRouter()
	require.NoError(t, err)

	ctx := types.NewRoutingContext(context.Background(), RouterPipeline, model, "", "req-1", "")
	ctx.Message = "hello"
	ctx.StrategyPipeline = []string{string(RouterLeastRequest), string(RouterRandom)}

	ready := &utils.PodArray{Pods: pods}
	addr, err := router.Route(ctx, ready)
	require.NoError(t, err)
	require.NotEmpty(t, addr)
	require.NotNil(t, ctx.TargetPod())
	require.Equal(t, "p-lr-1", ctx.TargetPod().Name)
}

func TestPipelineRouter_EmptyPipeline_DefaultRandom(t *testing.T) {
	model := fmt.Sprintf("pipeline-test-model-%d", time.Now().UnixNano())
	pods := []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "p-only", Namespace: "default"},
			Status: v1.PodStatus{PodIP: "10.0.1.1", Conditions: []v1.PodCondition{{Type: v1.PodReady, Status: v1.ConditionTrue}}},
		},
	}
	initGlobalCacheWithPodsAndMetricsForPipelineTest(t, model, pods, map[string]map[string]metrics.MetricValue{
		"p-only": {metrics.RealtimeNumRequestsRunning: &metrics.SimpleMetricValue{Value: 0}},
	})

	router, err := NewPipelineRouter()
	require.NoError(t, err)

	ctx := types.NewRoutingContext(context.Background(), RouterPipeline, model, "", "req-2", "")
	ctx.Message = "hello"
	ctx.StrategyPipeline = nil

	ready := &utils.PodArray{Pods: pods}
	addr, err := router.Route(ctx, ready)
	require.NoError(t, err)
	require.NotEmpty(t, addr)
	require.NotNil(t, ctx.TargetPod())
	require.Equal(t, "p-only", ctx.TargetPod().Name)
}

func TestPipelineRouter_SingleStageRandom_EqualsLegacyRandomForSinglePod(t *testing.T) {
	model := fmt.Sprintf("pipeline-test-model-%d", time.Now().UnixNano())
	pods := []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "p-single", Namespace: "default"},
			Status: v1.PodStatus{PodIP: "10.0.4.1", Conditions: []v1.PodCondition{{Type: v1.PodReady, Status: v1.ConditionTrue}}},
		},
	}
	initGlobalCacheWithPodsAndMetricsForPipelineTest(t, model, pods, map[string]map[string]metrics.MetricValue{
		"p-single": {metrics.RealtimeNumRequestsRunning: &metrics.SimpleMetricValue{Value: 0}},
	})

	pRouterAny, err := NewPipelineRouter()
	require.NoError(t, err)

	ctxPipeline := types.NewRoutingContext(context.Background(), RouterPipeline, model, "", "req-2b", "")
	ctxPipeline.Message = "hello"
	ctxPipeline.StrategyPipeline = []string{string(RouterRandom)}
	ready := &utils.PodArray{Pods: pods}
	addrPipeline, err := pRouterAny.Route(ctxPipeline, ready)
	require.NoError(t, err)

	legacy := randomRouter{}
	ctxLegacy := types.NewRoutingContext(context.Background(), RouterRandom, model, "", "req-2c", "")
	ctxLegacy.Message = "hello"
	addrLegacy, err := legacy.Route(ctxLegacy, ready)
	require.NoError(t, err)

	require.Equal(t, "p-single", ctxPipeline.TargetPod().Name)
	require.Equal(t, "p-single", ctxLegacy.TargetPod().Name)
	require.Equal(t, addrLegacy, addrPipeline)
}

func TestPipelineRouter_UnknownStage_Ignored(t *testing.T) {
	model := fmt.Sprintf("pipeline-test-model-%d", time.Now().UnixNano())
	pods := []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "p-u-1", Namespace: "default"},
			Status: v1.PodStatus{PodIP: "10.0.2.1", Conditions: []v1.PodCondition{{Type: v1.PodReady, Status: v1.ConditionTrue}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "p-u-2", Namespace: "default"},
			Status: v1.PodStatus{PodIP: "10.0.2.2", Conditions: []v1.PodCondition{{Type: v1.PodReady, Status: v1.ConditionTrue}}},
		},
	}
	initGlobalCacheWithPodsAndMetricsForPipelineTest(t, model, pods, map[string]map[string]metrics.MetricValue{
		"p-u-1": {metrics.RealtimeNumRequestsRunning: &metrics.SimpleMetricValue{Value: 0}},
		"p-u-2": {metrics.RealtimeNumRequestsRunning: &metrics.SimpleMetricValue{Value: 100}},
	})

	router, err := NewPipelineRouter()
	require.NoError(t, err)

	ctx := types.NewRoutingContext(context.Background(), RouterPipeline, model, "", "req-3", "")
	ctx.Message = "hello"
	ctx.StrategyPipeline = []string{"unknown-stage", string(RouterLeastRequest)}

	ready := &utils.PodArray{Pods: pods}
	addr, err := router.Route(ctx, ready)
	require.NoError(t, err)
	require.NotEmpty(t, addr)
	require.NotNil(t, ctx.TargetPod())
	require.Equal(t, "p-u-1", ctx.TargetPod().Name)
}

func TestPipelineRouter_PrefixCache_UpdatesIndexAfterFinalSelection(t *testing.T) {
	model := fmt.Sprintf("pipeline-test-model-%d", time.Now().UnixNano())
	pods := []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "p-pc-1", Namespace: "default"},
			Status: v1.PodStatus{PodIP: "10.0.3.1", Conditions: []v1.PodCondition{{Type: v1.PodReady, Status: v1.ConditionTrue}}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "p-pc-2", Namespace: "default"},
			Status: v1.PodStatus{PodIP: "10.0.3.2", Conditions: []v1.PodCondition{{Type: v1.PodReady, Status: v1.ConditionTrue}}},
		},
	}
	initGlobalCacheWithPodsAndMetricsForPipelineTest(t, model, pods, map[string]map[string]metrics.MetricValue{
		"p-pc-1": {metrics.RealtimeNumRequestsRunning: &metrics.SimpleMetricValue{Value: 0}},
		"p-pc-2": {metrics.RealtimeNumRequestsRunning: &metrics.SimpleMetricValue{Value: 100}},
	})

	routerAny, err := NewPipelineRouter()
	require.NoError(t, err)
	router := routerAny.(*PipelineRouter)
	require.NoError(t, router.ensureInit())

	ctx := types.NewRoutingContext(context.Background(), RouterPipeline, model, "", "req-4", "")
	ctx.Message = "abcdefgh" // ensure at least 2 blocks with default character tokenizer
	ctx.StrategyPipeline = []string{string(RouterPrefixCache), string(RouterLeastRequest), string(RouterRandom)}

	ready := &utils.PodArray{Pods: pods}
	// Before routing: ensure no prefix match for this model/pods/message.
	tok := (&router.prefix).getTokenizerForRequest(ctx, ready)
	tokens, err := tok.TokenizeInputText(ctx.Message)
	require.NoError(t, err)
	readyPodsMap := map[string]struct{}{"p-pc-1": {}, "p-pc-2": {}}
	matchedBefore, _ := router.prefix.prefixCacheIndexer.MatchPrefix(tokens, model, readyPodsMap)
	require.Empty(t, matchedBefore)

	addr, err := router.Route(ctx, ready)
	require.NoError(t, err)
	require.NotEmpty(t, addr)
	require.NotNil(t, ctx.TargetPod())

	readyPodsMap2 := map[string]struct{}{"p-pc-1": {}, "p-pc-2": {}}
	matchedAfter, _ := router.prefix.prefixCacheIndexer.MatchPrefix(tokens, model, readyPodsMap2)
	require.Contains(t, matchedAfter, ctx.TargetPod().Name)
	require.Equal(t, 100, matchedAfter[ctx.TargetPod().Name])
}
