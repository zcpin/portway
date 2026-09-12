[Setup]
AppName=SSH Tunnel autostart matching tests
AppVersion=1.0.0
DefaultDirName={tmp}\ssh-tunnel-autostart-tests
CreateAppDir=no
Uninstallable=no
PrivilegesRequired=lowest
OutputBaseFilename=autostart-cleanup-tests

[Code]
#include "autostart_cleanup.iss"

var
  Failures: String;
  TestCount: Integer;

procedure CheckMatch(const LabelText, CommandLine, InstalledExe: String; Expected: Boolean);
begin
  TestCount := TestCount + 1;
  if StartupCommandTargetsExecutable(CommandLine, InstalledExe) <> Expected then
    Failures := Failures + LabelText + #13#10;
end;

function InitializeSetup(): Boolean;
var
  InstalledExe, ResultFile: String;
begin
  InstalledExe := 'C:\Apps\SSH Tunnel Manager\ssh-tunnel-daemon.exe';
  CheckMatch('quoted current path', '"' + InstalledExe + '" -hide-console', InstalledExe, True);
  CheckMatch('no arguments', '"' + InstalledExe + '"', InstalledExe, True);
  CheckMatch('case insensitive', '"c:\apps\ssh tunnel manager\SSH-TUNNEL-DAEMON.EXE"', InstalledExe, True);
  CheckMatch('forward slashes', '"C:/Apps/SSH Tunnel Manager/ssh-tunnel-daemon.exe"', InstalledExe, True);
  CheckMatch('path normalization', '"C:\Apps\other\..\SSH Tunnel Manager\ssh-tunnel-daemon.exe"', InstalledExe, True);
  CheckMatch('unquoted path', 'C:\Apps\Tunnel\ssh-tunnel-daemon.exe -hide-console', 'C:\Apps\Tunnel\ssh-tunnel-daemon.exe', True);
  CheckMatch('other installation', '"D:\Tools\ssh-tunnel-daemon.exe"', InstalledExe, False);
  CheckMatch('similar directory', '"C:\Apps\SSH Tunnel Manager-old\ssh-tunnel-daemon.exe"', InstalledExe, False);
  CheckMatch('similar executable', '"C:\Apps\SSH Tunnel Manager\ssh-tunnel-daemon.exe.old"', InstalledExe, False);
  CheckMatch('argument mentions current path', 'C:\Tools\other.exe "' + InstalledExe + '"', InstalledExe, False);
  CheckMatch('relative command', 'ssh-tunnel-daemon.exe -hide-console', InstalledExe, False);
  CheckMatch('missing quote', '"' + InstalledExe, InstalledExe, False);
  CheckMatch('invalid quoted suffix', '"' + InstalledExe + '".other', InstalledExe, False);
  CheckMatch('empty command', '', InstalledExe, False);

  ResultFile := ExpandConstant('{param:ResultFile}');
  if Failures = '' then
    SaveStringToFile(ResultFile, 'PASS ' + IntToStr(TestCount), False)
  else
    SaveStringToFile(ResultFile, 'FAIL' + #13#10 + Failures, False);
  { Only pure string/path checks run; returning False prevents installation. }
  Result := False;
end;
