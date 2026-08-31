package cmd

// 版本信息,构建时由 ldflags 注入(与 DockOrae 主程序同款做法,发版打 tag 即可):
//
//	-X github.com/DockOrae/DockOrae-Agent/cmd.Version=v0.1.0
var (
	Version   string
	Commit    string
	BuildTime string
)

// DisplayVersion 展示用版本号:构建未注入时为 unknown
func DisplayVersion() string {
	if Version == "" {
		return "unknown"
	}
	return Version
}
