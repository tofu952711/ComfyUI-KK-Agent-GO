package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"kkagent/internal/model"
	"kkagent/internal/tool"
	"sort"
	"strings"
)

const dshPrefix = "mcp__video__"

func dshDefinitions(source []model.ToolDefinition) []model.ToolDefinition {
	out := make([]model.ToolDefinition, 0, len(source)+3)
	for _, definition := range source {
		definition.Name = dshPrefix + definition.Name
		out = append(out, definition)
	}
	for _, name := range []string{"list_mcp_resource_templates", "list_mcp_resources", "read_mcp_resource"} {
		properties := map[string]any{"server": map[string]any{"type": "string", "description": "Configured MCP server name."}}
		required := []string{"server"}
		description := "List resources available from an MCP server."
		if name == "read_mcp_resource" {
			properties["uri"] = map[string]any{"type": "string", "description": "Resource URI to read."}
			required = append(required, "uri")
			description = "Read an MCP resource by URI from the named server. Use a listed URI or an expanded resource template."
		} else {
			properties["cursor"] = map[string]any{"type": "string", "description": "Continuation cursor returned by this server."}
			if name == "list_mcp_resource_templates" {
				description = "List parameterized resource URI templates from an MCP server."
			}
		}
		schema, _ := json.Marshal(map[string]any{"type": "object", "properties": properties, "required": required})
		out = append(out, model.ToolDefinition{Name: name, Description: description, Schema: schema})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (e *Engine) dshTool(ctx context.Context, scope model.Scope, call model.ToolCall, definitions []model.ToolDefinition) (model.ToolResult, error) {
	if strings.HasPrefix(call.Name, dshPrefix) {
		call.Name = strings.TrimPrefix(call.Name, dshPrefix)
		return e.tools.Execute(ctx, scope, call)
	}
	for _, definition := range definitions {
		if definition.Name == call.Name {
			if err := tool.Validate(definition.Schema, call.Arguments); err != nil {
				return dshToolError(err.Error()), nil
			}
			break
		}
	}
	var arguments map[string]any
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		return dshToolError("Invalid tool arguments"), nil
	}
	if arguments["server"] != "video" {
		return dshToolError("Unknown MCP server"), nil
	}
	delete(arguments, "server")
	method := map[string]string{"list_mcp_resource_templates": "resources/templates/list", "list_mcp_resources": "resources/list", "read_mcp_resource": "resources/read"}[call.Name]
	caller, ok := e.tools.(interface {
		Resource(context.Context, model.Scope, string, map[string]any) (json.RawMessage, error)
	})
	if !ok || method == "" {
		return dshToolError("MCP resources unavailable"), nil
	}
	raw, err := caller.Resource(ctx, scope, method, arguments)
	if err != nil {
		return dshToolError("Error: " + err.Error()), nil
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		return dshToolError("Error: Invalid MCP resource result"), nil
	}
	key := map[string]string{"resources/list": "resources", "resources/templates/list": "resourceTemplates", "resources/read": "contents"}[method]
	if len(value[key]) == 0 {
		if method == "resources/read" {
			return dshToolError("Error: Invalid result for resources/read: [\n  {\n    \"expected\": \"array\",\n    \"code\": \"invalid_type\",\n    \"path\": [\n      \"contents\"\n    ],\n    \"message\": \"Invalid input: expected array, received undefined\"\n  }\n]"), nil
		}
		value[key] = json.RawMessage(`[]`)
	}
	normalized := map[string]json.RawMessage{key: value[key]}
	if next, ok := value["nextCursor"]; ok {
		normalized["nextCursor"] = next
	}
	raw, err = json.Marshal(normalized)
	if err != nil {
		return model.ToolResult{}, err
	}
	return model.ToolResult{Content: []model.Block{{Type: "text", Text: "MCP server: video\n" + string(raw)}}}, nil
}

func dshToolError(message string) model.ToolResult {
	return model.ToolResult{IsError: true, Content: []model.Block{{Type: "text", Text: message}}}
}

func (e *Engine) dshToolBlocks(blocks []model.Block) ([]model.Block, error) {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "text":
			parts = append(parts, block.Text)
		case "image":
			description, err := e.assets.DSHAttachment(block)
			if err != nil {
				return nil, fmt.Errorf("describe DSH tool attachment: %w", err)
			}
			parts = append(parts, description)
		}
	}
	return []model.Block{{Type: "text", Text: strings.Join(parts, "\n")}}, nil
}
