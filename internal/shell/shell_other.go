//go:build !windows

package shell

import (
	"context"
	"errors"
	"log/slog"
)

// Options 是壳层参数。非 Windows 平台没有桌面壳。
type Options struct {
	Listen  string
	DataDir string
	Mode    string
	Log     *slog.Logger
}

// Shell 在非 Windows 平台是空实现。
type Shell struct{}

// New 创建壳层。
func New(Options) *Shell { return &Shell{} }

// Open 无操作。
func (*Shell) Open() {}

// Quit 无操作。
func (*Shell) Quit() {}

// Run 返回不支持错误。
func (*Shell) Run(context.Context) error {
	return errors.New("桌面壳仅支持 Windows，Linux 站点请用 run 子命令并配合 systemd")
}
