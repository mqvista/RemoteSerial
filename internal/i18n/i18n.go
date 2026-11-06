// Package i18n 提供后端的双语（English / 正體中文）文字。
//
// 做法类似 gettext：代码里直接写简体中文原文，作为查表的键，catalog.go 给出英文与正體中文译文。
// 原文本身不会显示给用户。i18n_test.go 会扫描全部调用点，缺译文时测试失败。
//
// 错误与状态提示用 Errorf / M 生成“惰性”文本：只记录原文和参数，到 Error() / String()
// 时才按当前语言翻译，所以切换语言后已有的提示也会跟着变。日志在写入时翻译，之后不变。
package i18n

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

// Lang 是界面语言。
type Lang string

const (
	EN Lang = "en"
	TW Lang = "zh-TW"
)

var (
	cur       atomic.Value // Lang
	listeners struct {
		sync.Mutex
		fns []func(Lang)
	}
)

func init() { cur.Store(EN) }

// Normalize 把各种写法归一成 EN 或 TW；空串或无法识别时返回空串。
// 只有两种语言，所以所有中文变体都归到正體中文。
func Normalize(s string) Lang {
	s = strings.ToLower(strings.TrimSpace(s))
	switch {
	case s == "":
		return ""
	case strings.HasPrefix(s, "zh"):
		return TW
	case strings.HasPrefix(s, "en"):
		return EN
	}
	return ""
}

// Resolve 决定实际使用的语言：配置值优先，其次环境变量 RSAGENT_LANG，最后跟随系统。
func Resolve(configured string) Lang {
	if l := Normalize(configured); l != "" {
		return l
	}
	if l := Normalize(os.Getenv("RSAGENT_LANG")); l != "" {
		return l
	}
	if l := Normalize(systemLanguage()); l != "" {
		return l
	}
	return EN
}

// Current 返回当前语言。
func Current() Lang { return cur.Load().(Lang) }

// SetLang 切换当前语言，变化时通知监听者。
func SetLang(l Lang) {
	if l != EN && l != TW {
		l = EN
	}
	if old := cur.Swap(l); old == l {
		return
	}
	listeners.Lock()
	fns := append([]func(Lang){}, listeners.fns...)
	listeners.Unlock()
	for _, fn := range fns {
		fn(l)
	}
}

// OnChange 注册语言变化回调（如托盘菜单重新设置文字）。
func OnChange(fn func(Lang)) {
	listeners.Lock()
	listeners.fns = append(listeners.fns, fn)
	listeners.Unlock()
}

// T 翻译原文；表里没有时原样返回。
func T(src string) string {
	e, ok := catalog[src]
	if !ok {
		return src
	}
	if Current() == TW {
		return e[1]
	}
	return e[0]
}

// Sprintf 先翻译格式串再格式化。
func Sprintf(src string, args ...any) string { return fmt.Sprintf(T(src), args...) }

// Text 是到使用时才翻译的文字。
type Text interface{ String() string }

type msg struct {
	src  string
	args []any
}

func (m msg) String() string {
	if len(m.args) == 0 {
		return T(m.src)
	}
	return fmt.Sprintf(T(m.src), m.args...)
}

// M 生成惰性文字。
func M(src string, args ...any) Text { return msg{src, args} }

type raw string

func (r raw) String() string { return string(r) }

// Raw 包装不需要翻译的文字（如 Tailscale 后端返回的英文错误）。
func Raw(s string) Text { return raw(s) }

type errText struct{ err error }

func (e errText) String() string { return e.err.Error() }

// ErrText 把错误当作惰性文字使用；err 为 nil 时返回 nil。
func ErrText(err error) Text {
	if err == nil {
		return nil
	}
	return errText{err}
}

// String 安全地取 Text 的内容，nil 为空串。
func String(t Text) string {
	if t == nil {
		return ""
	}
	return t.String()
}

// Error 是惰性翻译的错误，支持 %w 包装。
type Error struct {
	src  string
	args []any
}

// Errorf 相当于 fmt.Errorf，但格式串在 Error() 时才翻译。
func Errorf(src string, args ...any) error { return &Error{src, args} }

// New 相当于 errors.New，消息在 Error() 时才翻译。
func New(src string) error { return &Error{src: src} }

func (e *Error) Error() string {
	if len(e.args) == 0 {
		return T(e.src)
	}
	return fmt.Errorf(T(e.src), e.args...).Error()
}

func (e *Error) String() string { return e.Error() }

// Unwrap 返回 %w 包装的错误，供 errors.Is / errors.As 使用。
func (e *Error) Unwrap() []error {
	if !strings.Contains(e.src, "%w") {
		return nil
	}
	var out []error
	for _, a := range e.args {
		if err, ok := a.(error); ok {
			out = append(out, err)
		}
	}
	return out
}
