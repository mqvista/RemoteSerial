// Package buildinfo 保存构建时通过 -ldflags -X 注入的版本信息。
package buildinfo

import "fmt"

var (
	// Version 版本号，发布时由构建脚本注入。
	Version = "dev"
	// Commit 构建时的 git 短提交号。
	Commit = "none"
	// Date 构建时间，UTC，RFC3339。
	Date = "unknown"
)

// String 返回单行版本描述。
func String() string {
	return fmt.Sprintf("rsagent %s (commit %s, built %s)", Version, Commit, Date)
}
