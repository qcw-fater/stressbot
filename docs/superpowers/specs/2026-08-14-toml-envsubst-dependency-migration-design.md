# TOML 与环境变量依赖迁移设计

## 背景

stressbot 当前使用 `github.com/BurntSushi/toml v1.6.0` 解码 TOML，并使用 `github.com/drone/envsubst v1.0.3` 展开配置字符串中的环境变量。后者已长期缺乏维护；当前代码还需要直接遍历其 `parse` AST，才能实现未定义变量的严格报错。

本次迁移将 TOML 解码替换为 `github.com/pelletier/go-toml/v2`，将环境变量展开替换为 `github.com/fluxcd/pkg/envsubst v1.7.0`。迁移只更新实现和依赖，不扩大配置文件的公开语法契约。

## 目标

- 删除 `github.com/BurntSushi/toml` 和 `github.com/drone/envsubst` 依赖。
- 使用 Pelletier v2 的严格解码模式继续拒绝未知 TOML 字段。
- 使用 FluxCD envsubst 的严格模式替代本地 AST 预检。
- 保持既有默认值预填、错误包装和环境变量展开顺序。
- 保持当前公开插值契约：`${VAR}`、`${VAR:-default}`、`$$`。
- 通过迁移前后语义对拍测试防止行为漂移。

## 非目标

- 不新增或宣传其他 Bash 参数展开语法。
- 不主动拒绝 FluxCD 库能够解析但项目未承诺的表达式；这些表达式属于未保证兼容的实现细节。
- 不在 TOML 原始字节上执行环境变量替换。
- 不改变配置结构体、配置样例或运行模式。
- 不引入日期时间、自定义 TOML 编解码或配置写回能力。

## 设计

### TOML 解码

`LoadTOML` 保持以下数据流：

1. 读取配置文件。
2. 将调用方传入的 `defaults` 复制为目标配置。
3. 创建 Pelletier v2 `Decoder`。
4. 调用 `DisallowUnknownFields()` 后解码到目标配置。
5. 对解码后的字符串字段执行环境变量展开。

Pelletier 的严格模式会以 `*toml.StrictMissingError` 返回全部未知字段。`LoadTOML` 将该错误继续包装为包含“未知字段”的中文错误，同时保留原错误作为 `%w`，使调用方仍可通过 `errors.As` 获取字段位置等诊断信息。其他解析或类型错误继续包装为“解析配置文件”。

### 环境变量展开

`expandString` 保留不含 `$` 时直接返回的快速路径。其余输入调用：

```go
envsubst.EvalEnv(s, true)
```

`strict=true` 使用 `os.LookupEnv`：裸 `${VAR}` 未定义时报错；带默认值的 `${VAR:-default}` 仍正常取默认值；已定义为空的裸变量仍展开为空字符串；`$$` 继续输出字面美元符号。

因此删除以下 Drone 专属实现：

- `github.com/drone/envsubst/parse` import；
- `defaultOps`；
- `collectUndefinedNoDefault`；
- 两阶段 AST 预检与展开逻辑。

FluxCD 严格模式通常在首个未定义变量处返回错误，不再一次收集同一字符串里的全部缺失变量。这不改变 fail-loud 契约，属于可接受的诊断差异。

### 反射遍历

`ExpandConfigStrings` 和 `expandValue` 的作用范围不变：递归处理 `string`、`*string`、struct 和 `map[string]string` 的值，跳过其他类型。环境变量展开仍发生在 TOML 成功解码之后，避免环境变量内容改变 TOML 语法或字段类型。

## 错误处理

- 文件读取失败：继续返回“读取配置文件”。
- TOML 未知字段：返回包含配置路径和“未知字段”的错误，并保留 `StrictMissingError` 错误链。
- 其他 TOML 解析/类型错误：继续返回“解析配置文件”。
- 环境变量表达式非法或裸变量未定义：继续由 `ExpandConfigStrings` 向上包装为“展开环境变量”。
- 所有新增或调整的项目错误文案使用中文；第三方库底层错误作为 `%w` 保留。

## 测试策略

先添加能够区分新旧实现的测试并确认 RED，再修改生产代码：

- `LoadTOML` 未知字段错误可以 `errors.As` 为 Pelletier `StrictMissingError`，并包含未知字段位置。
- `expandString` 的未定义变量错误包含缺失变量名。

然后运行并保留现有契约测试：

- 简单和嵌入式 `${VAR}` 展开；
- `${VAR:-default}` 在已定义、未定义、定义为空三种状态下的行为；
- 已定义为空的裸 `${VAR}`；
- 未定义裸变量报错；
- `$$` 转义；
- 混合表达式和空默认值；
- struct、指针和 `map[string]string` 递归展开；
- 默认值预填与 TOML 文件覆盖；
- TOML 未知字段严格拒绝。

## 验证

代码修改后执行：

1. `go test ./config`
2. `go test ./...`
3. `go build ./...`
4. `cd cmd/web && npx.cmd tsc -b`
5. `cd cmd/web && npm.cmd run test`

本次只变更通用配置加载依赖，不涉及 flow.json 或运行时网络行为，因此不需要编辑器配置校验和 2～5 分钟压测运行验证。
