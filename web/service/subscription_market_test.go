package service

import (
	"encoding/base64"
	"testing"
)

func TestSubscriptionURINameLegacyVMessRemarks(t *testing.T) {
	link := "vmess://YXV0bzpiZDI0ZWVjMS1kZmVhLTM4OTgtOTMwNy03MGE0MjdmYjJjYzJAc2FrZm5zZ2plOC5janh5b3VuZy5jb206MTA1NDY?alterId=0&remarks=%F0%9F%87%B9%F0%9F%87%BC%20IPLC-V310-%E5%8F%B0%E6%B9%BE-1x-NF%26Disney%26%E5%8A%A8%E7%94%BB%E7%96%AF%2A&obfs=none"
	want := "🇹🇼 IPLC-V310-台湾-1x-NF&Disney&动画疯*"
	if got := subscriptionURIName(link); got != want {
		t.Fatalf("subscriptionURIName() = %q, want %q", got, want)
	}
}

func TestParseVMessLinkDataLegacyURI(t *testing.T) {
	link := "vmess://YXV0bzpiZDI0ZWVjMS1kZmVhLTM4OTgtOTMwNy03MGE0MjdmYjJjYzJAc2FrZm5zZ2plOC5janh5b3VuZy5jb206MTA1NDY?alterId=0&remarks=%F0%9F%87%B9%F0%9F%87%BC%20IPLC-V310-%E5%8F%B0%E6%B9%BE-1x-NF%26Disney%26%E5%8A%A8%E7%94%BB%E7%96%AF%2A&obfs=none"
	data, ok := parseVMessLinkData(link)
	if !ok {
		t.Fatal("parseVMessLinkData() failed for legacy URI")
	}
	if data["add"] != "sakfnsgje8.cjxyoung.com" {
		t.Fatalf("add = %v", data["add"])
	}
	if data["id"] != "bd24eec1-dfea-3898-9307-70a427fb2cc2" {
		t.Fatalf("id = %v", data["id"])
	}
	if port, ok := anyToPositiveInt(data["port"]); !ok || port != 10546 {
		t.Fatalf("port = %v", data["port"])
	}
	if data["net"] != "tcp" {
		t.Fatalf("net = %v", data["net"])
	}
}

func TestAuthenticatedVMessRelayLinkLegacyURI(t *testing.T) {
	source := "vmess://YXV0bzpiZDI0ZWVjMS1kZmVhLTM4OTgtOTMwNy03MGE0MjdmYjJjYzJAc2FrZm5zZ2plOC5janh5b3VuZy5jb206MTA1NDY?alterId=0&remarks=legacy&obfs=none"
	link, ok := authenticatedVMessRelayLink(source, "relay-node", "fa.xux-ai.top", 20000, "11111111-2222-3333-4444-555555555555")
	if !ok {
		t.Fatal("authenticatedVMessRelayLink() failed for legacy URI")
	}
	data, ok := parseVMessLinkData(link)
	if !ok {
		t.Fatal("generated relay link did not parse")
	}
	if data["add"] != "fa.xux-ai.top" || data["id"] != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("unexpected relay data: %#v", data)
	}
	if port, ok := anyToPositiveInt(data["port"]); !ok || port != 20000 {
		t.Fatalf("port = %v", data["port"])
	}
}

func TestSubscriptionURINameVMessJSON(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte(`{"ps":"json-node","add":"example.com","port":443,"id":"uuid"}`))
	if got := subscriptionURIName("vmess://" + payload); got != "json-node" {
		t.Fatalf("subscriptionURIName() = %q, want json-node", got)
	}
}
