// macOS 管理窗口：NSWindow + WKWebView，以及让 Cmd+C/V/Q 等快捷键生效的主菜单。
// 窗口关闭时只隐藏，保留页面状态；可见时显示 Dock 图标，隐藏后回到纯菜单栏应用。

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#include "window_darwin.h"

extern void goShellQuit(void);
extern void goShellDeploy(int id, char *action);

@interface RSWindowController : NSObject <NSWindowDelegate, WKNavigationDelegate, WKUIDelegate, WKScriptMessageHandler>
@property(strong) NSWindow *window;
@property(strong) WKWebView *webView;
@property(strong) NSURL *baseURL;
@property(copy) NSString *title;
@property(copy) NSString *okLabel;
@property(copy) NSString *cancelLabel;
@property(strong) NSURL *reopenURL;
@end

static RSWindowController *controller;

static RSWindowController *sharedController(void) {
  if (controller == nil) {
    controller = [RSWindowController new];
  }
  return controller;
}

@implementation RSWindowController

- (void)showURL:(NSURL *)url {
  if (self.window == nil) {
    self.baseURL = url;
    NSRect rect = NSMakeRect(0, 0, 1100, 720);
    NSWindowStyleMask style = NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
                              NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable;
    NSWindow *w = [[NSWindow alloc] initWithContentRect:rect
                                              styleMask:style
                                                backing:NSBackingStoreBuffered
                                                  defer:NO];
    w.releasedWhenClosed = NO;
    w.title = self.title ?: @"rsagent";
    w.minSize = NSMakeSize(640, 420);
    w.delegate = self;
    if (![w setFrameUsingName:@"rsagent.main"]) {
      [w center];
    }
    [w setFrameAutosaveName:@"rsagent.main"];

    WKWebViewConfiguration *cfg = [WKWebViewConfiguration new];
    // 页面通过 window.webkit.messageHandlers.rsagent 请求需要管理员授权的部署动作
    [cfg.userContentController addScriptMessageHandler:self name:@"rsagent"];
    WKWebView *wv = [[WKWebView alloc] initWithFrame:rect configuration:cfg];
    wv.navigationDelegate = self;
    wv.UIDelegate = self;
    w.contentView = wv;
    self.window = w;
    self.webView = wv;
    [wv loadRequest:[NSURLRequest requestWithURL:url]];
  }
  [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
  [self.window makeKeyAndOrderFront:nil];
  [NSApp activateIgnoringOtherApps:YES];
}

// 只接受本机管理页面发来的 {id, action}，交给 Go 侧执行（会弹系统授权框）。
- (void)userContentController:(WKUserContentController *)ucc didReceiveScriptMessage:(WKScriptMessage *)message {
  NSURL *src = message.frameInfo.request.URL;
  if (src == nil || ![self isLocal:src] || ![message.body isKindOfClass:[NSDictionary class]]) {
    return;
  }
  NSDictionary *body = message.body;
  NSNumber *mid = body[@"id"];
  NSString *action = body[@"action"];
  if (![mid isKindOfClass:[NSNumber class]] || ![action isKindOfClass:[NSString class]]) {
    return;
  }
  goShellDeploy(mid.intValue, (char *)action.UTF8String);
}

// 程序已在运行时再次双击 .app（或从程序坞、启动台、open 打开），系统不会启动第二个实例，
// 而是给本进程发“重新打开”事件：把管理窗口叫出来。
- (void)handleReopen:(NSAppleEventDescriptor *)event withReply:(NSAppleEventDescriptor *)reply {
  if (self.reopenURL != nil) {
    [self showURL:self.reopenURL];
  }
}

- (void)windowWillClose:(NSNotification *)note {
  [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
}

- (void)reload:(id)sender {
  [self.webView reload];
}

- (void)quitApp:(id)sender {
  goShellQuit();
}

// 与 core 同源（同主机同端口）的请求留在窗口里，其余交给系统浏览器。
- (BOOL)isLocal:(NSURL *)url {
  if (![url.scheme hasPrefix:@"http"]) {
    return [url.scheme isEqualToString:@"about"] || [url.scheme isEqualToString:@"blob"] ||
           [url.scheme isEqualToString:@"data"];
  }
  return [url.host isEqualToString:self.baseURL.host] &&
         [url.port isEqualToNumber:self.baseURL.port ?: @80];
}

- (void)webView:(WKWebView *)webView
    decidePolicyForNavigationAction:(WKNavigationAction *)action
                    decisionHandler:(void (^)(WKNavigationActionPolicy))decisionHandler {
  NSURL *url = action.request.URL;
  if (action.targetFrame.isMainFrame && url != nil && ![self isLocal:url]) {
    [[NSWorkspace sharedWorkspace] openURL:url];
    decisionHandler(WKNavigationActionPolicyCancel);
    return;
  }
  decisionHandler(WKNavigationActionPolicyAllow);
}

// target="_blank" / window.open：一律用系统浏览器打开。
- (WKWebView *)webView:(WKWebView *)webView
    createWebViewWithConfiguration:(WKWebViewConfiguration *)configuration
               forNavigationAction:(WKNavigationAction *)action
                    windowFeatures:(WKWindowFeatures *)windowFeatures {
  if (action.request.URL != nil) {
    [[NSWorkspace sharedWorkspace] openURL:action.request.URL];
  }
  return nil;
}

// core 可能还在启动：加载失败就稍后重试。
- (void)retryLater {
  dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(1 * NSEC_PER_SEC)), dispatch_get_main_queue(), ^{
    [self.webView loadRequest:[NSURLRequest requestWithURL:self.baseURL]];
  });
}

- (void)webView:(WKWebView *)webView
    didFailProvisionalNavigation:(WKNavigation *)navigation
                       withError:(NSError *)error {
  if (error.code != NSURLErrorCancelled) {
    [self retryLater];
  }
}

- (void)webView:(WKWebView *)webView
    runJavaScriptAlertPanelWithMessage:(NSString *)message
                      initiatedByFrame:(WKFrameInfo *)frame
                     completionHandler:(void (^)(void))completionHandler {
  NSAlert *a = [NSAlert new];
  a.messageText = message;
  [a addButtonWithTitle:self.okLabel ?: @"OK"];
  [a beginSheetModalForWindow:self.window
            completionHandler:^(NSModalResponse r) {
              completionHandler();
            }];
}

- (void)webView:(WKWebView *)webView
    runJavaScriptConfirmPanelWithMessage:(NSString *)message
                        initiatedByFrame:(WKFrameInfo *)frame
                       completionHandler:(void (^)(BOOL))completionHandler {
  NSAlert *a = [NSAlert new];
  a.messageText = message;
  [a addButtonWithTitle:self.okLabel ?: @"OK"];
  [a addButtonWithTitle:self.cancelLabel ?: @"Cancel"];
  [a beginSheetModalForWindow:self.window
            completionHandler:^(NSModalResponse r) {
              completionHandler(r == NSAlertFirstButtonReturn);
            }];
}

@end

void rsDeployDone(int mid, const char *json) {
  NSString *js = [NSString stringWithFormat:@"window.__rsagentNativeDone && window.__rsagentNativeDone(%d, %s)", mid,
                                            json];
  dispatch_async(dispatch_get_main_queue(), ^{
    [sharedController().webView evaluateJavaScript:js completionHandler:nil];
  });
}

void rsInstallReopenHandler(const char *url) {
  NSURL *u = [NSURL URLWithString:[NSString stringWithUTF8String:url]];
  dispatch_async(dispatch_get_main_queue(), ^{
    RSWindowController *c = sharedController();
    c.reopenURL = u;
    // 必须在应用完成启动后注册，否则会被 AppKit 默认的处理器覆盖
    [[NSAppleEventManager sharedAppleEventManager] setEventHandler:c
                                                       andSelector:@selector(handleReopen:withReply:)
                                                     forEventClass:kCoreEventClass
                                                        andEventID:kAEReopenApplication];
  });
}

void rsShowWindow(const char *url) {
  NSURL *u = [NSURL URLWithString:[NSString stringWithUTF8String:url]];
  dispatch_async(dispatch_get_main_queue(), ^{
    [sharedController() showURL:u];
  });
}

static NSMenuItem *addItem(NSMenu *m, NSString *title, SEL action, NSString *key, id target) {
  NSMenuItem *it = [m addItemWithTitle:title action:action keyEquivalent:key];
  it.target = target;
  return it;
}

void rsSetLabels(const char *labels) {
  NSArray<NSString *> *l = [[NSString stringWithUTF8String:labels] componentsSeparatedByString:@"\x1f"];
  if (l.count < 17) {
    return;
  }
  dispatch_async(dispatch_get_main_queue(), ^{
    RSWindowController *c = sharedController();
    c.title = l[0];
    c.okLabel = l[15];
    c.cancelLabel = l[16];
    if (c.window != nil) {
      c.window.title = l[0];
    }
    NSMenu *bar = [NSMenu new];

    NSMenu *app = [[NSMenu alloc] initWithTitle:@"rsagent"];
    addItem(app, l[1], @selector(hide:), @"h", nil);
    [app addItem:[NSMenuItem separatorItem]];
    addItem(app, l[2], @selector(quitApp:), @"q", c);
    [bar addItemWithTitle:@"" action:nil keyEquivalent:@""].submenu = app;

    NSMenu *edit = [[NSMenu alloc] initWithTitle:l[3]];
    addItem(edit, l[4], @selector(undo:), @"z", nil);
    NSMenuItem *redo = addItem(edit, l[5], @selector(redo:), @"z", nil);
    redo.keyEquivalentModifierMask = NSEventModifierFlagCommand | NSEventModifierFlagShift;
    [edit addItem:[NSMenuItem separatorItem]];
    addItem(edit, l[6], @selector(cut:), @"x", nil);
    addItem(edit, l[7], @selector(copy:), @"c", nil);
    addItem(edit, l[8], @selector(paste:), @"v", nil);
    addItem(edit, l[9], @selector(selectAll:), @"a", nil);
    [bar addItemWithTitle:l[3] action:nil keyEquivalent:@""].submenu = edit;

    NSMenu *view = [[NSMenu alloc] initWithTitle:l[10]];
    addItem(view, l[11], @selector(reload:), @"r", c);
    [bar addItemWithTitle:l[10] action:nil keyEquivalent:@""].submenu = view;

    NSMenu *win = [[NSMenu alloc] initWithTitle:l[12]];
    addItem(win, l[13], @selector(performMiniaturize:), @"m", nil);
    addItem(win, l[14], @selector(performClose:), @"w", nil);
    [bar addItemWithTitle:l[12] action:nil keyEquivalent:@""].submenu = win;
    NSApp.windowsMenu = win;

    NSApp.mainMenu = bar;
  });
}
