package app

import "fmt"

type BatchResult struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// BatchTunnels is idempotent per entry and reports failures individually.
func (a *App) BatchTunnels(action string, names []string) ([]BatchResult, error) {
	if action != "start" && action != "stop" {
		return nil, fmt.Errorf("action must be start or stop")
	}
	if len(names) == 0 || len(names) > 1000 {
		return nil, fmt.Errorf("select between 1 and 1000 tunnels")
	}
	for _, name := range names {
		if name == "" {
			return nil, fmt.Errorf("tunnel name is required")
		}
	}
	results := make([]BatchResult, 0, len(names))
	seen := make(map[string]bool)
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		running, exists := a.GetStatus()[name]
		var err error
		switch {
		case !exists:
			err = fmt.Errorf("tunnel %s not found", name)
		case action == "start" && !running:
			err = a.StartTunnel(name)
		case action == "stop" && running:
			err = a.StopTunnel(name)
		}
		result := BatchResult{Name: name, OK: err == nil}
		if err != nil {
			result.Error = err.Error()
		}
		results = append(results, result)
	}
	return results, nil
}
