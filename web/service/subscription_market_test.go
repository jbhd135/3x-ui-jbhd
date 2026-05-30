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

func TestSubscriptionURINameVMessJSON(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte(`{"ps":"json-node","add":"example.com","port":443,"id":"uuid"}`))
	if got := subscriptionURIName("vmess://" + payload); got != "json-node" {
		t.Fatalf("subscriptionURIName() = %q, want json-node", got)
	}
}
