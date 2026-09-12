package app

import "github.com/byteporter/ssh-tunnel/internal/config"

func (a *App) ExportConfig() (config.ConfigExport, error) { return a.mgr.ExportConfig() }
func (a *App) PreviewImport(input config.ImportRequest) (config.ImportPreview, error) {
	return a.mgr.PreviewImport(input)
}
func (a *App) ListBackups() ([]config.ConfigBackup, error) { return a.mgr.ListBackups() }
func (a *App) ReadBackup(name string) (string, error)      { return a.mgr.ReadBackup(name) }

func (a *App) ImportConfig(input config.ImportRequest) (config.ConfigBackup, error) {
	backup, err := a.mgr.ImportConfig(input)
	if err != nil {
		return config.ConfigBackup{}, err
	}
	a.emitSnapshot()
	return backup, nil
}
