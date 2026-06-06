package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

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
	tools       []Tool
	process     *exec.Cmd
	stdin       io.WriteCloser
	stdout      *bufio.Reader
	mu          sync.Mutex
	connected   bool
	lastUsed    time.Time
	idleTimeout time.Duration
}

type Tool struct {
	Name        string
	Description string
	Server      string
}

type MCPServer struct {
	Name    string
	Script  string
	Command []string
	Enabled bool
	IsNPX   bool
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

func (c *Client) Connect(serverName string) error {
	c.mu.RLock()
	conn, ok := c.servers[serverName]
	c.mu.RUnlock()

	if !ok {
		return fmt.Errorf("server not found: %s", serverName)
	}

	conn.mu.Lock()
	defer conn.mu.Unlock()

	if conn.connected {
		conn.lastUsed = time.Now()
		return nil
	}

	var cmd *exec.Cmd
	if conn.IsNPX {
		cmd = exec.Command("npx", conn.Command...)
	} else {
		cmd = exec.Command("python3", conn.Script)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return err
	}

	conn.process = cmd
	conn.stdin = stdin
	conn.stdout = bufio.NewReader(stdout)
	conn.connected = true
	conn.lastUsed = time.Now()

	if err := c.initialize(conn); err != nil {
		conn.process.Process.Kill()
		conn.connected = false
		return err
	}

	tools, err := c.listTools(conn)
	if err != nil {
		return err
	}
	conn.tools = tools

	fmt.Printf("✅ MCP connected: %s (%d tools)\n", serverName, len(tools))
	return nil
}

func (c *Client) initialize(conn *ServerConn) error {
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"clientInfo": map[string]string{
				"name":    "xli-bot",
				"version": "1.0.0",
			},
		},
	}
	return c.sendRequest(conn, req)
}

func (c *Client) listTools(conn *ServerConn) ([]Tool, error) {
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	}

	if err := c.sendRequest(conn, req); err != nil {
		return nil, err
	}

	resp, err := c.readResponse(conn)
	if err != nil {
		return nil, err
	}

	var tools []Tool
	if result, ok := resp["result"].(map[string]interface{}); ok {
		if toolsArr, ok := result["tools"].([]interface{}); ok {
			for _, t := range toolsArr {
				if toolMap, ok := t.(map[string]interface{}); ok {
					tool := Tool{
						Name:        getString(toolMap, "name"),
						Description: getString(toolMap, "description"),
						Server:      conn.Name,
					}
					tools = append(tools, tool)
				}
			}
		}
	}

	return tools, nil
}

func (c *Client) CallTool(ctx context.Context, serverName, toolName string, args map[string]interface{}) (string, error) {
	if err := c.Connect(serverName); err != nil {
		return "", err
	}

	c.mu.RLock()
	conn, ok := c.servers[serverName]
	c.mu.RUnlock()

	if !ok || !conn.connected {
		return "", fmt.Errorf("server not connected: %s", serverName)
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
		if reconnectErr := c.Connect(serverName); reconnectErr == nil {
			c.mu.RLock()
			conn, _ = c.servers[serverName]
			c.mu.RUnlock()
			conn.mu.Lock()
			c.sendRequest(conn, req)
			resp, err = c.readResponse(conn)
			conn.mu.Unlock()
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

func (c *Client) ListTools(serverName string) ([]Tool, error) {
	if err := c.Connect(serverName); err != nil {
		return nil, err
	}

	c.mu.RLock()
	conn, ok := c.servers[serverName]
	c.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("server not found")
	}

	conn.mu.Lock()
	defer conn.mu.Unlock()
	return conn.tools, nil
}

func (c *Client) ListAllTools() []Tool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var all []Tool
	for _, conn := range c.servers {
		if conn.connected {
			conn.mu.Lock()
			all = append(all, conn.tools...)
			conn.mu.Unlock()
		}
	}
	return all
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

func (c *Client) AutoReconnect() {
	c.mu.RLock()
	names := make([]string, 0, len(c.servers))
	for name, conn := range c.servers {
		if conn.Enabled && !conn.connected {
			names = append(names, name)
		}
	}
	c.mu.RUnlock()

	for _, name := range names {
		if err := c.Connect(name); err != nil {
			fmt.Printf("❌ Auto-reconnect failed %s: %v\n", name, err)
		}
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
