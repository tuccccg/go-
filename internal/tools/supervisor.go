package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type mcpTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	} `json:"input_schema"`
}

// SyncMCPTools refreshes the Go registry from the Python Supervisor's live MCP catalog.
func SyncMCPTools(ctx context.Context, registry *Registry, baseURL, token string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/internal/mcp/tools", nil)
	if err != nil {
		return err
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("supervisor returned HTTP %d", response.StatusCode)
	}
	var catalog []mcpTool
	if err := json.NewDecoder(response.Body).Decode(&catalog); err != nil {
		return err
	}
	registry.RemovePrefix("mcp_")
	for _, item := range catalog {
		allowed := make(map[string]bool, len(item.InputSchema.Properties))
		for key := range item.InputSchema.Properties {
			allowed[key] = true
		}
		name := item.Name
		registry.Register(Definition{Name: name, Description: item.Description, Required: item.InputSchema.Required, Allowed: allowed,
			Run: func(_ map[string]any, input map[string]any) (any, error) {
				return callSupervisorTool(context.Background(), baseURL, token, name, input)
			},
		})
	}
	return nil
}

func callSupervisorTool(ctx context.Context, baseURL, token, name string, input map[string]any) (any, error) {
	body, err := json.Marshal(map[string]any{"name": name, "arguments": input})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/internal/mcp/tools/call", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := (&http.Client{Timeout: 45 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("supervisor tool call returned HTTP %d", response.StatusCode)
	}
	var result struct {
		Result any `json:"result"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Result, nil
}
