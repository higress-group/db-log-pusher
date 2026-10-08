## DB 日志推送插件 (db-log-pusher) 和日志收集服务 (db-log-collector)

`db-log-pusher` 是一个 WASM 插件，用于收集 HTTP 请求/响应日志，并将这些日志推送到外部收集器服务 (`db-log-collector`) 进行存储和分析。这两个组件共同构成了完整的日志收集解决方案。

> **官方镜像**：
> - `db-log-pusher`: `oci://opensource-registry.cn-hangzhou.cr.aliyuncs.com/plugins/db-log-pusher:latest`
> - `db-log-collector`: `opensource-registry.cn-hangzhou.cr.aliyuncs.com/higress-group/log-collector:latest`

## 一、db-log-pusher 功能特性

- **全面的日志收集**: 捕获请求/响应的完整信息，包括基础信息、流量统计、连接信息等
- **AI 日志支持**: 特别针对 AI 应用场景，支持收集模型调用日志和 token 统计
- **灵活的配置**: 支持自定义收集器服务地址和路径
- **实时推送**: 异步将日志实时推送到外部收集器
- **性能优化**: 采用非阻塞方式发送日志，不影响主业务流程
- **智能客户端**: 自动创建内部集群客户端，使用 `collector_service_name` 和 `collector_port` 配置建立连接
- **超时处理**: 包含 5 秒超时设置，防止长时间阻塞
- **错误处理**: 记录发送失败和异常情况，不影响主业务流程
- **数据库存储**: 内置数据库存储机制，用于持久化日志管理

## 配置参数

| 参数 | 类型 | 必填 | 默认值 | 描述 |
|------|------|------|--------|------|
| `collector_service_name` | string | 是 | - | 收集器服务地址，需为网关数据面可解析、可访问的 FQDN 或服务地址，例如 `log-collector.higress-system.svc.cluster.local` |
| `collector_port` | int | 是 | - | 收集器端口，例如 `80` |
| `collector_path` | string | 否 | `/` | 接收日志的 API 路径，例如 `/ingest` |
| `instance_id` | string | 否 | 自动从网关元数据读取 | 网关实例 ID，用于实例维度查询 |

## 收集的数据字段

插件会收集以下类型的详细信息：

### 基础请求信息
- `start_time`: 请求开始时间
- `authority`: Host/Authority
- `method`: HTTP 方法
- `path`: 请求路径
- `protocol`: HTTP 协议版本
- `request_id`: X-Request-ID
- `trace_id`: X-B3-TraceID
- `user_agent`: User-Agent
- `x_forwarded_for`: X-Forwarded-For

### 响应信息
- `response_code`: 响应状态码
- `response_flags`: Envoy 响应标志
- `response_code_details`: 响应码详情

### 流量信息
- `bytes_received`: 接收字节数
- `bytes_sent`: 发送字节数
- `duration`: 请求总耗时(毫秒)

### 上游信息
- `upstream_cluster`: 上游集群名
- `upstream_host`: 上游主机
- `upstream_service_time`: 上游服务耗时
- `upstream_transport_failure_reason`: 上游传输失败原因

### 连接信息
- `downstream_local_address`: 下游本地地址
- `downstream_remote_address`: 下游远程地址
- `upstream_local_address`: 上游本地地址

### 路由信息
- `route_name`: 路由名称
- `requested_server_name`: SNI

### AI 相关信息
- `ai_log`: WASM AI 日志
- `input_tokens`: 输入 token 数量
- `output_tokens`: 输出 token 数量
- `total_tokens`: 总 token 数量
- `model`: 模型名称
- `api`: API 名称
- `consumer`: 消费者信息

### 监控元数据
- `instance_id`: 实例 ID
- `route`: 路由
- `service`: 服务
- `mcp_server`: MCP Server
- `mcp_tool`: MCP Tool

### 请求/响应体
- `request_body`: 请求体
- `response_body`: 响应体

响应体逐块透传，不会为了写访问日志而缓存完整响应。日志字段 `resp_body` 最多记录前 16 KiB；超过上限时 `resp_body_truncated` 为 `true`。插件还会保留末尾 16 KiB 以尝试提取大响应末尾的 token 用量，但不会将末尾样本写入 `resp_body`。这不改变客户端收到的响应正文。

## 配置方式

### 方式一：通过 Higress Console 配置（推荐）

这是最简单直接的配置方式，通过 Higress Console 的图形化界面即可完成插件安装和配置。

#### 操作步骤

1. **访问 Higress Console**
   - 登录 Higress Console 管理页面
   - 导航到 **插件配置** -> **添加插件**

2. **填写插件信息**
   - **插件名称**: `db-log-pusher-plugin`
   - **插件描述**: `Collect HTTP request logs to database`
   - **镜像地址**: `oci://opensource-registry.cn-hangzhou.cr.aliyuncs.com/plugins/db-log-pusher:latest`
   - **插件执行阶段**: 选择 **认证阶段** (AUTHN)
   - **插件执行优先级**: `1000`（需要高于 `ai-statistics`，同一阶段内数字越大，请求阶段越先执行、响应阶段越后执行）
   - **插件拉取策略**: 选择 **总是拉取** (Always)

3. **配置路由和策略**
   - 在插件配置页面，点击"添加匹配规则"
   - 在 **ingress** 列表中选择或输入需要应用此插件的服务名称，例如：
     - `model-api-qwen3-plus-0`
     - `travel-assistant`

4. **配置插件参数**
   - 在 **自定义插件配置** 区域，选择刚才创建的 `db-log-pusher` 插件
   - 在参数配置表单中，逐行填写以下参数（每行一个参数，格式为 `key: value`）：
   ```
   log_level: info
   collector_service_name: log-collector.higress-system.svc.cluster.local
   collector_port: 80
   collector_path: /ingest
   ```
   - 确保 **configDisable** 设置为 `false`（启用配置）

5. **保存配置**
   - 点击"保存"按钮完成配置
   - Higress 会自动部署插件到网关

#### 配置说明

- **执行阶段**: 选择认证阶段（AUTHN），使 `db-log-pusher` 在请求过滤器链中位于 `ai-statistics` 之前，从而在响应阶段晚于 `ai-statistics` 读取 AI 日志
- **优先级**: 建议设置为 1000，确保同一阶段下高于 `ai-statistics`；同一阶段内数字越大，请求阶段越先执行、响应阶段越后执行
- **拉取策略**: 使用 `latest` 时建议选择 Always，确保网关重新拉取仓库中当前的 `latest` 内容

#### 验证配置

配置保存后，可以通过以下方式验证：
- 查看 Higress Console 插件列表，确认插件状态正常
- 访问配置的服务，检查日志是否正常发送到收集器

---

### 方式二：通过 Kubernetes YAML 配置

如果您更喜欢使用 Kubernetes 原生配置方式，可以通过创建 WasmPlugin 资源来部署插件。

```yaml
apiVersion: extensions.higress.io/v1alpha1
kind: WasmPlugin
metadata:
  name: db-log-pusher-plugin
  namespace: higress-system
  annotations:
    higress.io/comment: "DB Log Pusher Plugin for collecting request logs"
    higress.io/wasm-plugin-title: "DB Log Pusher"
    higress.io/wasm-plugin-description: "Collect HTTP request logs to database"
  labels:
    higress.io/wasm-plugin-name: db-log-pusher
    higress.io/wasm-plugin-category: logging
spec:
  url: oci://opensource-registry.cn-hangzhou.cr.aliyuncs.com/plugins/db-log-pusher:latest
  defaultConfigDisable: true  # 默认关闭全局配置
  failStrategy: FAIL_OPEN      # 失败时放行，避免影响业务
  imagePullPolicy: Always  # 总是拉取最新版本
  phase: AUTHN           # 请求阶段早于默认阶段，响应阶段晚于默认阶段
  priority: 1000         # 同阶段内请求越先执行，响应越后执行
  # 匹配规则：应用到所有服务
  matchRules:
    - configDisable: false
      ingress:
        - model-api-qwen3-plus-0
        - travel-assistant
      config:
        log_level: info  # 可选
        collector_service_name: "log-collector.higress-system.svc.cluster.local"
        collector_port: 80
        collector_path: "/ingest"
        # instance_id: "higress-gateway"  # 可选，不配置时会尝试从网关元数据读取
```

应用配置：

```bash
kubectl apply -f db-log-pusher.yaml
```

---

## 二、配套组件：Log Collector 部署

`db-log-pusher` 插件需要配合日志收集服务一起使用。以下是一个简单的日志收集器部署示例。

### 1. 准备数据库

首先创建一个 MySQL 数据库用于存储日志数据。执行以下 SQL 创建表结构：

```sql
CREATE DATABASE IF NOT EXISTS higress_poc
  DEFAULT CHARACTER SET utf8mb4
  COLLATE utf8mb4_unicode_ci;

USE higress_poc;

CREATE TABLE IF NOT EXISTS access_logs (
  id bigint NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  start_time bigint NULL DEFAULT NULL COMMENT '请求开始时间(Unix epoch 秒)',
  trace_id varchar(64) NULL DEFAULT NULL COMMENT 'X-B3-TraceID 分布式追踪ID',
  authority varchar(128) NULL DEFAULT NULL COMMENT 'Host/Authority 域名',
  method varchar(16) NULL DEFAULT NULL COMMENT 'HTTP 方法 (GET/POST等)',
  path varchar(1024) NULL DEFAULT NULL COMMENT '请求路径',
  protocol varchar(16) NULL DEFAULT NULL COMMENT 'HTTP 协议版本 (HTTP/1.1等)',
  request_id varchar(64) NULL DEFAULT NULL COMMENT 'X-Request-ID 请求唯一标识',
  user_agent varchar(512) NULL DEFAULT NULL COMMENT 'User-Agent 客户端信息',
  x_forwarded_for varchar(256) NULL DEFAULT NULL COMMENT 'X-Forwarded-For 客户端真实IP',
  response_code int NULL DEFAULT NULL COMMENT '响应状态码 (200/404/500等)',
  response_flags varchar(64) NULL DEFAULT NULL COMMENT 'Envoy 响应标志',
  response_code_details varchar(256) NULL DEFAULT NULL COMMENT '响应码详情',
  bytes_received bigint NULL DEFAULT NULL COMMENT '接收字节数',
  bytes_sent bigint NULL DEFAULT NULL COMMENT '发送字节数',
  duration int NULL DEFAULT NULL COMMENT '请求总耗时(ms)',
  upstream_cluster varchar(256) NULL DEFAULT NULL COMMENT '上游集群名',
  upstream_host varchar(256) NULL DEFAULT NULL COMMENT '上游主机地址',
  upstream_service_time varchar(32) NULL DEFAULT NULL COMMENT '上游服务耗时',
  upstream_transport_failure_reason varchar(256) NULL DEFAULT NULL COMMENT '上游传输失败原因',
  upstream_local_address varchar(64) NULL DEFAULT NULL COMMENT '上游本地地址',
  downstream_local_address varchar(64) NULL DEFAULT NULL COMMENT '下游本地地址',
  downstream_remote_address varchar(64) NULL DEFAULT NULL COMMENT '下游远程地址',
  route_name varchar(256) NULL DEFAULT NULL COMMENT '路由名称',
  requested_server_name varchar(256) NULL DEFAULT NULL COMMENT 'SNI 服务器名称',
  istio_policy_status varchar(64) NULL DEFAULT NULL COMMENT 'Istio 策略状态',
  ai_log json NULL DEFAULT NULL COMMENT 'WASM AI 日志 (JSON字符串)',
  instance_id varchar(128) NULL DEFAULT NULL COMMENT '实例ID（Pod名称或容器ID）',
  api varchar(128) NULL DEFAULT NULL COMMENT 'API名称（如 chat/completions）',
  model varchar(128) NULL DEFAULT NULL COMMENT '模型名称（如 qwen-max）',
  consumer varchar(256) NULL DEFAULT NULL COMMENT '消费者信息（用户名/API Key等）',
  route varchar(256) NULL DEFAULT NULL COMMENT '路由名称（冗余字段，便于查询）',
  service varchar(256) NULL DEFAULT NULL COMMENT '服务名称（上游服务）',
  mcp_server varchar(256) NULL DEFAULT NULL COMMENT 'MCP服务器名称',
  mcp_tool varchar(256) NULL DEFAULT NULL COMMENT 'MCP工具名称',
  input_tokens bigint NULL DEFAULT NULL COMMENT '输入token数量',
  output_tokens bigint NULL DEFAULT NULL COMMENT '输出token数量',
  total_tokens bigint NULL DEFAULT NULL COMMENT '总token数量',
  request_body mediumtext NULL DEFAULT NULL COMMENT '请求体（最大16MB）',
  response_body mediumtext NULL DEFAULT NULL COMMENT '响应体（最大16MB）',
  PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='HTTP 访问日志表';

CREATE INDEX idx_start_time ON access_logs (start_time DESC);
CREATE INDEX idx_trace_id ON access_logs (trace_id);
CREATE INDEX idx_authority_time ON access_logs (authority, start_time DESC);
CREATE INDEX idx_response_code_time ON access_logs (response_code, start_time DESC);
CREATE INDEX idx_path ON access_logs (path(255));
CREATE INDEX idx_method_authority ON access_logs (method, authority);
CREATE INDEX idx_duration ON access_logs (duration DESC);
CREATE INDEX idx_upstream_cluster ON access_logs (upstream_cluster, start_time DESC);
CREATE INDEX idx_route_name ON access_logs (route_name, start_time DESC);
CREATE INDEX idx_instance_id ON access_logs (instance_id, start_time DESC);
CREATE INDEX idx_api ON access_logs (api, start_time DESC);
CREATE INDEX idx_model ON access_logs (model, start_time DESC);
CREATE INDEX idx_consumer ON access_logs (consumer, start_time DESC);
CREATE INDEX idx_service ON access_logs (service, start_time DESC);
CREATE INDEX idx_mcp_server ON access_logs (mcp_server, start_time DESC);
CREATE INDEX idx_mcp_tool ON access_logs (mcp_tool, start_time DESC);
```

### 2. 部署 Log Collector 服务

#### 方式一：Kubernetes 部署（推荐）

将以下 YAML 保存为 `log-collector.yaml` 并应用：

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: log-collector
  namespace: higress-system
  labels:
    app: log-collector
spec:
  replicas: 1
  selector:
    matchLabels:
      app: log-collector
  template:
    metadata:
      labels:
        app: log-collector
    spec:
      containers:
      - name: collector
        image: opensource-registry.cn-hangzhou.cr.aliyuncs.com/higress-group/log-collector:latest
        imagePullPolicy: Always
        ports:
        - containerPort: 8080
        env:
        # 修改为你的 MySQL 连接信息
        - name: MYSQL_DSN
          value: "user:password@tcp(mysql-host:3306)/higress_poc?charset=utf8mb4&parseTime=True&loc=Local"
        resources:
          limits:
            cpu: "500m"
            memory: "512Mi"
          requests:
            cpu: "100m"
            memory: "128Mi"
        livenessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 10
          periodSeconds: 10
        readinessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 5
          periodSeconds: 5

---
apiVersion: v1
kind: Service
metadata:
  name: log-collector
  namespace: higress-system
spec:
  selector:
    app: log-collector
  ports:
  - port: 80
    targetPort: 8080
    protocol: TCP
  type: ClusterIP
```

应用部署：

```bash
kubectl apply -f log-collector.yaml
```

#### 方式二：Docker 单机部署

如果您想在本地或单台服务器上快速部署，可以使用 Docker 运行日志收集服务。

**部署命令：**

```bash
docker run -d \
  --name log-collector \
  -p 8080:8080 \
  -e MYSQL_DSN="user:password@tcp(mysql-host:3306)/higress_poc?charset=utf8mb4&parseTime=True&loc=Local" \
  --restart unless-stopped \
  opensource-registry.cn-hangzhou.cr.aliyuncs.com/higress-group/log-collector:latest
```

**参数说明：**
- `-d`: 后台运行容器
- `--name log-collector`: 指定容器名称
- `-p 8080:8080`: 将容器的 8080 端口映射到宿主机
- `-e MYSQL_DSN`: 设置 MySQL 数据库连接字符串，请根据实际情况修改
- `--restart unless-stopped`: 容器退出时自动重启（除非手动停止）

**验证部署：**

检查容器运行状态：
```bash
docker ps | grep log-collector
```

查看容器日志：
```bash
docker logs -f log-collector
```

测试健康检查端点：
```bash
curl http://localhost:8080/health
```

**停止和删除容器：**

```bash
# 停止容器
docker stop log-collector

# 删除容器
docker rm log-collector
```

### 3. 验证部署

#### Kubernetes 部署验证

检查 Pod 状态：

```bash
kubectl get pods -n higress-system -l app=log-collector
```

查看日志确认服务启动正常：

```bash
kubectl logs -n higress-system deployment/log-collector
```

测试健康检查端点：

```bash
kubectl exec -n higress-system deployment/log-collector -- wget -qO- http://localhost:8080/health
```

#### Docker 部署验证

检查容器运行状态：
```bash
docker ps | grep log-collector
```

查看容器日志：
```bash
docker logs -f log-collector
```

测试健康检查端点：
```bash
curl http://localhost:8080/health
```

正常响应应该返回类似：`ok` 的响应。

### 4. 自定义 Log Collector（可选）

如需自定义功能，可参考以下源码结构进行修改和重新构建：

**源码结构：**
```
db-log-pusher/
├── main.go                      # Pusher 插件主程序
└── log-collector/               # Collector 服务端
    ├── main.go                  # Collector 主程序
    ├── Dockerfile               # Docker 镜像构建文件
    └── ...                      # 其他依赖文件
```

**主要功能：**
- 提供 `/ingest` 端点接收日志（POST）
- 提供 `/query` 端点查询日志（GET）
- 提供 `/health` 端点健康检查
- 批量写入数据库（默认每 50 条或每秒刷新一次）
- 支持丰富的查询参数（时间范围、实例 ID、API、模型、MCP Server 等）

**构建镜像：**

方式一：GitHub Actions 自动构建（推荐）

推送对应标签即可触发自动构建，并同时更新版本镜像和 `latest` 镜像：

```bash
# 构建 db-log-pusher 插件 OCI 镜像
git tag v1.2.3
git push origin v1.2.3

# 构建 log-collector 服务镜像
git tag collector-v1.2.3
git push origin collector-v1.2.3
```

镜像将自动推送到：
- `oci://opensource-registry.cn-hangzhou.cr.aliyuncs.com/plugins/db-log-pusher:1.2.3`
- `oci://opensource-registry.cn-hangzhou.cr.aliyuncs.com/plugins/db-log-pusher:latest`
- `opensource-registry.cn-hangzhou.cr.aliyuncs.com/higress-group/log-collector:1.2.3`
- `opensource-registry.cn-hangzhou.cr.aliyuncs.com/higress-group/log-collector:latest`

方式二：本地手动构建

```bash
cd log-collector
docker build -t your-registry/log-collector:latest .
```

### 5. 注意事项

1. **性能考虑**: 默认的 log-collector 是单实例部署，适用于中小流量场景。对于高并发场景，建议：
   - 增加 replicas 数量
   - 使用消息队列（如 Kafka）作为缓冲
   - 采用专业的日志系统（如 Elasticsearch + Logstash）

2. **数据安全**: 
   - 建议使用独立的数据库账号，限制权限
   - 生产环境应使用 TLS 加密数据库连接
   - 定期备份日志数据

3. **资源限制**: 根据实际流量调整容器的 CPU 和内存限制

4. **监控告警**: 建议为 log-collector 添加监控指标，如：
   - HTTP 请求成功率
   - 数据库写入延迟
   - Buffer 队列长度

## 使用注意事项

### 插件执行顺序
如果需要读取 `ai-statistics` 插件写入的 AI 日志，请确保：
1. 在 WasmPlugin 资源中，`db-log-pusher` 的 phase 应早于 `ai-statistics`，例如使用 AUTHN 阶段早于默认阶段
2. 或者在同一 phase 中，`db-log-pusher` 的 priority 应高于 `ai-statistics`（数字越大，请求阶段越先执行、响应阶段越后执行）
3. `db-log-pusher` 在响应阶段读取 AI 日志，而 HTTP 过滤器响应阶段会按请求过滤器链的反向顺序执行

### 性能考虑
- 插件采用异步方式发送日志，不会阻塞主请求流程
- 对于大请求体，插件会进行适当处理以避免内存问题
- 日志发送失败不会影响主业务流程

### 与其他插件的配合
- 与认证插件配合时，可以从认证信息中获取消费者信息
- 与路由插件配合时，可以获取更精确的路由和服务信息
- 与 MCP 服务配合时，可以获取工具调用相关信息

## 故障排除

### 日志未发送
1. 检查收集器服务是否正常运行
2. 确认网络连通性
3. 查看 Higress 网关日志中的错误信息

### 配置验证
- 确保 `collector_service_name` 和 `collector_port` 配置正确
- 验证收集器服务能够接收 JSON 格式的日志数据

## 高级配置

对于更复杂的部署场景，您可以根据需要调整以下参数：
- `collector_path`: 根据您的日志收集服务 API 路径进行调整
- 配合其他监控工具进行日志格式化和处理

## HiMarket集成

> 更多详细信息请参考官方文档：[HiMarket db-log-pusher 插件文档](https://higress.cn/docs/himarket/himarket-db-log-pusher/)
