// 安装前的旧版本卸载。
//
// 背景：项目从「SSH 隧道管理器 / SSH Tunnel Manager」改名为「端口通 / Portway」，
// DefaultDirName 随之从 {localappdata}\Programs\SSH Tunnel Manager 改为
// {localappdata}\Programs\Portway。但 Inno Setup 的 AppId 没变，而 AppId 相同
// 即视为同一产品：覆盖安装时 Inno 会沿用注册表里记录的旧安装路径，于是新版本
// 继续装进旧目录，用户看到的仍是一个叫「SSH Tunnel Manager」的文件夹。
//
// 做法：安装前先静默卸载已安装的旧版本，再让本次安装走全新安装流程。
//
// 为什么不在升级时直接沿用旧目录：用户看到的目录名与产品名不一致，且旧目录里
// 可能同时残留改名前后两套可执行文件（ssh_tunnel_client.exe 与 portway.exe）。
// 先卸载再安装能得到一个干净、名字正确的目录。
//
// 卸载只针对**本项目**：读的是同一个 AppId 的卸载信息，且必须是当前用户
// （HKCU）下登记的。不碰任何其他软件，也不碰用户配置目录 ~/.portway。
//
// 若用户当初选择了「为所有用户安装」（HKLM），这里不会去动它——静默卸载
// 需要管理员权限，而本安装程序是 PrivilegesRequired=lowest。这种情况留给
// Inno 自己的升级逻辑处理。

const UninstallKey = 'Software\Microsoft\Windows\CurrentVersion\Uninstall\{D5E9C2A4-7B31-4F0D-9A6E-3C8B5D1A7F20}_is1';

// 读取指定卸载注册表项下的卸载命令行；查不到时返回空串。
//
// 键名做成参数是为了可测试：测试用假 AppId 走一遍「查不到」的分支，
// 从而不必在测试机上真的调用卸载程序。
function InstalledUninstallCommandFor(const Key: String): String;
var
  Cmd: String;
begin
  Result := '';
  if RegQueryStringValue(HKCU, Key, 'QuietUninstallString', Cmd) then begin
    Result := Trim(Cmd);
    Exit;
  end;
  if RegQueryStringValue(HKCU, Key, 'UninstallString', Cmd) then begin
    Cmd := Trim(Cmd);
    if Cmd <> '' then
      Result := Cmd + ' /VERYSILENT /SUPPRESSMSGBOXES /NORESTART';
  end;
end;

// 读取已安装版本的卸载命令行；未安装时返回空串。
//
// 优先用 QuietUninstallString（自带 /SILENT），它由 Inno 写出，最可靠。
// 没有时退回 UninstallString 并自行补静默参数。
function InstalledUninstallCommand: String;
begin
  Result := InstalledUninstallCommandFor(UninstallKey);
end;

// 读取已安装版本的显示名，仅用于提示文案。
function InstalledDisplayName: String;
begin
  if not RegQueryStringValue(HKCU, UninstallKey, 'DisplayName', Result) then
    Result := '端口通';
end;

// 把卸载命令行拆成可执行文件与参数两部分。
//
// Inno 写出的 UninstallString 形如 "C:\path\unins000.exe"（可能带引号），
// 也可能是不带引号但路径含空格的裸路径。两种都要能处理。
procedure SplitCommand(const CommandLine: String; var Exe, Params: String);
var
  S: String;
  ClosingQuote, SpacePos: Integer;
begin
  S := Trim(CommandLine);
  Exe := '';
  Params := '';
  if S = '' then Exit;

  if S[1] = '"' then begin
    ClosingQuote := Pos('"', Copy(S, 2, Length(S)));
    if ClosingQuote = 0 then Exit;
    Exe := Copy(S, 2, ClosingQuote - 1);
    Params := Trim(Copy(S, ClosingQuote + 2, Length(S)));
    Exit;
  end;

  // 无引号：优先按 .exe 边界切，避免把含空格的路径切坏。
  SpacePos := Pos(' /', S);
  if SpacePos > 0 then begin
    Exe := Copy(S, 1, SpacePos - 1);
    Params := Trim(Copy(S, SpacePos + 1, Length(S)));
  end else
    Exe := S;
end;

// 静默卸载已安装的旧版本。
//
// 返回 True 表示「可以继续安装」——包括本来就没装、以及卸载成功两种情况。
// 返回 False 只在用户主动取消时，此时中止安装。
function UninstallPreviousVersion(): Boolean;
var
  CommandLine, Exe, Params, DisplayName: String;
  ResultCode: Integer;
begin
  Result := True;
  CommandLine := InstalledUninstallCommand;
  if CommandLine = '' then Exit;   // 没装过，直接全新安装

  DisplayName := InstalledDisplayName;
  SplitCommand(CommandLine, Exe, Params);
  if (Exe = '') or (not FileExists(Exe)) then begin
    // 注册表有记录但卸载程序已丢失（用户手删了目录）。这种情况继续安装即可，
    // 本次安装会覆盖注册表记录。
    Log('Previous installation registered but uninstaller missing: ' + Exe);
    Exit;
  end;

  if not Exec(Exe, Params, '', SW_HIDE, ewWaitUntilTerminated, ResultCode) then begin
    if MsgBox('检测到已安装的旧版本（' + DisplayName + '），但自动卸载未能启动。' + #13#10 + #13#10 +
        '请先手动卸载，再重新运行本安装程序。' + #13#10 + #13#10 +
        '选择「是」忽略并继续安装，选择「否」退出安装程序。',
        mbConfirmation, MB_YESNO) = IDYES then Exit;
    Result := False;
    Exit;
  end;

  Log('Previous version uninstaller exited with code ' + IntToStr(ResultCode));
  if ResultCode <> 0 then begin
    // 卸载程序返回非 0 通常意味着用户取消了，或卸载被中断。
    if MsgBox('旧版本（' + DisplayName + '）的卸载程序返回了错误码 ' + IntToStr(ResultCode) + '。' + #13#10 + #13#10 +
        '选择「是」忽略并继续安装，选择「否」退出安装程序。',
        mbConfirmation, MB_YESNO) = IDYES then Exit;
    Result := False;
  end;
end;

// 安装前检查：先卸载已安装的旧版本，再继续安装。
function CheckLegacyInstall(): Boolean;
begin
  Result := UninstallPreviousVersion();
end;
