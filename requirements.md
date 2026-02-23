# COCO/Trae CLI 执行指导：AIBrix 多路由 MVP

你好！请你扮演一名资深的云原生 Go 工程师，在 [aibrix](https://github.com/DaveLi8086/aibrix) repo中，为我实现 [Issue #1843](https://github.com/vllm-project/aibrix/issues/1843) 所需的“多路由 MVP（最小改动方案）”。
以下是完整的任务说明和分步执行指令，请严格遵循。COCO/Trae CLI 执行指导：AIBrix 多路由 MVP

以下是完整的任务说明和分步执行指令，请严格遵循。

### 1. 使用前准备

- **工作目录**：请确认你的当前工作目录在 `inf_aibrix_repo` 仓库的根目录下。
- **分支约定**：
  - 在开始任何修改前，请基于 `master` 分支创建一个新的特性分支。分支名请遵循 `aime/{timestamp}-multi-routing-mvp` 格式。
  - 之后的所有代码提交都在这个新分支上进行。
- **代码规范**：
  - **禁止推送**：在整个任务过程中，你只需要在本地提交（commit），**绝对不要将任何改动推送到远程仓库（push）**。
  - **遵循风格**：请保持与现有代码库一致的编码风格和命名约定。

### 2. 任务目标与范围

本次任务是实现一个**最小可行产品（****MVP****）**，核心目标是在 AIBrix 的 Gateway 插件中支持“多策略顺序管线”路由，同时确保对现有功能的向后兼容。

**你要做的事 (In Scope):**

1. **新增** **`pipeline`** **路由****算法**：创建一个名为 `pipeline` 的新路由算法。当用户在请求中指定 `routing-strategy: pipeline` 时，此算法将被激活。
2. **解析策略序列**：
   1. `pipeline` 算法需要能解析来自 HTTP 请求头 `routing-strategies` (例如: `"prefix-cache,least-request"`) 或 YAML 中 `routing_strategies` 字段定义的策略序列。
   2. 该序列定义了子策略的执行顺序。
3. **复用现有算法逻辑**：`pipeline` 算法本身不实现具体的路由逻辑，而是按顺序调用（复用）`random`、`least-request`、`prefix-cache` 等现有算法的核心逻辑，对候选 Pod 集合进行链式过滤。
4. **向后兼容**：所有改动不能影响现有的单一策略路由行为。当用户不使用 `pipeline` 策略时，系统表现应与修改前完全一致。

**你不必做的事 (Out of Scope):**

1. **不引入新的 CRD**：本次任务不涉及 Kubernetes Custom Resource Definition (CRD) 的创建或修改。所有配置通过请求头或现有 YAML 文件完成。
2. **不做分布式状态管理**：你不需要解决多 Gateway 副本间的状态一致性问题。所有路由状态（如 `least-request` 的计数器）继续保持在本地内存中。
3. **不修改** **`Route()`** **接口签名**：为了保持兼容性，现有 `types.Router` 接口的 `Route()` 方法签名 (`(string, error)`) 必须保持不变。

### 3. 分步执行指令

请严格按照以下步骤和文件路径执行修改。

#### **步骤 1：环境设置与分支创建**

1. 执行 `git checkout master && git pull` 更新主分支。
2. 执行 `git checkout -b aime/$(date +%s)-multi-routing-mvp` 创建并切换到新分支。

#### **步骤 2：核心代码修改**

**文件 1：****`pkg/types/router_context.go`** **(修改)**

- **职责**：为 `RoutingContext` 增加一个字段，用于在请求的生命周期内携带策略序列。

- **建议**：

  ````go
  // 在 RoutingContext 结构体中 
  StrategyPipeline []string 
  // 在 reset 方法中 
  request.StrategyPipeline = nil
  ````

**文件 2：****`pkg/plugins/gateway/gateway_req_headers.go`** **(修改)**

- **职责**：解析新的 `routing-strategies` 请求头。
- **建议**：
  - 在 `HandleRequestHeaders` 函数中，调用 `getRoutingStrategy` 之后，增加一段逻辑：
  - 如果 `routingCtx.Algorithm` 是 `pipeline`，则从请求头 `headers` 中查找 `routing-strategies` (常量 `HeaderRoutingStrategies`)。
  - 如果找到，用 `,` 分割字符串，并将得到的策略名切片存入 `routingCtx.StrategyPipeline`。
  - 如果未找到，可以留空或设置一个默认的降级策略序列（如 `["random"]`）。

**文件 3：****`pkg/plugins/gateway/algorithms/router.go`** **(修改)**

- **职责**：注册新的 `pipeline` 路由算法。
- **建议**：
  - 在文件顶部增加常量 `const RouterPipeline types.RoutingAlgorithm = "pipeline"`。
  - 在 `init()` 函数中，调用 `Register(RouterPipeline, NewPipelineRouter)` 来注册构造函数。

**文件 4：****`pkg/plugins/gateway/algorithms/random.go`** **(修改)**

- **职责**：解耦核心选择逻辑，使其可被 `pipeline` 复用。
- **建议**：
  - 创建一个新的可导出函数 `SelectAsList(pods types.PodList) (types.PodList, error)`。
  - 此函数实现从 `pods` 中随机选择一个，并返回一个只包含该 Pod 的新 `PodList`。
  - 修改原有的 `Route()` 方法，使其内部调用 `SelectAsList()`，然后从返回的单元素 `PodList` 中取出 Pod 并返回其地址。

**文件 5：****`pkg/plugins/gateway/algorithms/least_request.go`** **(修改)**

- **职责**：解耦核心选择逻辑。
- **建议**：
  - 将 `selectTargetPodWithLeastRequestCount` 的逻辑提取为一个新的可导出函数 `SelectAsList(cache cache.Cache, pods types.PodList) (types.PodList, error)`。
  - 此函数应返回一个 `PodList`，其中包含所有负载最低的 Pod（而非只返回一个）。
  - 修改 `Route()` 方法，使其调用 `SelectAsList()`，并在返回的 Pod 列表中随机选择一个作为最终目标（复用 `random` 的逻辑）。

**文件 6：****`pkg/plugins/gateway/algorithms/prefix_cache.go`** **(修改)**

- **职责**：解耦核心选择逻辑。
- **建议**：
  - 创建一个新的可导出函数 `SelectAsList(p *prefixCacheRouter, ctx *types.RoutingContext, readyPods types.PodList) (types.PodList, error)`。
  - 此函数应包含 `routeOriginal` 方法中的核心逻辑：负载不均衡检查、前缀匹配、从匹配 Pod 中选择目标。
  - **关键**：它应返回一个包含所有“最佳”候选者的 `PodList`，而不是最终的单个 Pod。例如，返回所有命中率最高且负载最低的 Pod。
  - 修改 `Route()` 方法，使其调用 `SelectAsList()`，然后在返回的列表中随机选择一个。

**文件 7：****`pkg/plugins/gateway/algorithms/pipeline.go`** **(新增)**

- **职责**：实现 `pipeline` 算法的核心编排逻辑。
- **建议**：
  - 定义 `PipelineRouter` 结构体，实现 `types.Router` 接口。
  - `NewPipelineRouter` 构造函数用于初始化。
  - 在 `Route()` 方法中实现以下逻辑：
    - 从 `RoutingContext` 中获取 `StrategyPipeline`。如果为空，则直接调用 `random` 逻辑并返回。
    - 初始化一个 `currentPods PodList`，其值为输入的 `readyPodList`。
    - **循环**遍历 `StrategyPipeline` 中的每个策略名：a. 根据策略名，调用对应算法包中解耦出的 `SelectAsList()` 函数（例如 `least_request.SelectAsList(..., currentPods)`）。b. 如果返回的 `PodList` 为空或出错，记录一条**结构化日志**（含 `request_id`, `stage`, `error`），然后**立即中断循环**，使用 `currentPods`（即上一阶段的结果）作为最终候选集。c. 如果成功，将 `currentPods` 更新为新返回的 `PodList`。
    - 循环结束后，从最终的 `currentPods` 列表中随机选择一个 Pod 作为目标。
    - 调用 `ctx.SetTargetPod()` 并返回目标地址。

#### **步骤 3：测试与验证**

**文件 8：****`pkg/plugins/gateway/algorithms/pipeline_test.go`** **(新增)**

- **职责**：为 `PipelineRouter` 编写单元测试。
- **要求**：
  - Mock `types.PodList` 和 `RoutingContext`。
  - 测试场景应覆盖：
    - 正常的两阶段 pipeline (`prefix-cache`, `least-request`)。
    - 策略序列为空，降级为随机。
    - 中间阶段返回空候选集，正确降级到上一阶段结果。
    - 策略序列中包含无效策略名，应跳过并记录日志。

**文件 9：其他** **`..._test.go`** **文件 (修改)**

- **职责**：为 `random`、`least-request`、`prefix-cache` 中解耦出的新 `SelectAsList` 函数补充单元测试。

#### **步骤 4：示例与日志**

**文件 10：****`samples/pipeline_routing_example.yaml`** **(新增)**

- **职责**：提供一个可运行的 YAML 示例来演示 `pipeline` 路由。

- **内容建议**（如果 `benchmarks/L20` 目录或类似文件不存在）：

  - ````yaml
    # 模仿现有 aibrix serving/deployment yaml 结构
    apiVersion: apps/v1
    kind: Deployment
    metadata:
      name: my-pipeline-model
    spec:
      template:
        metadata:
          annotations:
            # 通过 annotation 配置 pipeline
            routing.aibrix.ai/strategy: "pipeline"
            routing.aibrix.ai/strategies: "prefix-cache,least-request"
    ...
    
    ````

  -  **如果 Annotation 方式不适用，请在 Gateway 的****环境变量****中配置。**

**日志与指标要求**：

- 在 `PipelineRouter` 的 `Route` 方法中，为每次路由决策输出一条**结构化日志**。日志应包含：
  - `request_id`
  - `input_strategies` (输入的策略序列)
  - `executed_stages` (每个阶段的名称、输入 Pod 数、输出 Pod 数)
  - `final_decision` (最终选择的 Pod 和原因，如 "randomly selected from 2 candidates")
  - `fallback_reason` (如果发生降级)
- 新增一个 Prometheus Counter：`aibrix_gateway_routing_pipeline_requests_total`，标签为 `final_strategy` (最终决策的策略) 和 `result` (`success`/`fallback`)。

### 4. 验收清单

请在完成所有编码后，对照以下清单进行自检：

- [ ] **编译****通过**：`go build ./...` 命令无错误。**
- [ ] 单元测试通过**：`go test ./...` 命令无失败。**
- [ ] 默认行为不变**：当不使用 `pipeline` 策略时，原有的 `random`, `least-request`, `prefix-cache` 路由行为与修改前完全一致。**
- [ ] Pipeline 生效**：通过发送带 `routing-strategy: pipeline` 和 `routing-strategies: ...` 的请求，可以观察到日志中打印了正确的管线执行流程。**
- [ ] 示例可运行**：新增的 `samples/pipeline_routing_example.yaml` 部署后，流量能够正确路由到目标 Pod。

### 5. 交互提示话术

在你实现的过程中，我可能会随时向你提问。以下是一些你可能会用到的自检或交互话术：

- **检查状态**：“好的，我已经完成了对 `[文件名]` 的修改。现在让我运行 `git status` 来确认改动范围，并用 `go vet ./...` 检查是否存在明显的语法问题。”
- **生成提交信息**：“我已经完成了 `[功能点]` 的开发和测试。我将生成一条清晰的 commit message，例如：`feat(gateway): introduce pipeline router for multi-strategy routing`，然后执行 `git commit`。我不会执行 `git push`。”
- **输出改动摘要**：“本次改动主要涉及以下文件：[文件列表]。核心变更是新增了 `PipelineRouter`，并对现有路由算法进行逻辑解耦，以支持可复用的链式调用。所有改动都保持了接口的向后兼容性。”

请开始执行任务。如果你在任何步骤遇到困难，请明确指出问题所在。