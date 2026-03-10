# 数据库日志推送插件

该插件收集HTTP请求日志并将其推送到数据库日志收集服务。

## 概述

数据库日志推送插件捕获HTTP请求/响应详细信息，并将其推送到指定的收集服务进行存储和分析。它支持监控元数据字段和令牌使用统计信息。

## 功能

- 捕获全面的HTTP请求/响应信息
- 支持监控元数据字段（实例ID、API、模型、消费者等）
- 包含令牌使用统计信息（输入、输出和总令牌数）
- 异步将日志推送到收集服务
- 可配置的收集服务端点

## 配置

该插件接受以下配置参数：

```yaml
config:
  collector_service_name: "db-log-collector"  # 收集服务的名称
  collector_port: 80                        # 收集服务的端口  
  collector_path: "/ingest"                 # 发送日志的API路径
```

## 使用方法

使用WasmPlugin CRD将插件应用到您的Higress网关，如示例部署文件所示。

注意：此插件作为“推送器”将日志发送到收集服务。收集服务接收并存储日志。

## 插件开发

1. **克隆仓库**
   ```bash
   git clone https://github.com/your-repo/db-log-pusher-plugin.git
   cd db-log-pusher-plugin
   mv ./ ~/work/higress/plugins/wasm-go/extensions/db-log-pusher/
   cd ~/work/higress/plugins/wasm-go
   ```
2. **安装依赖**
   ```bash
   go mod download
   ```
3. **编译插件**
   ```bash
   PLUGIN_NAME=db-log-pusher make build
   ```
4. **测试插件**
   ```bash
   PLUGIN_NAME=db-log-pusher make test
   ```

## 插件打包

1. **构建 Docker 镜像**
   ```bash
   make docker-build
   ```
2. **推送 Docker 镜像**
   ```bash
   make docker-push
   ```

## 插件发布

1. **更新版本号**
   修改 `VERSION` 文件中的版本号。
2. **创建发布分支**
   ```bash
   git checkout -b release/vX.X.X
   ```
3. **提交更改**
   ```bash
   git add VERSION
   git commit -m "Release vX.X.X"
   git push origin release/vX.X.X
   ```
4. **创建发布标签**
   ```bash
   git tag vX.X.X
   git push origin vX.X.X
   ```