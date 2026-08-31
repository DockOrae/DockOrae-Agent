package host

import "testing"

func TestValidateHostname(t *testing.T) {
	ok := []string{"myhost", "my-host-1", "a", "host123", "A-B-C"}
	for _, h := range ok {
		if _, err := ValidateHostname(h); err != nil {
			t.Errorf("ValidateHostname(%q) 不应失败: %v", h, err)
		}
	}
	bad := []string{
		"", "-host", "host-", "host name", "host_name", "a/b",
		"host$(id)", "host;rm", "host..", ".host",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // 65 chars
	}
	for _, h := range bad {
		if _, err := ValidateHostname(h); err == nil {
			t.Errorf("ValidateHostname(%q) 应被拒绝", h)
		}
	}
}
