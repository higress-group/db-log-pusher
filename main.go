package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/higress-group/proxy-wasm-go-sdk/properties"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/log"
	"github.com/higress-group/wasm-go/pkg/tokenusage"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/tidwall/gjson"
)

func main() {}

func init() {
	wrapper.SetCtx(
		"db-log",
		wrapper.ParseConfig(parseConfig),
		wrapper.ProcessRequestHeaders(onHttpRequestHeaders),
		wrapper.ProcessRequestBody(onHttpRequestBody),
		wrapper.ProcessResponseHeaders(onHttpResponseHeaders),
		wrapper.ProcessStreamingResponseBody(onHttpStreamingResponseBody),
		wrapper.ProcessResponseBody(onHttpResponseBody),
		wrapper.ProcessStreamDone(onHttpStreamDone),
		wrapper.WithRebuildAfterRequests[PluginConfig](1000),
		wrapper.WithRebuildMaxMemBytes[PluginConfig](200*1024*1024),
	)
}

// context key 常量
const (
	ctxStreamingBodyBuffer = "streaming_body_buffer" // SSE 累积缓冲区 (*bytes.Buffer)
	ctxStreamingBytesSent  = "streaming_bytes_sent"  // 流式响应累加字节数 (int64)
	ctxFirstByteTime       = "first_byte_time_ms"    // 首字节到达时间戳 (int64, UnixMilli)
	ctxLogSent             = "log_sent"              // 日志发送幂等标记 (bool)
	ctxIsStreaming         = "is_streaming"          // 是否为流式响应 (bool)
)

// PluginConfig 定义插件配置 (对应 WasmPlugin 资源中的 pluginConfig)
type PluginConfig struct {
	CollectorServiceName string             `json:"collector_service_name"` // Collector 完整 FQDN，如 "log-collector.dns"
	CollectorPort       int64              `json:"collector_port"`    // Collector 端口，例如 80
	CollectorPath       string             `json:"collector_path"`    // 接收日志的 API 路径，例如 "/ingest"
	InstanceID          string             `json:"instance_id"`       // 网关实例 ID（如 "i-xxx"），由管控面配置传入
	CollectorClient     wrapper.HttpClient `json:"-"`                 // HTTP 客户端，用于发送日志
}

// LogEntry 定义发给 Collector 的 JSON 数据结构 (参考 Envoy accessLogFormat)
type LogEntry struct {
	// 基础请求信息
	StartTime     string `json:"start_time"`               // 请求开始时间
	Authority     string `json:"authority"`                // Host/Authority
	Method        string `json:"method"`                   // HTTP 方法
	Path          string `json:"path"`                     // 请求路径
	Protocol      string `json:"protocol"`                 // HTTP 协议版本
	RequestID     string `json:"request_id"`               // X-Request-ID
	TraceID       string `json:"trace_id,omitempty"`       // X-B3-TraceID
	UserAgent     string `json:"user_agent,omitempty"`     // User-Agent
	XForwardedFor string `json:"x_forwarded_for,omitempty"` // X-Forwarded-For
	
	// 响应信息
	ResponseCode        int    `json:"response_code"`                  // 响应状态码
	ResponseFlags       string `json:"response_flags,omitempty"`       // Envoy 响应标志
	ResponseCodeDetails string `json:"response_code_details,omitempty"` // 响应码详情
	
	// 流量信息
	BytesReceived int64 `json:"bytes_received"`            // 接收字节数
	BytesSent     int64 `json:"bytes_sent"`                // 发送字节数
	Duration      int64 `json:"duration"`                  // 请求总耗时(ms)
	FirstByteTime int64 `json:"first_byte_time,omitempty"` // 首字节响应耗时 TTFB (ms)，仅流式响应有效

	// 上游信息
	UpstreamCluster              string `json:"upstream_cluster,omitempty"`                // 上游集群名
	UpstreamHost                 string `json:"upstream_host,omitempty"`                   // 上游主机
	UpstreamServiceTime          string `json:"upstream_service_time,omitempty"`           // 上游服务耗时
	UpstreamTransportFailure     string `json:"upstream_transport_failure_reason,omitempty"` // 上游传输失败原因
	
	// 连接信息
	DownstreamLocalAddress  string `json:"downstream_local_address,omitempty"`  // 下游本地地址
	DownstreamRemoteAddress string `json:"downstream_remote_address,omitempty"` // 下游远程地址
	UpstreamLocalAddress    string `json:"upstream_local_address,omitempty"`    // 上游本地地址
	
	// 路由信息
	RouteName            string `json:"route_name,omitempty"`             // 路由名称
	RequestedServerName  string `json:"requested_server_name,omitempty"`  // SNI
	
	// AI 日志 (如果有)
	AILog json.RawMessage `json:"ai_log"` // WASM AI 日志，移除了 omitempty 以确保始终输出
	
	// 监控元数据字段
	InstanceID string `json:"instance_id"`      // 实例ID
	API        string `json:"api"`              // API名称
	Model      string `json:"model"`            // 模型名称
	Consumer   string `json:"consumer"`         // 消费者
	Route      string `json:"route"`            // 路由
	Service    string `json:"service"`          // 服务
	MCPServer  string `json:"mcp_server"`       // MCP Server
	MCPTool    string `json:"mcp_tool"`         // MCP Tool
	
	// Token 统计信息
	InputTokens  int64 `json:"input_tokens,omitempty"`   // 输入token数量
	OutputTokens int64 `json:"output_tokens,omitempty"`  // 输出token数量
	TotalTokens  int64 `json:"total_tokens,omitempty"`   // 总token数量
	
	// 详细数据 (可选)
	ReqHeaders  map[string]string `json:"req_headers,omitempty"`  // 完整请求头
	ReqBody     string            `json:"req_body,omitempty"`     // 请求体
	RespHeaders map[string]string `json:"resp_headers,omitempty"` // 完整响应头
	RespBody    string            `json:"resp_body,omitempty"`    // 响应体
}

// 解析配置
func parseConfig(jsonConf gjson.Result, config *PluginConfig) error {
	log.Debugf("[db-log-pusher] parsing config: %s", jsonConf.String())
	
	config.CollectorServiceName = jsonConf.Get("collector_service_name").String()
	config.CollectorPort = jsonConf.Get("collector_port").Int()
	
	// 校验必填参数
	if config.CollectorServiceName == "" || config.CollectorPort == 0 {
		log.Errorf("[db-log-pusher] collector_service_name and collector_port are required")
		return errors.New("collector_service_name and collector_port are required")
	}
	
	config.CollectorPath = jsonConf.Get("collector_path").String()
	if config.CollectorPath == "" {
		config.CollectorPath = "/"
	}

	// 从配置读取网关实例 ID（由管控面传入）
	config.InstanceID = jsonConf.Get("instance_id").String()
	
	// 创建 HTTP 客户端用于发送日志
	// 使用 FQDNCluster，服务名格式为 Higress 内部服务名（如 "log-collector.dns"）
	log.Debugf("[db-log-pusher] creating cluster client: service=%s, port=%d", config.CollectorServiceName, config.CollectorPort)
	config.CollectorClient = wrapper.NewClusterClient(wrapper.FQDNCluster{
		FQDN: config.CollectorServiceName,
		Port: config.CollectorPort,
	})
	
	return nil
}


// ---------------- 核心逻辑 ----------------

// 1. 处理请求头
func onHttpRequestHeaders(ctx wrapper.HttpContext, config PluginConfig) types.Action {
	// 获取所有请求头并暂存
	headers, err := proxywasm.GetHttpRequestHeaders()
	if err != nil {
		log.Errorf("[db-log-pusher] failed to get request headers: %v", err)
	}
	ctx.SetContext("req_headers", headers)
	ctx.SetContext("start_time", time.Now().UnixMilli())

	// 必须允许继续,否则请求会卡住
	// 如果需要读取 Body,必须在 return 时不打断流
	return types.ActionContinue
}

// 2. 处理请求体
func onHttpRequestBody(ctx wrapper.HttpContext, config PluginConfig, body []byte) types.Action {
	if len(body) > 0 {
		// 注意:大包体可能会分多次回调,生产环境建议限制长度或做截断
		ctx.SetContext("req_body", string(body))
	}
	return types.ActionContinue
}

// 3. 处理响应头
func onHttpResponseHeaders(ctx wrapper.HttpContext, config PluginConfig) types.Action {
	headers, _ := proxywasm.GetHttpResponseHeaders()
	ctx.SetContext("resp_headers", headers)

	contentType, _ := proxywasm.GetHttpResponseHeader("content-type")
	transferEncoding, _ := proxywasm.GetHttpResponseHeader("transfer-encoding")

	// 识别静态资源 / 二进制内容，完全跳过 body 缓冲
	if isStaticOrBinaryResponse(contentType) {
		log.Debugf("[db-log-pusher] skipping body for static/binary response (content-type=%s)", contentType)
		ctx.DontReadResponseBody()
		return types.ActionContinue
	}

	// 流式响应检测：SSE / NDJSON / JSON-Seq 等
	if isStreamingResponse(contentType, transferEncoding) {
		ctx.SetContext(ctxIsStreaming, true)
		log.Debugf("[db-log-pusher] detected streaming response (content-type=%s, transfer-encoding=%s), using streaming mode",
			contentType, transferEncoding)
	} else {
		// 非流式：回退到完整缓冲模式，由 onHttpResponseBody 处理
		ctx.BufferResponseBody()
	}

	return types.ActionContinue
}

// isStaticOrBinaryResponse 判断是否为静态资源 / 二进制内容
func isStaticOrBinaryResponse(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if ct == "" {
		return false
	}
	// 图片 / 字体 / 音视频 / wasm / 通用二进制
	if strings.HasPrefix(ct, "image/") ||
		strings.HasPrefix(ct, "font/") ||
		strings.HasPrefix(ct, "video/") ||
		strings.HasPrefix(ct, "audio/") ||
		strings.Contains(ct, "octet-stream") ||
		strings.Contains(ct, "wasm") {
		return true
	}
	// 前端静态资源：CSS / JS / 字体相关
	if strings.Contains(ct, "text/css") ||
		strings.Contains(ct, "text/javascript") ||
		strings.Contains(ct, "application/javascript") ||
		strings.Contains(ct, "application/x-javascript") ||
		strings.Contains(ct, "application/ecmascript") ||
		strings.Contains(ct, "font") {
		return true
	}
	// PDF / zip / 压缩包等二进制文档
	if strings.Contains(ct, "application/pdf") ||
		strings.Contains(ct, "application/zip") ||
		strings.Contains(ct, "application/x-tar") ||
		strings.Contains(ct, "application/gzip") ||
		strings.Contains(ct, "application/x-7z-compressed") {
		return true
	}
	return false
}

// isStreamingResponse 判断响应是否为流式
func isStreamingResponse(contentType, transferEncoding string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	te := strings.ToLower(strings.TrimSpace(transferEncoding))

	// 经典 SSE
	if strings.Contains(ct, "text/event-stream") {
		return true
	}
	// NDJSON / JSONL / JSON-Seq 等流式 JSON 协议
	if strings.Contains(ct, "application/x-ndjson") ||
		strings.Contains(ct, "application/stream+json") ||
		strings.Contains(ct, "application/jsonl") ||
		strings.Contains(ct, "application/json-seq") {
		return true
	}
	// chunked + JSON 流（某些 LLM 上游不带正式 SSE 头但实际是 chunked 流式）
	if strings.Contains(te, "chunked") && strings.HasPrefix(ct, "application/json") {
		// 注意：普通短 JSON 响应也可能是 chunked，这里偏保守仅在确实需要时启用
		// 默认不返回 true 避免误判，留作占位说明
		_ = te
	}
	return false
}

// 4. 处理响应体 (也是发送日志的最佳时机)
// 重要提示：如果需要读取 ai-statistics 写入的 AI 日志，db-log-pusher
// 需要在请求过滤器链中位于 ai-statistics 之前，这样响应阶段才会晚于
// ai-statistics 执行。不同 phase 下建议使用 AUTHN；同 phase 下 priority
// 应高于 ai-statistics。
func onHttpResponseBody(ctx wrapper.HttpContext, config PluginConfig, body []byte) types.Action {
	sendLog(ctx, config, body)
	return types.ActionContinue
}

// 5. 处理 SSE 流式响应体 (每个 chunk 即时回调，必须立即返回 chunk 转发给客户端)
// 此回调用于规避 wrapper 中"未注册流式回调时遇到 endOfStream=false 即 return ActionPause"的阻塞行为。
// 累积的 chunk 仅用于在流结束时记录完整响应体到日志，不影响实际响应转发。
func onHttpStreamingResponseBody(ctx wrapper.HttpContext, config PluginConfig, chunk []byte, endOfStream bool) []byte {

	// 这里所有错误均被吞掉，仅记录日志，不打断响应转发
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("[db-log-pusher] onHttpStreamingResponseBody panic recovered: %v", r)
		}
	}()

	// 首字节时间 (TTFB) - 第一次收到非空 chunk 时记录
	if len(chunk) > 0 {
		if _, ok := ctx.GetContext(ctxFirstByteTime).(int64); !ok {
			ctx.SetContext(ctxFirstByteTime, time.Now().UnixMilli())
		}
	}

	// 使用 *bytes.Buffer 替代 []byte append，减少频繁内存重分配
	// 类型断言失败也能安全恢复
	buf, ok := ctx.GetContext(ctxStreamingBodyBuffer).(*bytes.Buffer)
	if !ok || buf == nil {
		buf = &bytes.Buffer{}
		ctx.SetContext(ctxStreamingBodyBuffer, buf)
	}
	if len(chunk) > 0 {
		buf.Write(chunk)
	}

	// 累加 bytes_sent —— SSE 场景下 response.total_size / Content-Length 均不可靠
	bytesSent, _ := ctx.GetContext(ctxStreamingBytesSent).(int64)
	bytesSent += int64(len(chunk))
	ctx.SetContext(ctxStreamingBytesSent, bytesSent)

	// 流结束时，从缓冲区取出完整响应体并发送日志
	if endOfStream {
		log.Debugf("[db-log-pusher] SSE stream ended, total buffered body size=%d, bytes_sent=%d",
			buf.Len(), bytesSent)
		sendLog(ctx, config, buf.Bytes())
	}

	return chunk
}

// onHttpStreamDone Envoy 关闭 HTTP 流时的兜底回调（包括客户端断开等异常路径）
func onHttpStreamDone(ctx wrapper.HttpContext, config PluginConfig) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("[db-log-pusher] onHttpStreamDone panic recovered: %v", r)
		}
	}()

	// 幂等：已在正常路径发送过日志则跳过
	if sent, _ := ctx.GetContext(ctxLogSent).(bool); sent {
		return
	}

	// 仅对流式响应做兜底；非流式由 onHttpResponseBody 处理
	if streaming, _ := ctx.GetContext(ctxIsStreaming).(bool); !streaming {
		return
	}

	var body []byte
	if buf, ok := ctx.GetContext(ctxStreamingBodyBuffer).(*bytes.Buffer); ok && buf != nil {
		body = buf.Bytes()
	}
	log.Debugf("[db-log-pusher] stream done fallback triggered, buffered body size=%d", len(body))
	sendLog(ctx, config, body)
}

// sendLog 组装 LogEntry 并异步推送给 Collector
func sendLog(ctx wrapper.HttpContext, config PluginConfig, body []byte) {
	// 幂等标记：防止流式正常结束 + 兜底回调重复发送
	if sent, _ := ctx.GetContext(ctxLogSent).(bool); sent {
		log.Debugf("[db-log-pusher] sendLog skipped (already sent)")
		return
	}
	ctx.SetContext(ctxLogSent, true)

	// 1. 组装数据 - 参考 Envoy accessLogFormat 字段
	reqHeaders, _ := ctx.GetContext("req_headers").([][2]string)
	reqBody, _ := ctx.GetContext("req_body").(string)
	respHeaders, _ := ctx.GetContext("resp_headers").([][2]string)
	startTime, _ := ctx.GetContext("start_time").(int64)
	
	// 提取响应状态码
	statusCode := 200
	for _, h := range respHeaders {
		if h[0] == ":status" {
			if code, err := parseStatusCode(h[1]); err == nil {
				statusCode = code
			}
			break
		}
	}
	
	// 提取关键请求头
	requestID := getHeaderValue(reqHeaders, "x-request-id")
	traceID := getHeaderValue(reqHeaders, "x-b3-traceid")
	userAgent := getHeaderValue(reqHeaders, "user-agent")
	xForwardedFor := getHeaderValue(reqHeaders, "x-forwarded-for")
	
	// 获取 Envoy 属性
	protocol := getEnvoyProperty("request.protocol", "HTTP/1.1")
	bytesReceived := getEnvoyPropertyInt64("request.total_size", 0)

	// bytes_sent 优先使用流式累加值（SSE 场景下 response.total_size 不可靠）
	var bytesSent int64
	if streamedBytes, ok := ctx.GetContext(ctxStreamingBytesSent).(int64); ok && streamedBytes > 0 {
		bytesSent = streamedBytes
		log.Debugf("[db-log-pusher] bytes_sent from streaming accumulator: %d", bytesSent)
	} else {
		bytesSent = getResponseTotalSize()
	}

	responseFlags := getResponseFlags()
	responseCodeDetails := getEnvoyProperty("response.code_details", "")
	upstreamCluster := getEnvoyProperty("cluster_name", "")
	upstreamHost := getEnvoyProperty("upstream_host", "")
	upstreamServiceTime := getEnvoyProperty("upstream_service_time", "")
	downstreamLocalAddr := getEnvoyProperty("downstream_local_address", "")
	downstreamRemoteAddr := getEnvoyProperty("downstream_remote_address", "")
	upstreamLocalAddr := getEnvoyProperty("upstream_local_address", "")
	sni := getEnvoyProperty("requested_server_name", "")
	
	// 提取监控所需的元数据字段
	instanceID := getInstanceID(config)
	apiName := getAPIName(ctx)
	modelName := getModelName(ctx)
	consumer := getConsumer()
	routeNameMeta := getRouteName()
	serviceName := getServiceName()
	mcpServer := getMCPServer()
	mcpTool := getMCPTool(ctx)
	
	// 🔍 非流式响应 Token 获取逻辑
	var inputTokens, outputTokens, totalTokens int64 = 0, 0, 0
	if len(body) > 0 {
		// 使用 tokenusage 包从响应体中提取 token 信息
		if usage := tokenusage.GetTokenUsage(ctx, body); usage.TotalToken > 0 {
			inputTokens = usage.InputToken
			outputTokens = usage.OutputToken
			totalTokens = usage.TotalToken
			log.Debugf("[db-log-pusher] extracted tokens from response body: input=%d, output=%d, total=%d", 
				inputTokens, outputTokens, totalTokens)
		} else {
			log.Debugf("[db-log-pusher] no token usage found in response body")
		}
	}
	
	// 计算耗时
	duration := time.Now().UnixMilli() - startTime

	// #7 计算首字节响应耗时 TTFB（仅流式场景）
	var firstByteTime int64
	if ttfbTs, ok := ctx.GetContext(ctxFirstByteTime).(int64); ok && ttfbTs > 0 && startTime > 0 {
		firstByteTime = ttfbTs - startTime
		if firstByteTime < 0 {
			firstByteTime = 0
		}
	}
	// ⚠️ 先不读取 AI 日志，等到最后再读取
	// 因为 ai-statistics 在 onHttpResponseBody 的最后才写入
	
	entry := LogEntry{
		// 基础信息
		StartTime:     time.UnixMilli(startTime).Format(time.RFC3339),
		Authority:     ctx.Host(),
		Method:        ctx.Method(),
		Path:          ctx.Path(),
		Protocol:      protocol,
		RequestID:     requestID,
		TraceID:       traceID,
		UserAgent:     userAgent,
		XForwardedFor: xForwardedFor,
		
		// 响应信息
		ResponseCode:        statusCode,
		ResponseFlags:       responseFlags,
		ResponseCodeDetails: responseCodeDetails,
		
		// 流量信息
		BytesReceived: bytesReceived,
		BytesSent:     bytesSent,
		Duration:      duration,
		FirstByteTime: firstByteTime,

		// 上游信息
		UpstreamCluster:          upstreamCluster,
		UpstreamHost:             upstreamHost,
		UpstreamServiceTime:      upstreamServiceTime,
		UpstreamTransportFailure: getEnvoyProperty("upstream_transport_failure_reason", ""),
		
		// 连接信息
		DownstreamLocalAddress:  downstreamLocalAddr,
		DownstreamRemoteAddress: downstreamRemoteAddr,
		UpstreamLocalAddress:    upstreamLocalAddr,
		
		// 路由信息
		RouteName:           routeNameMeta,
		RequestedServerName: sni,
		
		// AI 日志 - 暂时留空，稍后填充
		AILog: nil,
		
		// 监控元数据
		InstanceID: instanceID,
		API:        apiName,
		Model:      modelName,
		Consumer:   consumer,
		Route:      routeNameMeta,
		Service:    serviceName,
		MCPServer:  mcpServer,
		MCPTool:    mcpTool,
		
		// Token 统计信息
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalTokens:  totalTokens,
		
		// 详细数据 (可选，根据需要采集)
		ReqHeaders:  toMap(reqHeaders),
		ReqBody:     reqBody,
		RespHeaders: toMap(respHeaders),
		RespBody:    string(body),
	}

	// 🔍 调试日志：打印即将存储的所有字段内容
	log.Debugf("[db-log-pusher] === 即将存储的日志内容 ===")
	log.Debugf("[db-log-pusher] 监控元数据: InstanceID=%s, API=%s, Model=%s, Consumer=%s", 
		entry.InstanceID, entry.API, entry.Model, entry.Consumer)
	log.Debugf("[db-log-pusher] 路由服务: Route=%s, Service=%s, MCPServer=%s, MCPTool=%s", 
		entry.Route, entry.Service, entry.MCPServer, entry.MCPTool)
	log.Debugf("[db-log-pusher] Token统计: InputTokens=%d, OutputTokens=%d, TotalTokens=%d", 
		entry.InputTokens, entry.OutputTokens, entry.TotalTokens)
	log.Debugf("[db-log-pusher] =========================")

	aiLogBytes, err := proxywasm.GetProperty([]string{wrapper.AILogKey})
	if err == nil && len(aiLogBytes) > 0 {
		rawStr := string(aiLogBytes)
		
		// 1. 如果开头有双引号，说明是被包裹的字符串，需要先处理掉包裹
		if strings.HasPrefix(rawStr, "\"") && strings.HasSuffix(rawStr, "\"") {
			if unquoted, err := strconv.Unquote(rawStr); err == nil {
				log.Debugf("[db-log-pusher] quoted AI log: %s", rawStr)
				rawStr = unquoted
				log.Debugf("[db-log-pusher] unquoted AI log: %s", rawStr)
			}
		}

		// 2. 处理可能被转义的JSON字符串 (例如 "{\"api\":\"qwen3-plus\",\"model\":\"qwen3-max\",...}")
		// 检查是否是以反斜杠转义的JSON字符串
		if strings.Contains(rawStr, "\\\"") {
			// 尝试Unescape字符串
			unescapedStr := strings.ReplaceAll(rawStr, "\\\"", "\"")
			unescapedStr = strings.ReplaceAll(unescapedStr, "\\\\", "\\")
			
			// 如果unescaped后是有效的JSON，则使用它
			if json.Valid([]byte(unescapedStr)) {
				rawStr = unescapedStr
			}
		}

		// 3. 尝试直接作为 RawMessage。如果是有效的 JSON 内容，Marshal 时会自动处理
		if json.Valid([]byte(rawStr)) {
			entry.AILog = json.RawMessage(rawStr)
			log.Debugf("[db-log-pusher] ✅ Successfully parsed AI log")
		} else {
			log.Warnf("[db-log-pusher] AI log is still invalid: %s", rawStr)
			entry.AILog = json.RawMessage(`{}`)
		}
	}


	// 2. 发送异步请求给 Collector
	// 由于 AILog 现在是 json.RawMessage 类型，序列化时会保持原始JSON格式
	log.Debugf("[db-log-pusher] about to marshal entry, AILog type: %T, AILog content: %s", entry.AILog, string(entry.AILog))
	
	payload, err := json.Marshal(entry)
	if err != nil {
		log.Errorf("[db-log-pusher] failed to marshal log entry: %v", err)
		return
	}
	
	log.Debugf("[db-log-pusher] marshaled payload length: %d, payload: %s", len(payload), string(payload))
	
	// 检查 payload 是否为空
	if len(payload) == 0 {
		log.Errorf("[db-log-pusher] marshaled payload is empty!")
		return
	}
	
	// 使用 wrapper.HttpClient.Post 方法，它会自动处理 headers
	headers := [][2]string{
		{"Content-Type", "application/json"},
	}

	// 获取最终使用的集群名
	clusterName := config.CollectorClient.ClusterName()
	
	log.Debugf("[db-log-pusher] sending log: cluster=%s, path=%s, payload_size=%d, payload=%s",
		clusterName, config.CollectorPath, len(payload), string(payload))

	// 这里的 5000 是超时时间(ms)
	// Fire-and-forget: 回调函数简单记录结果
	postErr := config.CollectorClient.Post(
		config.CollectorPath,
		headers,
		payload,
		func(statusCode int, responseHeaders http.Header, responseBody []byte) {
			if statusCode == 200 || statusCode == 204 {
				log.Debugf("[db-log-pusher] log sent successfully, status=%d", statusCode)
			} else {
				log.Warnf("[db-log-pusher] collector returned status=%d, body=%s", statusCode, string(responseBody))
			}
		},
		5000, // 超时 5 秒
	)
	if postErr != nil {
		log.Errorf("[db-log-pusher] failed to dispatch http call: %v", postErr)
	}

	return
}

// 辅助工具：Header 数组转 Map
func toMap(headers [][2]string) map[string]string {
	m := make(map[string]string)
	for _, h := range headers {
		m[h[0]] = h[1]
	}
	return m
}

// 从 Header 数组中获取指定 key 的值 (不区分大小写)
func getHeaderValue(headers [][2]string, key string) string {
	key = strings.ToLower(key)
	for _, h := range headers {
		if strings.ToLower(h[0]) == key {
			return h[1]
		}
	}
	return ""
}

// 解析状态码
func parseStatusCode(statusStr string) (int, error) {
	code, err := strconv.Atoi(statusStr)
	if err != nil {
		return 0, err
	}
	return code, nil
}

// 获取 Envoy 属性 (字符串类型)
func getEnvoyProperty(path string, defaultValue string) string {
	// Envoy 属性路径格式，参考: https://www.envoyproxy.io/docs/envoy/latest/intro/arch_overview/advanced/attributes
	var propertyPath []string
	
	switch path {
	case "request.protocol":
		propertyPath = []string{"request", "protocol"}
	case "response.flags":
		propertyPath = []string{"response", "flags"}
	case "response.code_details":
		propertyPath = []string{"response", "code_details"}
	case "cluster_name":
		propertyPath = []string{"cluster_name"}
	case "upstream_host":
		propertyPath = []string{"upstream", "address"}
	case "upstream_service_time":
		propertyPath = []string{"upstream", "service_time"}
	case "upstream_transport_failure_reason":
		propertyPath = []string{"upstream", "transport_failure_reason"}
	case "downstream_local_address":
		propertyPath = []string{"connection", "local_address"}
	case "downstream_remote_address":
		propertyPath = []string{"connection", "remote_address"}
	case "upstream_local_address":
		propertyPath = []string{"upstream", "local_address"}
	case "route_name":
		propertyPath = []string{"route_name"}
	case "requested_server_name":
		propertyPath = []string{"connection", "requested_server_name"}
	case "wasm.ai_log":
		propertyPath = []string{"wasm", "ai_log"}
	default:
		return defaultValue
	}
	
	value, err := proxywasm.GetProperty(propertyPath)
	if err != nil || len(value) == 0 {
		return defaultValue
	}
	return string(value)
}

// 获取实例ID
func getInstanceID(config PluginConfig) string {
	// 优先使用管控面配置的网关实例 ID
	if config.InstanceID != "" {
		return config.InstanceID
	}

	// Fallback: 从 Envoy 节点元数据获取 Pod 名称
	// Pod 名称格式通常是：higress-gateway-<hash>-<random>
	podNameBytes, err := proxywasm.GetProperty([]string{"node", "metadata", "POD_NAME"})
	if err == nil && len(podNameBytes) > 0 {
		podName := string(podNameBytes)
		if podName != "" {
			log.Debugf("[db-log-pusher] got instance_id from POD_NAME: %s", podName)
			return podName
		}
	}
	
	// 2. 从 Envoy 属性获取实例ID
	instanceID := getEnvoyProperty("instance_id", "")
	if instanceID != "" {
		log.Debugf("[db-log-pusher] got instance_id from envoy property: %s", instanceID)
		return instanceID
	}
	
	// 3. 从请求头获取
	instanceID, _ = proxywasm.GetHttpRequestHeader("x-instance-id")
	if instanceID != "" {
		log.Debugf("[db-log-pusher] got instance_id from header: %s", instanceID)
		return instanceID
	}
	
	// 4. 尝试从节点名称获取（作为备选方案）
	nodeNameBytes, err := proxywasm.GetProperty([]string{"node", "id"})
	if err == nil && len(nodeNameBytes) > 0 {
		nodeName := string(nodeNameBytes)
		if nodeName != "" {
			log.Debugf("[db-log-pusher] got instance_id from node.id: %s", nodeName)
			return nodeName
		}
	}
	
	log.Debugf("[db-log-pusher] instance_id not found, using default")
	return ""
}

// 获取API名称
func getAPIName(ctx wrapper.HttpContext) string {
	// 从路由名称解析
	routeName := getEnvoyProperty("route_name", "")
	if routeName != "" {
		// 格式: model-api-{api-name}-0
		parts := strings.Split(routeName, "-")
		if len(parts) >= 3 && parts[0] == "model" && parts[1] == "api" {
			// 提取从第3个部分开始的所有内容作为 API 名称
			// 例如: model-api-test-by-lisi-0 -> test-by-lisi
			apiName := strings.Join(parts[2:len(parts)-1], "-")
			return apiName
		}
	}
	
	log.Debugf("[db-log-pusher] api_name not determined from route/path")
	return ""
}

// 获取模型名称
func getModelName(ctx wrapper.HttpContext) string {
	// 优先从 ai-statistics 获取
	model := ctx.GetUserAttribute("model")
	if model != nil {
		if modelStr, ok := model.(string); ok && modelStr != "" {
			return modelStr
		}
	}
	
	// 从请求体解析
	reqBody, _ := ctx.GetContext("req_body").(string)
	if reqBody != "" {
		modelFromReq := extractModelFromRequestBody(reqBody)
		if modelFromReq != "" {
			return modelFromReq
		}
	}
	
	log.Debugf("[db-log-pusher] model_name not found")
	return ""
}

// 获取消费者信息
func getConsumer() string {
	// 优先从认证插件设置的头获取（jwt-auth/key-auth等插件认证通过后会设置此header）
	consumer, _ := proxywasm.GetHttpRequestHeader("x-mse-consumer")
	if consumer != "" {
		return consumer
	}
	return ""
}

// 获取路由名称 - 区分MCP场景和Model API场景
func getRouteName() string {
	routeName := getEnvoyProperty("route_name", "")
	if routeName != "" {
		return routeName
	}
	return "-"
}

// 获取服务名称
func getServiceName() string {
	// 从上游集群获取
	clusterName := getEnvoyProperty("cluster_name", "")
	if clusterName != "" {
		// 清理集群名称格式
		// service := strings.TrimPrefix(clusterName, "outbound|")
		// service = strings.TrimPrefix(service, "inbound|")
		// parts := strings.Split(service, "|")
		// if len(parts) > 0 {
		// 	return parts[len(parts)-1] // 取最后一部分作为服务名
		// }
		return clusterName
	}
	
	return ""
}

// 解析 response flags 为可读字符串
func parseResponseFlags(flags uint64) string {
    var flagStrings []string
    
    //定各种标志位的含义
    flagMap := map[uint64]string{
        0x1:    "UH",     // No healthy upstream hosts
        0x2:    "UF",     // Upstream connection failure
        0x4:    "NR",     // No route found
        0x8:    "URX",    // Upstream retry limit exceeded
        0x10:   "DC",     // Downstream connection termination
        0x20:   "LH",     // Failed local health check
        0x40:   "UT",     // Upstream request timeout
        0x80:   "LR",     // Local reset
        0x100:  "UR",     // Upstream remote reset
        0x200:  "UC",     // Upstream connection termination
        0x400:  "DI",     // Delay injected
        0x800:  "FI",     // Fault injected
        0x1000: "RL",     // Rate limited
        0x2000: "UAEX",   // Unauthorized external service
        0x4000: "RLSE",   // Rate limit service error
        0x8000: "IH",     // Invalid Envoy request headers
        0x10000: "SI",    // Stream idle timeout
        0x20000: "DPE",   // Downstream protocol error
        0x40000: "UPE",   // Upstream protocol error
        0x80000: "UMSDR", // Upstream max stream duration reached
    }
    
    //检查每个标志位
    for bit, flagStr := range flagMap {
        if flags&bit != 0 {
            flagStrings = append(flagStrings, flagStr)
        }
    }
    
    if len(flagStrings) == 0 {
        return "-"
    }
    
    return strings.Join(flagStrings, ",")
}

// 使用专门的函数获取 response flags
func getResponseFlags() string {
    flags, err := properties.GetResponseFlags()
    if err != nil {
		// TODO: 这里为啥error了？
        return ""
    }
    return parseResponseFlags(flags)
}

// 获取MCP Server - 准确实现版本
func getMCPServer() string {
    // 从MCP协议相关的头部获取（最准确的方式）
    // 检查MCP会话相关的头部信息
    mcpSessionId, err := proxywasm.GetHttpRequestHeader("mcp-session-id")
    if err == nil && mcpSessionId != "" {
        // 如果存在MCP会话ID，尝试从中解析MCP Server信息
        log.Debugf("[db-log-pusher] got mcp_session_id: %s", mcpSessionId)
    }
    
    // 从MCP协议版本头部获取
    mcpProtocolVersion, err := proxywasm.GetHttpRequestHeader("mcp-protocol-version")
    if err == nil && mcpProtocolVersion != "" {
        // 如果是MCP协议请求，从已设置的属性中获取MCP服务器名称
        // 在MCP服务器处理代码中，已经通过 SetProperty 设置了 mcp_server_name
        mcpServerName, err := proxywasm.GetProperty([]string{"mcp_server_name"})
        if err == nil && mcpServerName != nil && len(mcpServerName) > 0 {
            log.Debugf("[db-log-pusher] got mcp_server from property: %s", string(mcpServerName))
            return string(mcpServerName)
        }
    }
    
    // 从MCP特定头部获取
    mcpServerName, err := proxywasm.GetHttpRequestHeader("x-envoy-mcp-server-name")
    if err == nil && mcpServerName != "" {
        log.Debugf("[db-log-pusher] got mcp_server from x-envoy-mcp-server-name: %s", mcpServerName)
        return mcpServerName
    }
    
    // 如果没有找到准确的MCP Server信息，返回空字符串而不是"unknown"
    // 这样符合"没有就是没有"的原则，避免歧义
    return ""
}

// 获取MCP Tool
func getMCPTool(ctx wrapper.HttpContext) string {
	// 方法1: 从标准MCP工具头获取（最准确）
	// Higress系统通过x-envoy-mcp-tool-name header传递工具名称
	toolName, err := proxywasm.GetHttpRequestHeader("x-envoy-mcp-tool-name")
	if err == nil && toolName != "" {
		log.Debugf("[db-log-pusher] got mcp_tool from header: %s", toolName)
		return toolName
	}
	
	// 方法2: 从请求体中提取工具名称（备选方案）
	// 适用于tools/call请求，从params.name字段提取
	requestBody := ctx.GetContext("req_body")
	if requestBody != nil {
		if bodyStr, ok := requestBody.(string); ok && bodyStr != "" {
			// 尝试从JSON请求体中提取tool name
			toolNameFromBody := extractToolNameFromJson(bodyStr)
			if toolNameFromBody != "" {
				log.Debugf("[db-log-pusher] got mcp_tool from request body: %s", toolNameFromBody)
				return toolNameFromBody
			}
		}
	}
	
	return ""
}

// 从请求体提取模型名称
func extractModelFromRequestBody(body string) string {
	result := gjson.Get(body, "model")
	if result.Exists() {
		return result.String()
	}
	return ""
}

// 从JSON请求体中提取MCP工具名称
func extractToolNameFromJson(body string) string {
	// 对于tools/call请求，工具名称在params.name字段中
	result := gjson.Get(body, "params.name")
	if result.Exists() {
		return result.String()
	}
	return ""
}

// 获取 Envoy 属性 (int64 类型)
func getEnvoyPropertyInt64(path string, defaultValue int64) int64 {
	// Envoy 属性路径格式，参考: https://www.envoyproxy.io/docs/envoy/latest/intro/arch_overview/advanced/attributes
	var propertyPath []string
	
	switch path {
	case "request.total_size":
		// 正确的属性路径应该是 request.total_size
		propertyPath = []string{"request", "total_size"}
	case "response.total_size":
		// 正确的属性路径应该是 response.total_size
		propertyPath = []string{"response", "total_size"}
	default:
		log.Debugf("[db-log-pusher] unknown property path: %s", path)
		return defaultValue
	}
	
	value, err := proxywasm.GetProperty(propertyPath)
	if err != nil {
		log.Debugf("[db-log-pusher] failed to get property %v: %v", propertyPath, err)
		return defaultValue
	}
	
	if len(value) == 0 {
		log.Debugf("[db-log-pusher] property %v is empty", propertyPath)
		return defaultValue
	}
	
	// Envoy 属性值是 little-endian 格式的 uint64，需要正确解析
	// 参考：https://github.com/proxy-wasm/spec/tree/master/abi-versions/vNEXT
	if len(value) != 8 {
		log.Debugf("[db-log-pusher] property %v has unexpected length: %d", propertyPath, len(value))
		return defaultValue
	}
	
	// 将 8 字节的 little-endian 数据转换为 int64
	intValue := int64(binary.LittleEndian.Uint64(value))
	log.Debugf("[db-log-pusher] got property %v = %d", propertyPath, intValue)
	
	return intValue
}

// 获取 Envoy 属性 (int64 类型)
func getResponseTotalSize() int64 {
	// 首先尝试直接获取 response.total_size
	size := getEnvoyPropertyInt64("response.total_size", 0)
	if size > 0 {
		log.Debugf("[db-log-pusher] got response.total_size directly: %d", size)
		return size
	}
	
	// 如果为0，尝试从 Content-Length 头获取
	if contentLengthStr, err := proxywasm.GetHttpResponseHeader("content-length"); err == nil {
		if contentLength, err := strconv.ParseInt(contentLengthStr, 10, 64); err == nil {
			log.Debugf("[db-log-pusher] using Content-Length header as fallback: %d", contentLength)
			return contentLength
		}
	}
	
	// 检查是否为流式传输
	if transferEncoding, err := proxywasm.GetHttpResponseHeader("transfer-encoding"); err == nil {
		log.Debugf("[db-log-pusher] response is using Transfer-Encoding: %s", transferEncoding)
		// 对于流式传输，可能需要特殊处理
	}
	
	// 最后的兜底方案：返回0并记录警告
	log.Warnf("[db-log-pusher] unable to determine response size, returning 0")
	return 0
}
