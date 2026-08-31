package sysctl

import "testing"

func TestWhitelist(t *testing.T) {
	ok := map[string]string{
		"vm.swappiness":        "10",
		"fs.file-max":          "65535",
		"net.core.somaxconn":   "4096",
		"net.ipv4.ip_forward":  "1",
		"vm.overcommit_memory": "0",
	}
	for k, v := range ok {
		if _, err := ValidateKey(k); err != nil {
			t.Errorf("ValidateKey(%q) 应在白名单: %v", k, err)
		}
		if _, err := ValidateValue(k, v); err != nil {
			t.Errorf("ValidateValue(%q=%q) 应合法: %v", k, v, err)
		}
	}
	// 非法 key:格式错误或不在白名单
	badKeys := []string{
		"kernel.printk",                // 不在白名单
		"net.ipv4.conf.all.forwarding", // 不在白名单
		"../etc/passwd",                // 路径注入
		"vm.swappiness;reboot",         // 注入
		"vm",                           // 无子键
		"",                             // 空
	}
	for _, k := range badKeys {
		if _, err := ValidateKey(k); err == nil {
			t.Errorf("ValidateKey(%q) 应被拒绝", k)
		}
	}
	// 非法值:key 在白名单但取值越界
	badValues := []struct{ k, v string }{
		{"vm.swappiness", "101"},
		{"vm.swappiness", "-1"},
		{"vm.swappiness", "abc"},
		{"fs.file-max", "0"},
		{"net.ipv4.ip_forward", "2"},
	}
	for _, b := range badValues {
		if _, err := ValidateValue(b.k, b.v); err == nil {
			t.Errorf("ValidateValue(%q=%q) 应被拒绝", b.k, b.v)
		}
	}
}
