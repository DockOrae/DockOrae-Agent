package compose

import "testing"

// 新版(v5.5)images --format json 实测输出
const newImagesJSON = `[{"ID":"sha256:65645c7bb6a0661892a8b03b89d0743208a18dd2f3f17a54ef4b76fb8e2f2a10","ContainerName":"demo-web","Repository":"nginx","Tag":"1.27-alpine","Platform":"linux/amd64","Size":20984244,"Created":"2025-04-16T14:50:31Z","LastTagTime":"2026-08-31T09:48:27.328979162Z"}]`

// 旧版 images --format json 输出(Service/Image/Digest 字段)
const oldImagesJSON = `[{"Service":"web","Image":"nginx:1.27-alpine","Digest":"sha256:65645c7bb6a0661892a8b03b89d0743208a18dd2f3f17a54ef4b76fb8e2f2a10","ContainerName":"demo-web-1"}]`

// 新版(v5.5)ps --format json 实测输出(单对象)
const newPSJSON = `{"Command":"\"/docker-entrypoint.sh\"","ExitCode":0,"Health":"","ID":"c35c04fdd3aa","Image":"nginx:1.27-alpine","Name":"demo-web","Names":"demo-web","Service":"web","State":"running","Status":"Up 9 minutes"}`

// 旧版 ps --format json 输出(数组形态)
const oldPSJSON = `[{"Name":"demo-web","Service":"web","State":"running","Status":"Up 1 hour"}]`

func TestParseComposeImagesNewFormat(t *testing.T) {
	recs, err := parseComposeImages([]byte(newImagesJSON))
	if err != nil {
		t.Fatalf("新版格式解析失败: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("应解析出 1 条记录,实际 %d", len(recs))
	}
	r := recs[0]
	if r.Service != "demo-web" {
		t.Errorf("Service 应= demo-web,实际 %q", r.Service)
	}
	if r.Image != "nginx:1.27-alpine" {
		t.Errorf("Image 应= nginx:1.27-alpine,实际 %q", r.Image)
	}
	if r.Digest != "sha256:65645c7bb6a0661892a8b03b89d0743208a18dd2f3f17a54ef4b76fb8e2f2a10" {
		t.Errorf("Digest 解析错误: %q", r.Digest)
	}
}

func TestParseComposeImagesOldFormat(t *testing.T) {
	recs, err := parseComposeImages([]byte(oldImagesJSON))
	if err != nil {
		t.Fatalf("旧版格式解析失败: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("应解析出 1 条记录,实际 %d", len(recs))
	}
	r := recs[0]
	if r.Service != "web" {
		t.Errorf("Service 应= web,实际 %q", r.Service)
	}
	if r.Image != "nginx:1.27-alpine" {
		t.Errorf("Image 应= nginx:1.27-alpine,实际 %q", r.Image)
	}
	if r.Digest != "sha256:65645c7bb6a0661892a8b03b89d0743208a18dd2f3f17a54ef4b76fb8e2f2a10" {
		t.Errorf("Digest 解析错误: %q", r.Digest)
	}
}

func TestParseComposeImagesBothFormatsEqual(t *testing.T) {
	newRecs, err := parseComposeImages([]byte(newImagesJSON))
	if err != nil {
		t.Fatal(err)
	}
	oldRecs, err := parseComposeImages([]byte(oldImagesJSON))
	if err != nil {
		t.Fatal(err)
	}
	// 两种格式的 Digest 应一致(同一镜像)
	if newRecs[0].Digest != oldRecs[0].Digest {
		t.Errorf("新旧格式 digest 应一致: %q vs %q", newRecs[0].Digest, oldRecs[0].Digest)
	}
}

func TestParseComposePSNewFormat(t *testing.T) {
	cs := parseComposePS([]byte(newPSJSON))
	if len(cs) != 1 {
		t.Fatalf("单对象形态应解析出 1 个容器,实际 %d", len(cs))
	}
	if st, _ := cs[0]["State"].(string); st != "running" {
		t.Errorf("State 应= running,实际 %q", st)
	}
	if svc, _ := cs[0]["Service"].(string); svc != "web" {
		t.Errorf("Service 应= web,实际 %q", svc)
	}
}

func TestParseComposePSOldFormat(t *testing.T) {
	cs := parseComposePS([]byte(oldPSJSON))
	if len(cs) != 1 {
		t.Fatalf("数组形态应解析出 1 个容器,实际 %d", len(cs))
	}
	if st, _ := cs[0]["State"].(string); st != "running" {
		t.Errorf("State 应= running,实际 %q", st)
	}
}

func TestParseComposePSMultiLine(t *testing.T) {
	// 多行形态(新版多容器可能逐行输出)
	multi := "{\"Name\":\"demo-web-1\",\"State\":\"running\"}\n{\"Name\":\"demo-web-2\",\"State\":\"exited\"}\n"
	cs := parseComposePS([]byte(multi))
	if len(cs) != 2 {
		t.Fatalf("多行形态应解析出 2 个容器,实际 %d", len(cs))
	}
	if st, _ := cs[1]["State"].(string); st != "exited" {
		t.Errorf("第 2 个容器 State 应= exited,实际 %q", st)
	}
}
