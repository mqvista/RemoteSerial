#ifndef RSAGENT_WINDOW_DARWIN_H
#define RSAGENT_WINDOW_DARWIN_H

// 均可从任意线程调用，实际工作派发到主队列。
void rsShowWindow(const char *url);
// 再次打开已在运行的 .app 时显示窗口；在应用完成启动后调用。
void rsInstallReopenHandler(const char *url);
// labels 为 \x1f 分隔的窗口标题与菜单文字，顺序见 shell_darwin.go 的 setNativeLabels。
void rsSetLabels(const char *labels);
// 把部署动作的结果（JSON）回传给页面。
void rsDeployDone(int mid, const char *json);

#endif
