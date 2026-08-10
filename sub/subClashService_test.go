package sub

import "testing"

func TestApplyTransportHTTPHeader(t *testing.T) {
	service := &SubClashService{}
	proxy := map[string]any{}
	stream := map[string]any{
		"tcpSettings": map[string]any{
			"header": map[string]any{
				"type": "http",
				"request": map[string]any{
					"method": "GET",
					"path":   []any{"/"},
					"headers": map[string]any{
						"Host": []any{"example.com"},
					},
				},
			},
		},
	}

	if !service.applyTransport(proxy, "tcp", stream) {
		t.Fatal("HTTP camouflage should be supported")
	}
	if got := proxy["network"]; got != "http" {
		t.Fatalf("network = %v, want http", got)
	}
	httpOpts, ok := proxy["http-opts"].(map[string]any)
	if !ok {
		t.Fatalf("http-opts missing or has unexpected type: %T", proxy["http-opts"])
	}
	if got := httpOpts["method"]; got != "GET" {
		t.Fatalf("method = %v, want GET", got)
	}
	paths, ok := httpOpts["path"].([]any)
	if !ok || len(paths) != 1 || paths[0] != "/" {
		t.Fatalf("path = %#v, want [/ ]", httpOpts["path"])
	}
	if _, ok := httpOpts["headers"].(map[string]any); !ok {
		t.Fatalf("headers missing or has unexpected type: %T", httpOpts["headers"])
	}
}

func TestApplyTransportRawTCP(t *testing.T) {
	service := &SubClashService{}
	proxy := map[string]any{}
	stream := map[string]any{
		"tcpSettings": map[string]any{
			"header": map[string]any{"type": "none"},
		},
	}

	if !service.applyTransport(proxy, "tcp", stream) {
		t.Fatal("raw TCP should be supported")
	}
	if got := proxy["network"]; got != "tcp" {
		t.Fatalf("network = %v, want tcp", got)
	}
	if _, exists := proxy["http-opts"]; exists {
		t.Fatal("raw TCP must not include http-opts")
	}
}

func TestApplyTransportRejectsUnsupportedTCPHeader(t *testing.T) {
	service := &SubClashService{}
	proxy := map[string]any{}
	stream := map[string]any{
		"tcpSettings": map[string]any{
			"header": map[string]any{"type": "unsupported"},
		},
	}

	if service.applyTransport(proxy, "tcp", stream) {
		t.Fatal("unsupported TCP header should be rejected")
	}
}
