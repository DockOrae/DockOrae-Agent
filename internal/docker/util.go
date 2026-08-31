package docker

import (
	"encoding/json"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// 类型对齐:供 dfRow 使用
type _containerState = string
