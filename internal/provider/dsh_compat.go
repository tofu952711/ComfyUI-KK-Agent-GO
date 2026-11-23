package provider

import (
	"encoding/json"
	"fmt"
)

// The SDK parses tool arguments and serializes them compactly on replay.
func dshAssistantArguments(message map[string]any) error {
	for _, value := range arr(message["tool_calls"]) {
		function := obj(obj(value)["function"])
		arguments, ok := function["arguments"].(string)
		if !ok {
			continue
		}
		var parsed any
		if err := json.Unmarshal([]byte(arguments), &parsed); err != nil {
			return fmt.Errorf("invalid DSH tool arguments: %w", err)
		}
		raw, err := json.Marshal(parsed)
		if err != nil {
			return err
		}
		function["arguments"] = string(raw)
	}
	return nil
}
