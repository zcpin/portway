; SSH 隧道管理器 —— Windows 安装程序（Inno Setup 6）
; 用法：ISCC /DAppVersion=1.0.0 scripts\installer.iss
; 由 scripts\build_windows.bat 调用，也可单独运行。

#ifndef AppVersion
  #define AppVersion "1.0.0"
#endif
#ifndef AppBuildName
  #define AppBuildName AppVersion
#endif
#ifndef AppBuildNumber
  #define AppBuildNumber "1"
#endif
#ifndef AppBundleDir
  #define AppBundleDir "..\client\build\windows\x64\runner\Release"
#endif

#define MyAppName "SSH 隧道管理器"
#define MyAppExe "ssh_tunnel_client.exe"
#define MyAppDaemon "ssh-tunnel-daemon.exe"

[Setup]
; GUID 需全局唯一，首次定稿后不要再改
AppId={{D5E9C2A4-7B31-4F0D-9A6E-3C8B5D1A7F20}
AppName={#MyAppName}
AppVersion={#AppVersion}
VersionInfoVersion={#AppBuildName}.{#AppBuildNumber}
AppPublisher=byteporter
DefaultDirName={localappdata}\Programs\SSH Tunnel Manager
DefaultGroupName={#MyAppName}
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
OutputDir=..\dist
OutputBaseFilename=ssh-tunnel-setup-{#AppVersion}
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
ArchitecturesInstallIn64BitMode=x64compatible
UninstallDisplayIcon={app}\{#MyAppExe}
; 安装程序自身图标与快捷方式图标复用客户端的应用图标。
SetupIconFile=..\client\windows\runner\resources\app_icon.ico
; 程序在安装后可写入的只有用户目录（配置 / 发现文件都落在 ~/.ssh-tunnel），
; 因此按用户级安装，无需管理员权限。

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Files]
; 发布目录包含：客户端 exe、Flutter 运行库 data\、daemon exe、示例配置。
Source: "{#AppBundleDir}\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs

[Icons]
Name: "{autoprograms}\{#MyAppName}"; Filename: "{app}\{#MyAppExe}"
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\{#MyAppExe}"; Tasks: desktopicon

[Tasks]
Name: "desktopicon"; Description: "创建桌面快捷方式"; GroupDescription: "附加任务："

[Run]
Filename: "{app}\{#MyAppExe}"; Description: "启动 {#MyAppName}"; Flags: nowait postinstall skipifsilent

[Code]
#include "autostart_cleanup.iss"

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  if CurUninstallStep = usUninstall then
    RemoveInstalledAutostart(ExpandConstant('{app}\{#MyAppDaemon}'));
end;
