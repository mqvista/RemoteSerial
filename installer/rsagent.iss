; RemoteSerial Agent 安装包（Inno Setup 6）
;
; 编译：ISCC.exe /DAppVersion=0.4.0 installer\rsagent.iss
; build.ps1 -Installer 会自动调用。
;
; 静默批量部署示例（OAuth client secret 入网 + 计划任务 + 防火墙）：
;   rsagent-setup-0.4.0.exe /VERYSILENT /SUPPRESSMSGBOXES /NORESTART ^
;     /AUTHKEY=tskey-client-xxxx /TAGS=tag:serial-agent /HOSTNAME=rs-site1 /TASKS="autostart,firewall"
; 装为服务：/TASKS="service,firewall"

#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif
#ifndef SourceExe
  #define SourceExe "..\dist\rsagent-windows-amd64.exe"
#endif
#define AppName "RemoteSerial Agent"
#define AppExe "rsagent.exe"
#define ServiceName "RemoteSerialAgent"

[Setup]
AppId={{B0C7E1E2-6D0A-4F7C-9E2B-6C0B5A1D2E3F}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher=mqvista
AppPublisherURL=https://github.com/mqvista/RemoteSerial
AppSupportURL=https://github.com/mqvista/RemoteSerial
DefaultDirName={autopf}\RemoteSerial
DefaultGroupName={#AppName}
DisableProgramGroupPage=yes
OutputDir=..\dist
OutputBaseFilename=rsagent-setup-{#AppVersion}
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
PrivilegesRequired=admin
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
SetupIconFile=..\internal\shell\icon.ico
UninstallDisplayIcon={app}\{#AppExe}
UninstallDisplayName={#AppName}
MinVersion=10.0
; 运行中的实例由 [Code] 里的 PrepareToInstall 用 rsagent quit / service stop 结束
CloseApplications=no
RestartApplications=no

[Languages]
Name: "chinesesimplified"; MessagesFile: "ChineseSimplified.isl"
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "autostart"; Description: "开机自启：用户登录后启动托盘程序（计划任务，配合自动登录）"; GroupDescription: "无人值守："; Flags: checkedonce
Name: "service"; Description: "安装为系统服务：开机即运行，不依赖登录（与上一项二选一）"; GroupDescription: "无人值守："; Flags: unchecked
Name: "firewall"; Description: "添加防火墙入站规则（UDP 41641），提高直连成功率"; GroupDescription: "网络："
Name: "desktopicon"; Description: "创建桌面快捷方式"; Flags: unchecked

[Files]
Source: "{#SourceExe}"; DestDir: "{app}"; DestName: "{#AppExe}"; Flags: ignoreversion

[Icons]
Name: "{group}\{#AppName}"; Filename: "{app}\{#AppExe}"
Name: "{group}\卸载 {#AppName}"; Filename: "{uninstallexe}"
Name: "{autodesktop}\{#AppName}"; Filename: "{app}\{#AppExe}"; Tasks: desktopicon

[Run]
; 命令行给了 /AUTHKEY 时先入网；key 只经过命令行，不落盘
Filename: "{app}\{#AppExe}"; Parameters: "provision --auth-key ""{param:AUTHKEY}"" --tags ""{param:TAGS|tag:serial-agent}""{code:HostnameArg}{code:ControlURLArg}"; StatusMsg: "正在加入 tailnet…"; Flags: runhidden waituntilterminated; Check: HasAuthKey
Filename: "{app}\{#AppExe}"; Parameters: "deploy firewall-add"; StatusMsg: "正在添加防火墙规则…"; Flags: runhidden waituntilterminated; Tasks: firewall
Filename: "{app}\{#AppExe}"; Parameters: "deploy task-install"; StatusMsg: "正在注册开机自启任务…"; Flags: runhidden waituntilterminated; Tasks: autostart
; 勾了“服务”，或升级前服务已安装：重新安装服务（复制新程序并启动）
Filename: "{app}\{#AppExe}"; Parameters: "service install"; StatusMsg: "正在安装服务…"; Flags: runhidden waituntilterminated; Check: WantService
Filename: "{app}\{#AppExe}"; Description: "启动 {#AppName}"; Flags: nowait postinstall skipifsilent runasoriginaluser
Filename: "{app}\{#AppExe}"; Flags: nowait runasoriginaluser; Check: WizardSilent and not WantService

[UninstallRun]
Filename: "{app}\{#AppExe}"; Parameters: "quit"; Flags: runhidden waituntilterminated; RunOnceId: "quit"
Filename: "{app}\{#AppExe}"; Parameters: "service uninstall"; Flags: runhidden waituntilterminated; RunOnceId: "service"
Filename: "{app}\{#AppExe}"; Parameters: "deploy task-remove"; Flags: runhidden waituntilterminated; RunOnceId: "task"
Filename: "{app}\{#AppExe}"; Parameters: "deploy firewall-remove"; Flags: runhidden waituntilterminated; RunOnceId: "firewall"

[UninstallDelete]
Type: files; Name: "{app}\{#AppExe}.old"

[Code]
var
  ServiceWasInstalled: Boolean;

function HasAuthKey(): Boolean;
begin
  Result := ExpandConstant('{param:AUTHKEY}') <> '';
end;

function HostnameArg(Param: String): String;
var
  H: String;
begin
  H := ExpandConstant('{param:HOSTNAME}');
  if H <> '' then
    Result := ' --hostname "' + H + '"'
  else
    Result := '';
end;

function ControlURLArg(Param: String): String;
var
  U: String;
begin
  U := ExpandConstant('{param:CONTROLURL}');
  if U <> '' then
    Result := ' --control-url "' + U + '"'
  else
    Result := '';
end;

function ServiceInstalled(): Boolean;
var
  ResultCode: Integer;
begin
  Result := False;
  if Exec(ExpandConstant('{sys}\sc.exe'), 'query {#ServiceName}', '', SW_HIDE, ewWaitUntilTerminated, ResultCode) then
    Result := ResultCode = 0;
end;

function WantService(): Boolean;
begin
  Result := WizardIsTaskSelected('service') or ServiceWasInstalled;
end;

// 安装前结束正在运行的托盘实例和服务，否则程序文件被占用。
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  Exe: String;
  ResultCode: Integer;
begin
  Result := '';
  ServiceWasInstalled := ServiceInstalled();
  Exe := ExpandConstant('{app}\{#AppExe}');
  if FileExists(Exe) then
  begin
    Exec(Exe, 'quit', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
    if ServiceWasInstalled then
      Exec(Exe, 'service stop', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
    Sleep(1500);
  end;
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  DataDir: String;
begin
  if CurUninstallStep = usPostUninstall then
  begin
    DataDir := ExpandConstant('{commonappdata}\RemoteSerial');
    if (not UninstallSilent) and DirExists(DataDir) then
      if MsgBox('是否同时删除数据目录？其中包含 Tailscale 登录状态、配置和日志：' + #13#10 + DataDir,
                mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES then
        DelTree(DataDir, True, True, True);
  end;
end;
