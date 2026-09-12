package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestImportPreviewMergeAndBackupRestore(t *testing.T) {
	cio, path := configIOFixture(t)
	before := cio.GetConfig()
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	input := ImportRequest{Mode: "merge", Content: `log_level = "debug"
max_reconnect_attempts = 0
[[tunnels]]
name = "first"
group = "imported"
auto_start = false
local_port = 15001
remote_host = "127.0.0.1"
remote_port = 6432
ssh_connection = "used"
[[tunnels]]
name = "third"
local_port = 15003
remote_host = "127.0.0.1"
remote_port = 3306
ssh_connection = "used"
`}
	preview, err := cio.PreviewImport(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Changes) != 3 || preview.Revision == "" {
		t.Fatalf("preview: %+v", preview)
	}
	assertConfigUnchanged(t, cio, path, before, disk)
	input.Revision = preview.Revision
	backup, err := cio.Import(input)
	if err != nil {
		t.Fatal(err)
	}
	if got := cio.GetConfig(); len(got.Tunnels) != 3 || got.Tunnels[0].AutoStartEnabled() || got.Tunnels[0].Group != "imported" || got.MaxReconnectAttempts != 0 {
		t.Fatalf("merge: %+v", got)
	}
	content, err := cio.ReadBackup(backup.Name)
	if err != nil || content != string(disk) {
		t.Fatalf("backup does not preserve original file: %v", err)
	}
	backups, err := cio.ListBackups()
	if err != nil || len(backups) != 1 || backups[0].Name != backup.Name {
		t.Fatalf("backups: %+v, %v", backups, err)
	}
	restore := ImportRequest{Content: content, Mode: "replace"}
	preview, err = cio.PreviewImport(restore)
	if err != nil {
		t.Fatal(err)
	}
	restore.Revision = preview.Revision
	if _, err := cio.Import(restore); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, cio.GetConfig()) {
		t.Fatal("restore did not recover original fields")
	}
	loaded, err := Load(path)
	if err != nil || !reflect.DeepEqual(before, loaded) {
		t.Fatalf("restored file: %v", err)
	}
}

func TestImportRejectsInvalidInputWithoutMutation(t *testing.T) {
	for _, input := range []ImportRequest{
		{Mode: "unknown", Content: "log_level = 'debug'"},
		{Mode: "merge", Content: "misspelled = true"},
		{Mode: "merge", Content: "not valid TOML"},
		{Mode: "merge", Content: strings.Repeat("x", MaxImportBytes+1)},
		{Mode: "merge", Content: "[[ssh_connections]]\nname='used'\n[[ssh_connections]]\nname='used'\n"},
		{Mode: "replace", Content: "[[tunnels]]\nname='invalid'\n"},
	} {
		cio, path := configIOFixture(t)
		before := cio.GetConfig()
		disk, _ := os.ReadFile(path)
		current, err := cio.Export()
		if err != nil {
			t.Fatal(err)
		}
		input.Revision = current.Revision
		if _, err := cio.PreviewImport(input); err == nil {
			t.Fatal("invalid preview accepted")
		}
		if _, err := cio.Import(input); err == nil {
			t.Fatal("invalid import accepted")
		}
		assertConfigUnchanged(t, cio, path, before, disk)
	}
}

func TestImportDetectsConcurrentAndExternalChanges(t *testing.T) {
	for _, external := range []bool{false, true} {
		cio, path := configIOFixture(t)
		input := ImportRequest{Mode: "merge", Content: "log_level = 'debug'"}
		preview, err := cio.PreviewImport(input)
		if err != nil {
			t.Fatal(err)
		}
		input.Revision = preview.Revision
		if external {
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, err = file.WriteString("\n# edited externally\n")
			file.Close()
			if err != nil {
				t.Fatal(err)
			}
		} else {
			settings := cio.GlobalSettings()
			settings.LogLevel = "warn"
			if err := cio.SetGlobalSettings(settings); err != nil {
				t.Fatal(err)
			}
		}
		before := cio.GetConfig()
		disk, _ := os.ReadFile(path)
		if _, err := cio.Import(input); !errors.Is(err, ErrRevisionChanged) {
			t.Fatalf("stale import: %v", err)
		}
		assertConfigUnchanged(t, cio, path, before, disk)
	}
}

func TestConcurrentImportsAllowOnlyOneRevision(t *testing.T) {
	cio, _ := configIOFixture(t)
	input := ImportRequest{Mode: "merge", Content: "log_level = 'debug'"}
	preview, err := cio.PreviewImport(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Revision = preview.Revision
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := cio.Import(input); results <- err }()
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrRevisionChanged) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
}

func TestFailedBackupOrSavePreservesConfiguration(t *testing.T) {
	for _, blockBackup := range []bool{true, false} {
		cio, path := configIOFixture(t)
		before := cio.GetConfig()
		disk, _ := os.ReadFile(path)
		input := ImportRequest{Mode: "merge", Content: "log_level = 'debug'"}
		preview, err := cio.PreviewImport(input)
		if err != nil {
			t.Fatal(err)
		}
		input.Revision = preview.Revision
		if blockBackup {
			err = os.WriteFile(path+".backups", []byte("blocked"), 0600)
		} else {
			err = os.Mkdir(path+".tmp", 0700)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cio.Import(input); err == nil {
			t.Fatal("expected filesystem failure")
		}
		assertConfigUnchanged(t, cio, path, before, disk)
	}
}

func TestBackupReadRejectsTraversalAndEscapingSymlinks(t *testing.T) {
	cio, path := configIOFixture(t)
	for _, name := range []string{"../config.toml", `C:\secret.toml`, "/secret", "arbitrary.toml"} {
		if _, err := cio.ReadBackup(name); err == nil {
			t.Fatalf("accepted path %s", name)
		}
	}
	dir := path + ".backups"
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.toml")
	if err := os.WriteFile(outside, []byte("secret fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	name := "20260912T000000.000000000-012345abcdef.toml"
	if err := os.Symlink(outside, filepath.Join(dir, name)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := cio.ReadBackup(name); err == nil {
		t.Fatal("backup escaped its directory")
	}
}
