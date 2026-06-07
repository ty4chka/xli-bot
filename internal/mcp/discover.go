package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func AutoDiscover(dir string) ([]MCPServer, error) {
	var servers []MCPServer

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".py") {
			return nil
		}

		name := strings.TrimSuffix(info.Name(), ".py")
		server := MCPServer{
			Name:    name,
			Script:  path,
			Enabled: true,
			IsNPX:   false,
		}

		servers = append(servers, server)
		fmt.Printf("🔍 MCP discovered: %s -> %s\n", name, path)
		return nil
	})

	if err != nil {
		return nil, err
	}

	return servers, nil
}
