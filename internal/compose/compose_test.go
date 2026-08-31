package compose

import "testing"

func TestValidateProject(t *testing.T) {
	ok := []string{"dockorae", "my-stack_1", "app.v2", "nginx-proxy"}
	for _, p := range ok {
		if _, err := ValidateProject(p); err != nil {
			t.Errorf("ValidateProject(%q) 不应失败: %v", p, err)
		}
	}
	bad := []string{"", "a b", "a/b", "../etc", "a;rm", "-x", "x$(id)", "x`y`", "x|y"}
	for _, p := range bad {
		if _, err := ValidateProject(p); err == nil {
			t.Errorf("ValidateProject(%q) 应被拒绝", p)
		}
	}
}

func TestValidatePath(t *testing.T) {
	if _, err := ValidatePath("/opt/docker-manager/docker-compose.yml"); err != nil {
		t.Error("绝对路径应合法")
	}
	for _, p := range []string{"", "relative.yml", "..", "/opt/../etc", "/opt/a b.yml", "/x;rm"} {
		if _, err := ValidatePath(p); err == nil {
			t.Errorf("ValidatePath(%q) 应被拒绝", p)
		}
	}
}
