package apiserver

import "github.com/DockOrae/DockOrae-Agent/internal/sysctl"

func sysctlKeys() []string { return sysctl.Keys() }
