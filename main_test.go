package main

import (
	"bytes"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/test"
)

func TestResponseBodyCapture(t *testing.T) {
	for _, size := range []int{0, 12, 32768, 32769, 2749379} {
		t.Run(stringSize(size), func(t *testing.T) {
			body := bytes.Repeat([]byte("x"), size)
			capture := &responseBodyCapture{}
			for offset := 0; offset < len(body); offset += 4096 {
				end := offset + 4096
				if end > len(body) {
					end = len(body)
				}
				capture.appendChunk(body[offset:end])
			}
			if capture.total != int64(size) || len(capture.prefix) != min(size, responseBodySampleLimit) || len(capture.tail) != min(size, responseBodySampleLimit) {
				t.Fatalf("unexpected capture sizes: total=%d prefix=%d tail=%d", capture.total, len(capture.prefix), len(capture.tail))
			}
			if capture.truncated() != (size > responseBodySampleLimit) {
				t.Fatalf("unexpected truncation state for %d bytes", size)
			}
		})
	}
	// Ensure the tail is genuinely the end of the stream, including a chunk
	// larger than the sample limit and chunks that reuse their backing array.
	capture := &responseBodyCapture{}
	capture.appendChunk(bytes.Repeat([]byte("a"), 32769))
	capture.appendChunk([]byte("final"))
	if !bytes.Equal(capture.tail[len(capture.tail)-5:], []byte("final")) {
		t.Fatalf("tail does not contain the final chunk")
	}
}

func TestLargeJSONResponseIsNotBuffered(t *testing.T) {
	for _, size := range []int{32768, 32769, 2749379} {
		t.Run(stringSize(size), func(t *testing.T) {
			host, status := test.NewTestHost(json.RawMessage(`{"collector_service_name":"log-collector.static","collector_port":80}`))
			defer host.Reset()
			if status != types.OnPluginStartStatusOK {
				t.Fatalf("plugin start failed: %v", status)
			}
			if action := host.CallOnHttpRequestHeaders([][2]string{{":method", "GET"}, {":path", "/api/log"}, {":authority", "example.com"}}, test.WithEndOfStream(true)); action != types.ActionContinue {
				t.Fatalf("request headers action: %v", action)
			}
			if action := host.CallOnHttpResponseHeaders([][2]string{{":status", "200"}, {"content-type", "application/json"}}); action != types.ActionContinue {
				t.Fatalf("response headers action: %v", action)
			}
			body := make([]byte, size)
			copy(body, `{"items":["`)
			for i := len(`{"items":["`); i < len(body)-3; i++ {
				body[i] = 'x'
			}
			copy(body[len(body)-3:], `"]}`)
			var forwarded []byte
			for offset := 0; offset < len(body); offset += 4096 {
				end := min(offset+4096, len(body))
				if action := host.CallOnHttpStreamingResponseBody(body[offset:end], end == len(body)); action != types.ActionContinue {
					t.Fatalf("chunk [%d:%d] paused response: %v", offset, end, action)
				}
				// The test host exposes the current Envoy chunk, not the
				// accumulated downstream response.
				forwarded = append(forwarded, host.GetResponseBody()...)
			}
			if !bytes.Equal(forwarded, body) {
				t.Fatalf("response body changed: got %d bytes, want %d", len(forwarded), len(body))
			}
			host.CompleteHttp()
			if calls := len(host.GetHttpCalloutAttributes()); calls != 1 {
				t.Fatalf("expected exactly one log dispatch, got %d", calls)
			}
		})
	}
}

func TestEmptyAndInterruptedResponsesLoggedOnce(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		name := "empty"
		if interrupted {
			name = "interrupted"
		}
		t.Run(name, func(t *testing.T) {
			host, status := test.NewTestHost(json.RawMessage(`{"collector_service_name":"log-collector.static","collector_port":80}`))
			defer host.Reset()
			if status != types.OnPluginStartStatusOK {
				t.Fatalf("plugin start failed: %v", status)
			}
			host.CallOnHttpRequestHeaders([][2]string{{":method", "GET"}, {":path", "/api/log"}, {":authority", "example.com"}}, test.WithEndOfStream(true))
			host.CallOnHttpResponseHeaders([][2]string{{":status", "200"}}, test.WithEndOfStream(!interrupted))
			if interrupted {
				if action := host.CallOnHttpStreamingResponseBody([]byte("partial"), false); action != types.ActionContinue {
					t.Fatalf("partial response paused: %v", action)
				}
			}
			host.CompleteHttp()
			if calls := len(host.GetHttpCalloutAttributes()); calls != 1 {
				t.Fatalf("expected exactly one log dispatch, got %d", calls)
			}
		})
	}
}

func stringSize(size int) string {
	return strconv.Itoa(size)
}
