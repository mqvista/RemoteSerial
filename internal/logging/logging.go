// Package logging 用 slog 统一日志，输出到滚动文件、内存环和可选的标准错误。
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Options 是日志初始化参数。
type Options struct {
	Dir      string     // 日志文件目录
	Level    slog.Level // 初始最低级别
	Console  bool       // 同时输出到标准错误
	RingSize int        // 内存环容量，0 取默认 1000
}

// Logger 把 slog.Logger、内存环和可运行时调整的级别绑在一起。
type Logger struct {
	*slog.Logger
	Ring   *Ring
	Level  *slog.LevelVar
	closer io.Closer
}

// Setup 初始化日志。文件名固定为 rsagent.log，单文件 10MB，保留 5 个备份、30 天。
func Setup(o Options) (*Logger, error) {
	if err := os.MkdirAll(o.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建日志目录: %w", err)
	}
	file := &lumberjack.Logger{
		Filename:   filepath.Join(o.Dir, "rsagent.log"),
		MaxSize:    10,
		MaxBackups: 5,
		MaxAge:     30,
		LocalTime:  true,
	}
	lv := &slog.LevelVar{}
	lv.Set(o.Level)
	ring := NewRing(o.RingSize)
	opts := &slog.HandlerOptions{Level: lv}
	hs := []slog.Handler{
		slog.NewTextHandler(file, opts),
		&ringHandler{ring: ring, level: lv},
	}
	if o.Console {
		hs = append(hs, slog.NewTextHandler(os.Stderr, opts))
	}
	l := slog.New(multiHandler{hs})
	slog.SetDefault(l)
	return &Logger{Logger: l, Ring: ring, Level: lv, closer: file}, nil
}

// SetLevel 运行时调整级别。
func (l *Logger) SetLevel(s string) error {
	lvl, err := ParseLevel(s)
	if err != nil {
		return err
	}
	l.Level.Set(lvl)
	return nil
}

// Close 关闭日志文件。
func (l *Logger) Close() error {
	if l == nil || l.closer == nil {
		return nil
	}
	return l.closer.Close()
}

// ParseLevel 把配置中的级别字符串转成 slog.Level。
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return slog.LevelInfo, fmt.Errorf("未知日志级别 %q", s)
}
