package main

import (
	"encoding/json"
	"testing"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	wasmtest "github.com/higress-group/wasm-go/pkg/test"
)

func TestStreamingResponsePassesThroughAndLogsCompleteBody(t *testing.T) {
	host := newStartedTestHost(t)
	defer host.Reset()

	callRequest(t, host)
	if action := host.CallOnHttpResponseHeaders([][2]string{
		{":status", "200"},
		{"content-type", "text/event-stream"},
	}); action != types.ActionContinue {
		t.Fatalf("response headers action = %v, want ActionContinue", action)
	}

	firstChunk := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"
	secondChunk := "data: {\"choices\":[{\"delta\":{\"content\":\" world\"}}]}\n\n"
	doneChunk := "data: [DONE]\n\n"

	if action := host.CallOnHttpStreamingResponseBody([]byte(firstChunk), false); action != types.ActionContinue {
		t.Fatalf("first streaming chunk action = %v, want ActionContinue", action)
	}
	if action := host.CallOnHttpStreamingResponseBody([]byte(secondChunk), false); action != types.ActionContinue {
		t.Fatalf("second streaming chunk action = %v, want ActionContinue", action)
	}
	if action := host.CallOnHttpStreamingResponseBody([]byte(doneChunk), true); action != types.ActionContinue {
		t.Fatalf("final streaming chunk action = %v, want ActionContinue", action)
	}

	callouts := host.GetHttpCalloutAttributes()
	if len(callouts) != 1 {
		t.Fatalf("collector callout count = %d, want 1", len(callouts))
	}

	payload := unmarshalCollectorPayload(t, callouts[0].Body)

	wantBody := firstChunk + secondChunk + doneChunk
	if payload.RespBody != wantBody {
		t.Fatalf("logged resp_body = %q, want complete streaming body %q", payload.RespBody, wantBody)
	}
	if payload.BytesSent != int64(len(wantBody)) {
		t.Fatalf("logged bytes_sent = %d, want streamed body size %d", payload.BytesSent, len(wantBody))
	}
}

func TestStreamingResponseStreamDoneLogsPartialBody(t *testing.T) {
	host := newStartedTestHost(t)
	defer host.Reset()

	callRequest(t, host)
	if action := host.CallOnHttpResponseHeaders([][2]string{
		{":status", "200"},
		{"content-type", "text/event-stream"},
	}); action != types.ActionContinue {
		t.Fatalf("response headers action = %v, want ActionContinue", action)
	}

	firstChunk := "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"
	secondChunk := "data: {\"choices\":[{\"delta\":{\"content\":\" stream\"}}]}\n\n"

	if action := host.CallOnHttpStreamingResponseBody([]byte(firstChunk), false); action != types.ActionContinue {
		t.Fatalf("first streaming chunk action = %v, want ActionContinue", action)
	}
	if action := host.CallOnHttpStreamingResponseBody([]byte(secondChunk), false); action != types.ActionContinue {
		t.Fatalf("second streaming chunk action = %v, want ActionContinue", action)
	}

	host.CompleteHttp()

	callouts := host.GetHttpCalloutAttributes()
	if len(callouts) != 1 {
		t.Fatalf("collector callout count = %d, want 1", len(callouts))
	}

	payload := unmarshalCollectorPayload(t, callouts[0].Body)
	wantBody := firstChunk + secondChunk
	if payload.RespBody != wantBody {
		t.Fatalf("logged resp_body = %q, want partial streaming body %q", payload.RespBody, wantBody)
	}
	if payload.BytesSent != int64(len(wantBody)) {
		t.Fatalf("logged bytes_sent = %d, want streamed body size %d", payload.BytesSent, len(wantBody))
	}
}

func TestNonStreamingResponseStillLogsCompleteBody(t *testing.T) {
	host := newStartedTestHost(t)
	defer host.Reset()

	callRequest(t, host)
	if action := host.CallOnHttpResponseHeaders([][2]string{
		{":status", "200"},
		{"content-type", "application/json"},
	}); action != types.ActionContinue {
		t.Fatalf("response headers action = %v, want ActionContinue", action)
	}

	body := `{"choices":[{"message":{"content":"complete"}}]}`
	if action := host.CallOnHttpResponseBody([]byte(body)); action != types.ActionContinue {
		t.Fatalf("response body action = %v, want ActionContinue", action)
	}

	callouts := host.GetHttpCalloutAttributes()
	if len(callouts) != 1 {
		t.Fatalf("collector callout count = %d, want 1", len(callouts))
	}

	payload := unmarshalCollectorPayload(t, callouts[0].Body)
	if payload.RespBody != body {
		t.Fatalf("logged resp_body = %q, want full non-streaming body %q", payload.RespBody, body)
	}
}

type collectorPayload struct {
	RespBody  string `json:"resp_body"`
	BytesSent int64  `json:"bytes_sent"`
}

func unmarshalCollectorPayload(t *testing.T, body []byte) collectorPayload {
	t.Helper()

	var payload collectorPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("failed to unmarshal collector payload: %v", err)
	}
	return payload
}

func newStartedTestHost(t *testing.T) wasmtest.TestHost {
	t.Helper()

	host, status := wasmtest.NewTestHost(json.RawMessage(`{
		"collector_service_name": "collector.dns",
		"collector_port": 80
	}`))
	if status != types.OnPluginStartStatusOK {
		t.Fatalf("plugin failed to start: %v", status)
	}
	return host
}

func callRequest(t *testing.T, host wasmtest.TestHost) {
	t.Helper()

	if action := host.CallOnHttpRequestHeaders([][2]string{
		{":authority", "example.com"},
		{":method", "POST"},
		{":path", "/v1/chat/completions"},
	}); action != types.ActionContinue {
		t.Fatalf("request headers action = %v, want ActionContinue", action)
	}
	if action := host.CallOnHttpRequestBody([]byte(`{"model":"qwen","stream":true}`)); action != types.ActionContinue {
		t.Fatalf("request body action = %v, want ActionContinue", action)
	}
}
