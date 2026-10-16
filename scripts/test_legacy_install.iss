// 旧版本卸载逻辑的用例。
//
// 与 test_autostart_cleanup.iss 同一套路：用 CreateAppDir=no 编译一个只跑
// 纯字符串/路径判定的程序，把结果写进文件，由 PowerShell 侧断言。
//
// 这里**不触碰真实注册表、也不真的执行卸载**：
//   - InstalledUninstallCommand / InstalledDisplayName 读注册表的分支在测试机上
//     会自然返回空（HKCU 下没有那个键），因此覆盖的是「未安装」这条路径。
//   - 真正需要覆盖的是 SplitCommand 的解析逻辑——卸载命令行可能带引号、
//     可能不带、路径可能含空格。这部分是纯函数，可以完整测试。
//
// 之所以不给测试注入注册表：Inno Setup 的 RegQueryStringValue 没有可替换的
// 实现，为测试去写真实注册表既危险又要求清理。宁可少覆盖一条分支。

[Setup]
AppName=Portway uninstall tests
AppVersion=1.0.0
DefaultDirName={tmp}\portway-uninstall-tests
CreateAppDir=no
Uninstallable=no
PrivilegesRequired=lowest
OutputBaseFilename=uninstall-tests

[Code]
#include "legacy_install.iss"

var
  Failures: String;
  TestCount: Integer;

procedure Check(const LabelText: String; Actual, Expected: Boolean);
begin
  TestCount := TestCount + 1;
  if Actual <> Expected then
    Failures := Failures + LabelText + #13#10;
end;

procedure CheckString(const LabelText, Actual, Expected: String);
begin
  TestCount := TestCount + 1;
  if CompareText(Actual, Expected) <> 0 then
    Failures := Failures + LabelText + ' (got "' + Actual + '")' + #13#10;
end;

// 断言 SplitCommand 的解析结果。
procedure CheckSplit(const LabelText, CommandLine, ExpectedExe, ExpectedParams: String);
var
  Exe, Params: String;
begin
  SplitCommand(CommandLine, Exe, Params);
  CheckString(LabelText + ' [exe]', Exe, ExpectedExe);
  CheckString(LabelText + ' [params]', Params, ExpectedParams);
end;

function InitializeSetup(): Boolean;
var
  ResultFile, Cmd: String;
begin
  // --- SplitCommand ---
  // Inno 默认写出的形式：带引号，无参数。
  CheckSplit('quoted path without params',
    '"C:\Users\me\AppData\Local\Programs\Portway\unins000.exe"',
    'C:\Users\me\AppData\Local\Programs\Portway\unins000.exe', '');

  // 带静默参数（QuietUninstallString 的常见形式）。
  CheckSplit('quoted path with silent params',
    '"C:\Program Files\Portway\unins000.exe" /SILENT',
    'C:\Program Files\Portway\unins000.exe', '/SILENT');

  // 路径含空格且带多个参数。
  CheckSplit('quoted path with spaces and several params',
    '"C:\Program Files\My App\unins000.exe" /VERYSILENT /SUPPRESSMSGBOXES /NORESTART',
    'C:\Program Files\My App\unins000.exe', '/VERYSILENT /SUPPRESSMSGBOXES /NORESTART');

  // 无引号、路径不含空格。
  CheckSplit('bare path without params',
    'C:\Portway\unins000.exe',
    'C:\Portway\unins000.exe', '');

  // 无引号但带参数：按 " /" 边界切分。
  CheckSplit('bare path with params',
    'C:\Portway\unins000.exe /VERYSILENT',
    'C:\Portway\unins000.exe', '/VERYSILENT');

  // 首尾空白应被裁掉。
  CheckSplit('surrounding whitespace is trimmed',
    '   "C:\Portway\unins000.exe" /SILENT   ',
    'C:\Portway\unins000.exe', '/SILENT');

  // 空命令行不产生可执行文件。
  CheckSplit('empty command line', '', '', '');

  // 只有空白同样视为空。
  CheckSplit('whitespace-only command line', '   ', '', '');

  // 引号不闭合：无法解析，返回空而不是把畸形串当路径。
  CheckSplit('unterminated quote yields empty exe',
    '"C:\Portway\unins000.exe', '', '');

  // --- InstalledUninstallCommand / InstalledDisplayName ---
  // 这两条读的是真实注册表，结果取决于测试机装没装过本产品：
  //   - 没装过：未安装命令返回空、显示名回退到产品名
  //   - 装过（开发机常见）：命令非空、显示名是 Inno 写出的
  //     「<产品名> version <版本号>」
  // 因此不能断言具体值，只能断言「两者自洽」：有命令就必然有显示名，
  // 且显示名以产品名开头。这样在两种机器上都成立。
  Cmd := InstalledUninstallCommand;
  if Cmd = '' then
    CheckString('display name falls back to the product name when not installed',
      InstalledDisplayName, '端口通')
  else
    Check('display name starts with the product name when installed',
      Pos('端口通', InstalledDisplayName) = 1, True);

  // --- UninstallPreviousVersion ---
  // 关键：**绝不能在测试里真的调用它**。测试机若装过本产品，它会去执行
  // 真实的 unins000.exe，把开发机上的安装卸掉。
  //
  // 这里只覆盖「未安装」这条分支：用注册表里不存在的一个假 AppId 走一遍
  // 等价逻辑，确认「查不到卸载命令 → 返回 True 继续安装」。
  // 已安装分支涉及真实卸载，交给发布流程的手工验证，不放进自动化测试。
  Check('no previous install means install may continue',
    InstalledUninstallCommandFor(
      'Software\Microsoft\Windows\CurrentVersion\Uninstall\{00000000-0000-0000-0000-000000000000}_is1') = '',
    True);

  ResultFile := ExpandConstant('{param:ResultFile}');
  if Failures = '' then
    SaveStringToFile(ResultFile, 'PASS ' + IntToStr(TestCount), False)
  else
    SaveStringToFile(ResultFile, 'FAIL' + #13#10 + Failures, False);
  { 纯字符串/路径检查，返回 False 阻止安装。 }
  Result := False;
end;
