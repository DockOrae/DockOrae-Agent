package binary

import "testing"

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.0.3", "v1.0.3", 0},
		{"v1.0.2", "v1.0.3", -1},
		{"v1.10.0", "v1.2.0", 1},
		{"v1.3.0", "v1.3.0-rc.1", 1},
		{"", "v0.0.1", -1},
		{"v0.1.0", "", 1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestValidReleaseTag(t *testing.T) {
	for _, v := range []string{"v1.0.3", "v0.0.1", "v2.10.0"} {
		if !validReleaseTag(v) {
			t.Errorf("%q 应合法", v)
		}
	}
	for _, v := range []string{"", "1.0.3", "v1", "v1.0", "latest", "v1.0.3-rc1", "v1.0.3;rm"} {
		if validReleaseTag(v) {
			t.Errorf("%q 应非法", v)
		}
	}
}

func TestRetagImageValue(t *testing.T) {
	cases := []struct {
		val, tag, want string
	}{
		{"dockorae/dockorae-agent:latest", "v1.0.3", "dockorae/dockorae-agent:v1.0.3"},
		{"dockorae/dockorae-agent:v1.0.2", "v1.0.3", "dockorae/dockorae-agent:v1.0.3"},
		{"dockorae/dockorae-agent@sha256:abc", "v1.0.3", "dockorae/dockorae-agent:v1.0.3"},
		{"dockorae-agent", "v1.0.3", "dockorae-agent:v1.0.3"},
	}
	for _, c := range cases {
		if got := retagImageValue(c.val, c.tag); got != c.want {
			t.Errorf("retagImageValue(%q,%q)=%q want %q", c.val, c.tag, got, c.want)
		}
	}
}

func TestFindManagerImage(t *testing.T) {
	yaml := `services:
  app:
    image: nginx:latest
  agent:
    image: dockorae/dockorae-agent:latest
  web:
    image: nginx:1.25`
	val, ok := findManagerImage(yaml)
	if !ok || val != "dockorae/dockorae-agent:latest" {
		t.Errorf("findManagerImage 应找到 agent 镜像,got %q ok=%v", val, ok)
	}
	if _, ok := findManagerImage("services:\n  app:\n    image: nginx\n"); ok {
		t.Error("无 agent 镜像时应返回 false")
	}
}
