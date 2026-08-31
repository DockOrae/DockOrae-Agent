package system

import "testing"

func TestValidateServiceName(t *testing.T) {
	ok := []string{"docker", "docker-manager", "nginx@default", "ssh", "cron.service"}
	for _, n := range ok {
		if _, err := ValidateServiceName(n); err != nil {
			t.Errorf("ValidateServiceName(%q) 不应失败: %v", n, err)
		}
	}
	bad := []string{
		"", "docker;rm", "docker && rm", "-x", "..", "a b",
		"docker$(id)", "`id`", "x|y", "x>y",
	}
	for _, n := range bad {
		if _, err := ValidateServiceName(n); err == nil {
			t.Errorf("ValidateServiceName(%q) 应被拒绝", n)
		}
	}
}

func TestServiceActions(t *testing.T) {
	for _, a := range []string{"start", "stop", "restart", "enable", "disable", "status"} {
		if !validAction(a) {
			t.Errorf("action %q 应为合法", a)
		}
	}
	for _, a := range []string{"exec", "run", "rm -rf", "kill;reboot"} {
		if validAction(a) {
			t.Errorf("action %q 应为非法", a)
		}
	}
}

func validAction(a string) bool {
	switch a {
	case "start", "stop", "restart", "enable", "disable", "status":
		return true
	}
	return false
}

func TestValidateTimezone(t *testing.T) {
	bad := []string{"", "..", "../../etc/passwd", "Asia/Shanghai;rm", "a b"}
	for _, tz := range bad {
		if _, err := (&Service{}).ValidateTimezone(tz); err == nil {
			t.Errorf("ValidateTimezone(%q) 应被拒绝(格式层)", tz)
		}
	}
}
