//go:build !darwin

package update

import "rsagent/internal/i18n"

var downloadSuffix = exeSuffix()

// BundleOf 只在 macOS 上有意义。
func BundleOf(string) string { return "" }

func pendingBinary(_ string, p Pending) (string, error) { return p.File, nil }

// ReplaceBundle 只在 macOS 上有意义。
func ReplaceBundle(string, string) error { return i18n.New("仅 macOS 支持替换 .app") }

func cleanupOldBundle(string) {}
