package docker

import (
	"bytes"
	"sync"
	"testing"
)

// TestExecSlotTracker 并发槽位:上限内放行、超出拒绝、释放后恢复。
func TestExecSlotTracker(t *testing.T) {
	tr := newExecSlotTracker()
	if !tr.acquire("c1") {
		t.Fatal("第一个槽位应放行")
	}
	if !tr.acquire("c1") {
		t.Fatal("第二个槽位应放行(上限 2)")
	}
	if tr.acquire("c1") {
		t.Fatal("第三个并发应拒绝")
	}
	// 不同容器互不影响
	if !tr.acquire("c2") {
		t.Fatal("不同容器应独立计数")
	}
	tr.release("c1")
	tr.release("c1")
	if !tr.acquire("c1") {
		t.Fatal("释放后应恢复可用")
	}
	// 并发安全:多个 goroutine 同时抢占
	var wg sync.WaitGroup
	ok := 0
	var mu sync.Mutex
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tr.acquire("c3") {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != MaxExecConcurrency {
		t.Fatalf("并发上限应恰好放行 %d 个,实际 %d", MaxExecConcurrency, ok)
	}
	for i := 0; i < MaxExecConcurrency; i++ {
		tr.release("c3")
	}
}

// TestCapWriter 输出上限:截断标记 + 超限报错 + 数据确实写入 dst。
func TestCapWriter(t *testing.T) {
	var buf bytes.Buffer
	w := &capWriter{limit: 10, dst: &buf}
	n, err := w.Write([]byte("hello"))
	if n != 5 || err != nil || w.truncated {
		t.Fatalf("未超限写入应成功,got n=%d err=%v truncated=%v", n, err, w.truncated)
	}
	n, err = w.Write([]byte("world!extra"))
	if n != 5 || err != errOutputLimit || !w.truncated {
		t.Fatalf("超限写入应截断到 5 并报错,got n=%d err=%v truncated=%v", n, err, w.truncated)
	}
	// 已满后继续写:直接拒绝
	n, err = w.Write([]byte("x"))
	if n != 0 || err != errOutputLimit {
		t.Fatalf("已满后应拒绝,got n=%d err=%v", n, err)
	}
	if got := buf.String(); got != "helloworld" {
		t.Fatalf("dst 内容应为 helloworld,got %q", got)
	}
}
