package firewall

import "testing"

func TestValidatePort(t *testing.T) {
	ok := []string{"80", "443", "8080", "1", "65535", "8000:8100", " 443 "}
	for _, p := range ok {
		if _, err := ValidatePort(p); err != nil {
			t.Errorf("ValidatePort(%q) 不应失败: %v", p, err)
		}
	}
	bad := []string{"", "0", "65536", "70000", "abc", "80/tcp", "80;rm", "-1", "8000:", ":80", "80:70", "8:9:10"}
	for _, p := range bad {
		if _, err := ValidatePort(p); err == nil {
			t.Errorf("ValidatePort(%q) 应被拒绝", p)
		}
	}
}

func TestValidateProto(t *testing.T) {
	if _, err := ValidateProto("tcp"); err != nil {
		t.Error("tcp 应合法")
	}
	if _, err := ValidateProto("UDP"); err != nil {
		t.Error("UDP 应合法")
	}
	if _, err := ValidateProto(""); err != nil {
		t.Error("空协议应默认 tcp")
	}
	for _, p := range []string{"icmp", "all", "tcp;rm", "udp,rm"} {
		if _, err := ValidateProto(p); err == nil {
			t.Errorf("ValidateProto(%q) 应被拒绝", p)
		}
	}
}
