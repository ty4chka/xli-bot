// internal/mcp/registry.go (реестр тулов с авто-обнаружением)
package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AutoDiscover находит MCP серверы в папке
func AutoDiscover(mcpDir string) ([]MCPServer, error) {
	var servers []MCPServer

	os.MkdirAll(mcpDir, 0755)

	files, err := os.ReadDir(mcpDir)
	if err != nil {
		return nil, err
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		name := file.Name()
		path := filepath.Join(mcpDir, name)

		// Python MCP серверы
		if strings.HasSuffix(name, "_cli.py") || strings.HasSuffix(name, "_cli") {
			serverName := strings.TrimSuffix(name, filepath.Ext(name))
			serverName = strings.TrimSuffix(serverName, "_cli")

			servers = append(servers, MCPServer{
				Name:    serverName,
				Script:  path,
				Enabled: true,
				IsNPX:   false,
			})
			fmt.Printf("🔍 Discovered MCP server: %s\n", serverName)
		}

		// NPX серверы (package.json)
		if name == "package.json" {
			dir := filepath.Dir(path)
			pkgName := filepath.Base(dir)
			servers = append(servers, MCPServer{
				Name:    pkgName,
				Command: []string{"."},
				Enabled: false, // NPX по умолчанию выключены
				IsNPX:   true,
			})
		}
	}

	return servers, nil
}
