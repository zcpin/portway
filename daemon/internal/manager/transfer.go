package manager

import "github.com/byteporter/ssh-tunnel/internal/config"

func (m *Manager) ExportConfig() (config.ConfigExport, error) { return m.configIO.Export() }
func (m *Manager) PreviewImport(input config.ImportRequest) (config.ImportPreview, error) {
	return m.configIO.PreviewImport(input)
}
func (m *Manager) ListBackups() ([]config.ConfigBackup, error) { return m.configIO.ListBackups() }
func (m *Manager) ReadBackup(name string) (string, error)      { return m.configIO.ReadBackup(name) }

func (m *Manager) ImportConfig(input config.ImportRequest) (config.ConfigBackup, error) {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	backup, err := m.configIO.Import(input)
	if err != nil {
		return config.ConfigBackup{}, err
	}
	if err := m.reloadInternal(); err != nil {
		return config.ConfigBackup{}, err
	}
	if err := m.applyLogLevel(); err != nil {
		return config.ConfigBackup{}, err
	}
	return backup, nil
}
