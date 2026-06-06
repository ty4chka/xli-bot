package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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

		if strings.HasSuffix(name, "_cli.py") || strings.HasSuffix(name, "_cli") {
			serverName := strings.TrimSuffix(name, filepath.Ext(name))
			serverName = strings.TrimSuffix(serverName, "_cli")

			servers = append(servers, MCPServer{
				Name:    serverName,
				Script:  path,
				Enabled: true,
				IsNPX:   false,
			})
			fmt.Printf("🔍 Discovered MCP: %s
", serverName)
		}
	}

	return servers, nil
}
