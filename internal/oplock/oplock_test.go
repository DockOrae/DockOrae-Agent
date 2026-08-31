package oplock

import (
	"testing"
)

func TestAcquireRelease(t *testing.T) {
	m := New()
	if err := m.Acquire("swap", "req1"); err != nil {
		t.Fatalf("首次获取应成功: %v", err)
	}
	// 重复获取应拒绝
	if err := m.Acquire("swap", "req2"); err == nil {
		t.Fatal("重复获取应返回 OPERATION_IN_PROGRESS")
	} else if err.Error() == "" {
		t.Fatal("错误应有消息")
	}
	// 不同锁互不影响
	if err := m.Acquire("update", "req2"); err != nil {
		t.Fatalf("不同锁应可获取: %v", err)
	}
	m.Release("swap")
	if err := m.Acquire("swap", "req3"); err != nil {
		t.Fatalf("释放后应可重新获取: %v", err)
	}
}

func TestAcquireMany(t *testing.T) {
	m := New()
	if err := m.AcquireMany([]string{"swap", "update", "compose"}, "req1"); err != nil {
		t.Fatalf("批量获取应成功: %v", err)
	}
	// 另一个请求尝试获取已占用的锁,应失败且不遗留
	if err := m.AcquireMany([]string{"system", "compose"}, "req2"); err == nil {
		t.Fatal("批量获取已占用锁应失败")
	}
	// 失败后 system 不应被遗留占用
	if err := m.Acquire("system", "req3"); err != nil {
		t.Fatalf("失败后应释放已获取的锁: %v", err)
	}
}
