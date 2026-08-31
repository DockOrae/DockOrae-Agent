package apiserver

import "testing"

// 路由模式匹配:普通路由 {param} 与 WS 路由必须都能命中(回归:WS 曾因 :param/{param} 语法不一致 404)
func TestRouteMatch(t *testing.T) {
	cases := []struct {
		method, pattern, path string
		wantMatch             bool
		wantParam             string
	}{
		{"GET", "/v1/docker/containers/{id}/logs", "/v1/docker/containers/abc123/logs", true, "abc123"},
		{"GET", "/v1/docker/containers/{id}/logs", "/v1/docker/containers/abc123/stats", false, ""},
		{"POST", "/v1/docker/containers/{id}/start", "/v1/docker/containers/xyz/start", true, "xyz"},
		{"GET", "/v1/docker/containers/{id}/logs", "/v1/docker/containers/a/b/logs", false, ""},
		{"GET", "/v1/docker/containers/{id}/logs", "/v1/containers/abc123/logs", false, ""},
		{"GET", "/v1/compose/managed/logs", "/v1/compose/managed/logs", true, ""},
		{"GET", "/v1/compose/managed/logs", "/v1/compose/managed/up", false, ""},
		{"GET", "/v1/docker/events", "/v1/docker/events", true, ""},
	}
	for _, c := range cases {
		r := wsRoute{method: c.method, pattern: c.pattern}
		params, ok := r.match(c.method, c.path)
		if ok != c.wantMatch {
			t.Errorf("wsRoute %s %s vs %s: match=%v want %v", c.method, c.pattern, c.path, ok, c.wantMatch)
			continue
		}
		if ok {
			if got := params["id"]; got != c.wantParam {
				t.Errorf("wsRoute %s: id=%q want %q", c.pattern, got, c.wantParam)
			}
		}
		// 普通路由同语法
		rr := route{method: c.method, pattern: c.pattern}
		if _, ok2 := rr.match(c.method, c.path); ok2 != c.wantMatch {
			t.Errorf("route %s %s vs %s: match=%v want %v", c.method, c.pattern, c.path, ok2, c.wantMatch)
		}
	}
}
