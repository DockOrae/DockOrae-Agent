package swap

import (
	"testing"
)

func TestValidateSize(t *testing.T) {
	cases := []struct {
		sizeMB int
		ok     bool
	}{
		{512, true}, {1024, true}, {2048, true}, {4096, true},
		{0, false}, {-1, false}, {511, false}, {100, false},
		{65536, true}, {65537, false}, {100000, false},
	}
	for _, c := range cases {
		_, err := ValidateSize(c.sizeMB)
		if (err == nil) != c.ok {
			t.Errorf("ValidateSize(%d) ok=%v, want %v (err=%v)", c.sizeMB, err == nil, c.ok, err)
		}
	}
}

func TestPresetSizes(t *testing.T) {
	want := []int{512, 1024, 2048, 4096}
	if len(PresetSizesMB) != len(want) {
		t.Fatalf("预设大小必须为 512/1G/2G/4G,got %v", PresetSizesMB)
	}
	for i := range want {
		if PresetSizesMB[i] != want[i] {
			t.Fatalf("预设 %d 应为 %d,got %v", i, want[i], PresetSizesMB)
		}
	}
}

func TestValidatePath(t *testing.T) {
	ok := []string{"", "/swapfile", "/swapfile2", "/srv/data/swap.img"}
	for _, p := range ok {
		if _, err := ValidatePath(p); err != nil {
			t.Errorf("ValidatePath(%q) 不应失败: %v", p, err)
		}
	}
	bad := []string{
		"/dev/sda1", "/dev", "/proc/swap", "/sys/foo", "/etc/passwd", "/var/swap",
		"/run/swap", "/tmp/swap", "relative/path", "../etc", "/", "//",
		"/swap file", "/swap;rm", "/swap$(id)",
	}
	for _, p := range bad {
		if _, err := ValidatePath(p); err == nil {
			t.Errorf("ValidatePath(%q) 应被拒绝", p)
		}
	}
}

func TestControlledSwapName(t *testing.T) {
	ok := []string{"/swapfile", "/swapfile1", "/swapfile2", "/swapfile.new", "/swapfile2.new"}
	bad := []string{"/dev/sda1", "/swapfilex", "/var/swap", "/swap", "/swapfile.newx"}
	for _, p := range ok {
		if !isControlledName(p) {
			t.Errorf("isControlledName(%q) 应为 true", p)
		}
	}
	for _, p := range bad {
		if isControlledName(p) {
			t.Errorf("isControlledName(%q) 应为 false", p)
		}
	}
}
