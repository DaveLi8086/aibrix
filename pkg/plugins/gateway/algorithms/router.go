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
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/vllm-project/aibrix/pkg/types"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

const (
	RouterNotSet = ""
)

var (
	ErrInitTimeout           = errors.New("router initialization timeout")
	ErrFallbackNotSupported  = errors.New("router not support fallback")
	ErrFallbackNotRegistered = errors.New("fallback router not registered")
	defaultRM                = NewRouterManager()
)

type RouterManager struct {
	routerInited      context.Context
	routerDoneInit    context.CancelFunc
	routerFactory     map[types.RoutingAlgorithm]types.RouterProviderFunc
	routerConstructor map[types.RoutingAlgorithm]types.RouterProviderRegistrationFunc
	routerMu          sync.RWMutex
}

func NewRouterManager() *RouterManager {
	rm := &RouterManager{}
	rm.routerInited, rm.routerDoneInit = context.WithTimeout(context.Background(), 1*time.Second)
	rm.routerFactory = make(map[types.RoutingAlgorithm]types.RouterProviderFunc)
	rm.routerConstructor = make(map[types.RoutingAlgorithm]types.RouterProviderRegistrationFunc)
	return rm
}

// Validate validates if user provided routing routers is supported by gateway
func (rm *RouterManager) Validate(algorithms string) (types.RoutingAlgorithm, bool) {
	rm.routerMu.RLock()
	defer rm.routerMu.RUnlock()
	if _, ok := rm.routerFactory[types.RoutingAlgorithm(algorithms)]; ok {
		return types.RoutingAlgorithm(algorithms), ok
	} else {
		return RouterNotSet, false
	}
}
func Validate(algorithms string) (types.RoutingAlgorithm, bool) {
	return defaultRM.Validate(algorithms)
}

// Select the user provided router provider supported by gateway, no error reported and fallback to random router
// Call Validate before this function to ensure expected behavior.
func (rm *RouterManager) Select(ctx *types.RoutingContext) (types.Router, error) {
	rm.routerMu.RLock()
	defer rm.routerMu.RUnlock()
	if provider, ok := rm.routerFactory[ctx.Algorithm]; ok {
		return provider(ctx)
	} else {
		klog.Warningf("Unsupported router strategy: %s, use %s instead.", ctx.Algorithm, RouterRandom)
		return RandomRouter, nil
	}
}
func Select(ctx *types.RoutingContext) (types.Router, error) {
	return defaultRM.Select(ctx)
}

func (rm *RouterManager) Register(algorithm types.RoutingAlgorithm, constructor types.RouterConstructor) {
	rm.routerMu.Lock()
	defer rm.routerMu.Unlock()
	rm.routerConstructor[algorithm] = func() types.RouterProviderFunc {
		router, err := constructor()
		if err != nil {
			klog.Errorf("Failed to construct router for %s: %v", algorithm, err)
			return nil
		}
		return func(_ *types.RoutingContext) (types.Router, error) {
			return router, nil
		}
	}
}
func Register(algorithm types.RoutingAlgorithm, constructor types.RouterConstructor) {
	defaultRM.Register(algorithm, constructor)
}

func (rm *RouterManager) RegisterProvider(algorithm types.RoutingAlgorithm, provider types.RouterProviderFunc) {
	rm.routerMu.Lock()
	defer rm.routerMu.Unlock()
	rm.routerFactory[algorithm] = provider
	klog.Infof("Registered router for %s", algorithm)
}
func RegisterProvider(algorithm types.RoutingAlgorithm, provider types.RouterProviderFunc) {
	defaultRM.RegisterProvider(algorithm, provider)
}

func (rm *RouterManager) SetFallback(router types.Router, fallback types.RoutingAlgorithm) error {
	r, ok := router.(types.FallbackRouter)
	if !ok {
		return ErrFallbackNotSupported
	}

	<-rm.routerInited.Done()
	initErr := rm.routerInited.Err()
	if initErr != context.Canceled {
		return fmt.Errorf("router did not initialized: %v", initErr)
	}

	rm.routerMu.RLock()
	defer rm.routerMu.RUnlock()

	if provider, ok := rm.routerFactory[fallback]; !ok {
		return ErrFallbackNotRegistered
	} else {
		r.SetFallback(fallback, provider)
	}
	return nil
}
func SetFallback(router types.Router, fallback types.RoutingAlgorithm) error {
	return defaultRM.SetFallback(router, fallback)
}

func (rm *RouterManager) Init() {
	rm.routerMu.Lock()
	defer rm.routerMu.Unlock()
	for algorithm, constructor := range rm.routerConstructor {
		rm.routerFactory[algorithm] = constructor()
		klog.Infof("Registered router for %s", algorithm)
	}
	rm.routerDoneInit()
}
func Init() {
	defaultRM.Init()
}

// =============================================================================
// Multi-Strategy Routing Support
// =============================================================================

// Reorderer defines the interface for routing strategies that support
// multi-strategy chain execution through reordering.
type Reorderer interface {
	// Reorder takes the current pod groups and reorders them based on strategy logic.
	// It returns new pod groups after reordering.
	Reorder(ctx *types.RoutingContext, podGroups PodGroups) PodGroups
}

// PodGroups represents a collection of pod groups.
// Each group contains pods that are considered equivalent at current strategy level.
// Groups are ordered by priority (higher priority groups come first).
type PodGroups [][]*v1.Pod

// Flatten flattens all pod groups into a single ordered pod list.
// Pods within each group maintain their relative order.
func (pg PodGroups) Flatten() []*v1.Pod {
	var result []*v1.Pod
	for _, group := range pg {
		result = append(result, group...)
	}
	return result
}

// ToPodList converts PodGroups to types.PodList interface implementation.
func (pg PodGroups) ToPodList() types.PodList {
	return &utils.PodArray{Pods: pg.Flatten()}
}

// Contains checks if a pod is contained in any group.
func (pg PodGroups) Contains(pod *v1.Pod) bool {
	for _, group := range pg {
		for _, p := range group {
			if p.Name == pod.Name && p.Namespace == pod.Namespace {
				return true
			}
		}
	}
	return false
}

// Count returns total number of pods across all groups.
func (pg PodGroups) Count() int {
	count := 0
	for _, group := range pg {
		count += len(group)
	}
	return count
}

// MultiRouter handles multi-strategy routing chain execution.
type MultiRouter struct {
	strategies []types.RoutingAlgorithm
	reorderers map[types.RoutingAlgorithm]Reorderer
}

// NewMultiRouter creates a new MultiRouter with the given strategy chain.
func NewMultiRouter(strategies []types.RoutingAlgorithm) *MultiRouter {
	return &MultiRouter{
		strategies: strategies,
		reorderers: make(map[types.RoutingAlgorithm]Reorderer),
	}
}

// RegisterReorderer registers a reorderer for a specific routing algorithm.
func (mr *MultiRouter) RegisterReorderer(algorithm types.RoutingAlgorithm, reorderer Reorderer) {
	mr.reorderers[algorithm] = reorderer
}

// Execute runs the multi-strategy chain and returns the final ordered pod list.
func (mr *MultiRouter) Execute(ctx *types.RoutingContext, initialPods types.PodList) PodGroups {
	// Initialize with all pods in a single group
	podGroups := PodGroups{initialPods.All()}

	// Execute each strategy in sequence
	for _, strategy := range mr.strategies {
		if reorderer, ok := mr.reorderers[strategy]; ok {
			// Use registered reorderer
			podGroups = reorderer.Reorder(ctx, podGroups)
		} else {
			// Fallback: try to get router and use it as reorderer if it implements the interface
			if router, err := Select(ctx); err == nil {
				if r, ok := router.(Reorderer); ok {
					podGroups = r.Reorder(ctx, podGroups)
				}
			}
		}
	}

	return podGroups
}

// ParseStrategyChain parses a comma-separated strategy chain string.
// Returns the list of strategies and an error if any strategy is invalid.
func ParseStrategyChain(chain string) ([]types.RoutingAlgorithm, error) {
	if chain == "" {
		return nil, fmt.Errorf("empty strategy chain")
	}

	parts := strings.Split(chain, ",")
	strategies := make([]types.RoutingAlgorithm, 0, len(parts))

	for _, part := range parts {
		strategy := types.RoutingAlgorithm(strings.TrimSpace(part))
		if strategy == "" {
			continue
		}

		// Validate that the strategy is registered
		if _, ok := defaultRM.routerFactory[strategy]; !ok {
			return nil, fmt.Errorf("unknown routing strategy: %s", strategy)
		}

		strategies = append(strategies, strategy)
	}

	if len(strategies) == 0 {
		return nil, fmt.Errorf("no valid strategies in chain")
	}

	return strategies, nil
}

// IsMultiStrategy checks if the algorithm string represents a multi-strategy chain.
func IsMultiStrategy(algorithm string) bool {
	return strings.Contains(algorithm, ",")
}

// MultiStrategyRoute executes multi-strategy routing chain.
// This is the main entry point for multi-strategy routing.
func MultiStrategyRoute(ctx *types.RoutingContext, readyPodList types.PodList, strategyChain string) (string, error) {
	// Parse strategy chain
	strategies, err := ParseStrategyChain(strategyChain)
	if err != nil {
		return "", fmt.Errorf("failed to parse strategy chain: %w", err)
	}

	// Create multi-router
	mr := NewMultiRouter(strategies)

	// Register reorderers for known strategies
	for _, strategy := range strategies {
		if router, err := Select(&types.RoutingContext{Algorithm: strategy}); err == nil {
			if reorderer, ok := router.(Reorderer); ok {
				mr.RegisterReorderer(strategy, reorderer)
			}
		}
	}

	// Execute multi-strategy chain
	podGroups := mr.Execute(ctx, readyPodList)

	// Flatten to get ordered pod list
	orderedPods := podGroups.Flatten()

	if len(orderedPods) == 0 {
		return "", fmt.Errorf("no pods available after multi-strategy routing")
	}

	// Select first available pod (with fallback support)
	// The orderedPods list preserves the priority order determined by all strategies
	targetPod := orderedPods[0]
	ctx.SetTargetPod(targetPod)

	klog.V(4).InfoS("multi-strategy routing completed",
		"requestID", ctx.RequestID,
		"strategies", strategies,
		"selectedPod", targetPod.Name,
		"totalCandidates", len(orderedPods),
	)

	return ctx.TargetAddress(), nil
}
