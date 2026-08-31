// 临时冒烟测试客户端(本地验证 Agent API,不入库)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	sock := os.Args[1]
	token := os.Args[2]
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", sock)
			},
		},
		Timeout: 30 * time.Second,
	}

	do := func(label, method, path string, body string, tok string) {
		var r io.Reader
		if body != "" {
			r = bytes.NewReader([]byte(body))
		}
		req, _ := http.NewRequest(method, "http://unix"+path, r)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("%-32s ERROR: %v\n", label, err)
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2000))
		var pretty bytes.Buffer
		_ = json.Indent(&pretty, b, "  ", " ")
		fmt.Printf("%-32s [%d] %s\n", label, resp.StatusCode, truncate(pretty.String(), 600))
	}

	do("1 无token", "GET", "/v1/health", "", "")
	do("2 错误token", "GET", "/v1/health", "", "wrong")
	do("3 health", "GET", "/v1/health", "", token)
	do("4 version", "GET", "/v1/version", "", token)
	do("5 swap/status", "GET", "/v1/swap/status", "", token)
	do("6 host/info", "GET", "/v1/host/info", "", token)
	do("7 404", "GET", "/v1/nonexistent", "", token)
	do("8 swap/create 缺confirm", "POST", "/v1/swap/create", `{"size_mb":2048}`, token)
	do("9 swap/create 非法大小", "POST", "/v1/swap/create", `{"size_mb":100,"confirm":true}`, token)
	do("10 swap/create 非法路径", "POST", "/v1/swap/create", `{"size_mb":1024,"path":"/dev/sda1","confirm":true}`, token)
	do("11 sysctl 非白名单", "POST", "/v1/sysctl/set", `{"key":"kernel.printk","value":"3"}`, token)
	do("12 sysctl 非法值", "POST", "/v1/sysctl/set", `{"key":"vm.swappiness","value":"999"}`, token)
	do("13 服务名注入", "POST", "/v1/system/service", `{"name":"docker;rm -rf /","action":"restart"}`, token)
	do("14 主机名注入", "POST", "/v1/host/hostname", `{"hostname":"x$(id)"}`, token)
	do("15 容器ID注入", "POST", "/v1/docker/containers/$(id)/start", `{}`, token)
	do("16 reboot 缺confirm", "POST", "/v1/host/reboot", `{}`, token)
	do("17 docker/status", "GET", "/v1/docker/status", "", token)
	do("18 disk/usage", "GET", "/v1/disk/usage", "", token)
	do("19 network/interfaces", "GET", "/v1/network/interfaces", "", token)
	do("20 system/time", "GET", "/v1/system/time", "", token)
	do("21 binary/status", "GET", "/v1/binary/status", "", token)
	do("22 compose 项目注入", "GET", "/v1/compose/status?project=../etc", "", token)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
