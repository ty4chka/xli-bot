package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type Tool struct {
	Name        string
	Description string
	Server      string
}

type Client struct {
	servers map[string]*ServerConn
	mu      sync.RWMutex
}

type ServerConn struct {
	Name        string
	Script      string
	Command     []string
	Enabled     bool
	IsNPX       bool
	process     *exec.Cmd
	stdin       io.WriteCloser
	stdout      *bufio.Reader
	mu          sync.Mutex
	connected   bool
	lastUsed    time.Time
	idleTimeout time.Duration
}

type MCPServer struct {
	Name    string
	Script  string
	Command []string
	Enabled bool
	IsNPX   bool
}

// Map tool -> server для быстрого routing
var toolToServer = map[string]string{
	"search_code":              "knowledge",
	"analyze_traceback":        "debugger",
	"run_tests":                "auto-tester",
	"fix_test":                 "auto-tester",
	"suggest_command":          "shell-helper",
	"fix_typo":                 "shell-helper",
	"generate_complex_command": "shell-helper",
	"list_dependencies":        "package-monitor",
	"check_vulnerabilities":    "package-monitor",
	"update_dependencies":      "package-monitor",
	"prompt_create":            "prompt-cli",
	"prompt_get":               "prompt-cli",
	"prompt_list":              "prompt-cli",
	"prompt_evaluate":          "prompt-cli",
	"blame_line":               "archaeologist",
	"code_ownership":           "archaeologist",
	"commit_history":           "archaeologist",
	"temporal_coupling":        "archaeologist",
	"analyze_complexity":       "refactor",
	"detect_long_methods":      "refactor",
	"dependency_graph":         "architecture",
	"circular_dependencies":    "architecture",
	"suggest_modules":          "architecture",
	"discover_tests":           "auto-tester",
}

func NewClient() *Client {
	return &Client{
		servers: make(map[string]*ServerConn),
	}
}

func (c *Client) Register(server MCPServer) error {
	conn := &ServerConn{
		Name:        server.Name,
		Script:      server.Script,
		Command:     server.Command,
		Enabled:     server.Enabled,
		IsNPX:       server.IsNPX,
		idleTimeout: 5 * time.Minute,
	}

	c.mu.Lock()
	c.servers[server.Name] = conn
	c.mu.Unlock()

	if server.Enabled {
		fmt.Printf("📦 MCP registered (lazy): %s\n", server.Name)
	}
	return nil
}

func (c *Client) ensureConnected(serverName string) (*ServerConn, error) {
	c.mu.RLock()
	conn, ok := c.servers[serverName]
	c.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("server not found: %s", serverName)
	}

	conn.mu.Lock()
	defer conn.mu.Unlock()

	if conn.connected {
		conn.lastUsed = time.Now()
		return conn, nil
	}

	var cmd *exec.Cmd
	if conn.IsNPX {
		cmd = exec.Command("npx", conn.Command...)
	} else {
		cmd = exec.Command("python3", conn.Script)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	conn.process = cmd
	conn.stdin = stdin
	conn.stdout = bufio.NewReader(stdout)
	conn.connected = true
	conn.lastUsed = time.Now()

	fmt.Printf("✅ MCP connected (lazy): %s\n", serverName)
	return conn, nil
}

func (c *Client) CallTool(ctx context.Context, serverName, toolName string, args map[string]interface{}) (string, error) {
	conn, err := c.ensureConnected(serverName)
	if err != nil {
		return "", err
	}

	conn.mu.Lock()
	defer conn.mu.Unlock()
	conn.lastUsed = time.Now()

	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      time.Now().UnixNano(),
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name":      toolName,
			"arguments": args,
		},
	}

	if err := c.sendRequest(conn, req); err != nil {
		return "", err
	}

	resp, err := c.readResponse(conn)
	if err != nil {
		conn.connected = false
		if newConn, reconnectErr := c.ensureConnected(serverName); reconnectErr == nil {
			newConn.mu.Lock()
			c.sendRequest(newConn, req)
			resp, err = c.readResponse(newConn)
			newConn.mu.Unlock()
			conn = newConn
		}
		if err != nil {
			return "", err
		}
	}

	if result, ok := resp["result"].(map[string]interface{}); ok {
		if content, ok := result["content"].([]interface{}); ok && len(content) > 0 {
			if first, ok := content[0].(map[string]interface{}); ok {
				return getString(first, "text"), nil
			}
		}
	}

	if errObj, ok := resp["error"].(map[string]interface{}); ok {
		return "", fmt.Errorf("MCP error: %v", errObj["message"])
	}

	return "", fmt.Errorf("empty response")
}

// CallToolAuto — ФИКС: routing по имени тулза, не перебор всех серверов
func (c *Client) CallToolAuto(ctx context.Context, toolName string, args map[string]interface{}) (string, error) {
	// Сначала пробуем точный routing
	if serverName, ok := toolToServer[toolName]; ok {
		result, err := c.CallTool(ctx, serverName, toolName, args)
		if err == nil {
			return result, nil
		}
		// Если сервер не найден или тул не поддерживается — fallback
		if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "Unknown") {
			return "", err
		}
	}

	// Fallback: перебираем все enabled серверы
	c.mu.RLock()
	serverNames := make([]string, 0, len(c.servers))
	for name, conn := range c.servers {
		if conn.Enabled {
			serverNames = append(serverNames, name)
		}
	}
	c.mu.RUnlock()

	for _, name := range serverNames {
		result, err := c.CallTool(ctx, name, toolName, args)
		if err == nil {
			return result, nil
		}
		if !strings.Contains(err.Error(), "Unknown") && !strings.Contains(err.Error(), "not found") {
			return "", err
		}
	}

	return "", fmt.Errorf("tool not found in any MCP server: %s", toolName)
}

func (c *Client) ListAllTools() []Tool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var tools []Tool
	for name, conn := range c.servers {
		status := "disabled"
		if conn.Enabled {
			status = "enabled"
		}
		if conn.connected {
			status = "connected"
		}
		tools = append(tools, Tool{
			Name:        name,
			Description: fmt.Sprintf("MCP server (%s)", status),
			Server:      name,
		})
	}
	return tools
}

func (c *Client) GetServerNames() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	names := make([]string, 0, len(c.servers))
	for name := range c.servers {
		names = append(names, name)
	}
	return names
}

func (c *Client) Status() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var sb strings.Builder
	total := len(c.servers)
	connected := 0
	enabled := 0
	for name, conn := range c.servers {
		if conn.Enabled {
			enabled++
		}
		if conn.connected {
			connected++
			sb.WriteString(fmt.Sprintf("  ✅ %s — connected\n", name))
		} else if conn.Enabled {
			sb.WriteString(fmt.Sprintf("  ⏳ %s — ready (lazy)\n", name))
		} else {
			sb.WriteString(fmt.Sprintf("  ❌ %s — disabled\n", name))
		}
	}
	return fmt.Sprintf("MCP servers: %d total | %d enabled | %d connected\n\n%s",
		total, enabled, connected, sb.String())
}

func (c *Client) Disconnect(serverName string) error {
	c.mu.RLock()
	conn, ok := c.servers[serverName]
	c.mu.RUnlock()

	if !ok {
		return nil
	}

	conn.mu.Lock()
	defer conn.mu.Unlock()

	if conn.connected && conn.process != nil {
		conn.process.Process.Kill()
		conn.process.Process.Wait()
		conn.connected = false
	}
	return nil
}

func (c *Client) Toggle(serverName string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	conn, ok := c.servers[serverName]
	if !ok {
		return false, fmt.Errorf("server not found: %s", serverName)
	}

	conn.Enabled = !conn.Enabled

	if !conn.Enabled && conn.connected {
		conn.mu.Lock()
		if conn.process != nil {
			conn.process.Process.Kill()
			conn.process.Process.Wait()
			conn.connected = false
		}
		conn.mu.Unlock()
	}

	return conn.Enabled, nil
}

func (c *Client) CleanupIdle() {
	c.mu.RLock()
	servers := make([]*ServerConn, 0, len(c.servers))
	for _, conn := range c.servers {
		servers = append(servers, conn)
	}
	c.mu.RUnlock()

	for _, conn := range servers {
		conn.mu.Lock()
		if conn.connected && time.Since(conn.lastUsed) > conn.idleTimeout {
			fmt.Printf("💤 Disconnecting idle MCP: %s\n", conn.Name)
			if conn.process != nil {
				conn.process.Process.Kill()
				conn.process.Process.Wait()
			}
			conn.connected = false
		}
		conn.mu.Unlock()
	}
}

func (c *Client) sendRequest(conn *ServerConn, req map[string]interface{}) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(conn.stdin, "%s\n", string(data))
	return err
}

func (c *Client) readResponse(conn *ServerConn) (map[string]interface{}, error) {
	line, err := conn.stdout.ReadString('\n')
	if err != nil {
		return nil, err
	}

	var resp map[string]interface{}
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
