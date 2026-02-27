
# AIBrix 多路由策略 (级联过滤) 开发需求

## 1. 范围 (Scope)

本次开发目标是在 AIBrix 网关中实现**级联过滤 (Cascaded Filter chain)** 的多路由策略，同时严格遵循**最小化代码改动**的约束。

- **核心功能**: 支持用户通过请求头 `routing-strategy: "x,y,z"` 动态指定一个策略链。
- **执行语义**:
    1.  系统获取所有可用的 Pods。
    2.  应用策略 `x` 的 `Filter` 方法，过滤出一个候选子集。
    3.  在 `x` 的结果集上，应用策略 `y` 的 `Filter` 方法，进一步缩小范围。
    4.  最后，由策略 `z` 的 `Route` 方法在 `y` 过滤后的结果集中选出唯一的目标 Pod。
- **动态组合**: 策略组合在请求时按需解析、校验和构建，并对组合结果进行缓存以提高后续请求效率。

## 2. 非目标 (Non-Goals)

- **不引入通用 Pipeline 引擎**: 避免引入复杂的框架或多阶段抽象，仅通过一个轻量级的组合器 (CompositeRouter) 实现。
- **不修改不相关代码**: 严格限制修改范围，禁止对现有路由算法的 `Route` 接口、核心逻辑以及与本次需求无关的文件进行任何形式的修改（包括格式化和重构）。
- **尽量缩小改动范围**: 不要改动不相关的代码（即使是看到bug或lint issue也不行），也不要改动已有comments（除非相关代码改动巨大）。每个现有route策略go文件都不要动。最好整体改动只覆盖两个文件，涉及几十行代码。

### 3.2. 请求处理

- **Header**: `routing-strategy: "x,y,z"`
- **归一化**: 在 `getRoutingStrategy` 中，对 Header 值进行归一化：去除首尾空格，并将逗号周围的空格压缩。例如，`"x, y, z"` 处理后等价于 `"x,y,z"`。
- **错误处理**:
    - 如果指定了一个不存在的路由策略或filter应用之后返回空，均fallback到random策略并在debug log里解释详情

## 4. 可能的实现方式（不一定全，请根据实际情况调整）

### 4.1. `CompositeRouter` 实现

- **文件位置**: 在 `pkg/plugins/gateway/algorithms/` 目录下创建新文件，如 `composite.go`。
- **核心逻辑**:
    1.  `CompositeRouter` 内部持有一个 `[]types.Router` 列表，代表 `x,y,z` 策略链。
    2.  实现 `Route()` 方法：
        - 遍历除最后一个策略外的所有 `routers` (即 `x` 和 `y`)。
        - 对每个 `router`，进行类型断言 `if fr, ok := router.(types.FilterRouter); ok`。
        - 如果断言成功，调用 `fr.Filter()` 并更新候选 Pod 列表。
        - 如果过滤后列表为空，立即返回错误。
        - 遍历结束后，调用最后一个策略 (即 `z`) 的 `Route()` 方法，在最终的候选集上选出唯一目标。

### 4.3. 请求识别与归一化

- **文件位置**: 修改 `pkg/plugins/gateway/util.go` (或 `gateway_req_headers.go`) 中的 `getRoutingStrategy`。
- **逻辑**:
    1.  当检测到 `routing-strategy` 值包含逗号时：
        - 将 `RoutingContext.Algorithm` 设置为一个特殊的内部值，例如 `"composite"`。
        - 在 `RoutingContext.ReqHeaders` 中保留原始的、未经修改的 `routing-strategy` Header 值，供 `CompositeRouter` 的 `Provider` 解析。
    2.  `Validate()` 函数只需将 `"composite"` 识别为有效算法即可。

## 5. 测试与验收 (Tests & Acceptance Criteria)

### 5.1. 单元测试

更新现有routing unit test, 把多路由的valid use case(使用多策略但实际args只有一种策略，或多策略)和invalid use case(使用不存在的策略，或空字符串，或策略存在但没有符合的pod)都包含进去，并制定好expected results。

### 5.2. E2E 测试

- 在 `test/e2e/routing_strategy_test.go` 中新增测试用例：
    - **Case 1**: `routing-strategy: "prefix-cache,least-request"`，断言请求成功，行为符合预期。
    - **Case 2**: `routing-strategy: "random,least-request"`，断言请求成功。
    - **Case 3**: `routing-strategy: "unknown-strategy,least-request"`，断言服务fallback到random并返回。
- 确保所有现有的单策略 E2E 测试用例保持通过，其行为不受影响。

## 6. 发布与兼容性 (Rollout & Compatibility)

- **无开关发布**: 该功能默认启用，因为它仅对使用逗号分隔格式的 `routing-strategy` 请求生效，对现有单策略完全无影响。
- **完全向后兼容**: 现有的使用诸如 `routing-strategy: "random"` 或 `routing-strategy: "least-request"` 的单策略请求，其行为和逻辑保持不变。

## 7. 日志要求 (Logging Requirements)

- 在 `CompositeRouter` 执行时，增加一条 **Debug 级别**的日志，记录正在执行的策略链，例如：`Executing  strategy chain: [prefix-cache, least-request]`。
- 保留现有的 `target-pod` 和 `routing-strategy` 响应头，便于问题排查。
