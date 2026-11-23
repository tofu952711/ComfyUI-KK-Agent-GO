package provider

import (
	"kkagent/internal/asset"
	"kkagent/internal/model"
	"testing"
)

func TestCustomSystemRulesRemainLiteralInAllProtocols(t *testing.T) {
	assets, _ := asset.New(t.TempDir(), 1<<20)
	client := New(assets, 1<<20)
	rules := "  用户自填系统提示词\n保留空格与换行。\n"
	task := " 本次用户要求\n"
	evidence := "独立素材证据"
	for _, protocol := range []string{"openai-completions", "deepseek-messages", "codez"} {
		t.Run(protocol, func(t *testing.T) {
			req := model.Request{Config: model.ProviderConfig{Protocol: protocol, BaseURL: "http://127.0.0.1:9/v1"}, Rules: rules, Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: task}, {Type: "text", Text: evidence}}}}}
			payload, err := client.payload(req)
			if err != nil {
				t.Fatal(err)
			}
			var system any
			var first map[string]any
			switch protocol {
			case "openai-completions":
				messages := payload["messages"].([]any)
				system = obj(messages[0])["content"]
				first = obj(messages[1])
			case "deepseek-messages":
				system = payload["system"]
				first = obj(payload["messages"].([]any)[0])
			case "codez":
				system = payload["instructions"]
				first = obj(payload["input"].([]any)[0])
			}
			if system != rules {
				t.Fatal("system rules were modified")
			}
			blocks := first["content"].([]any)
			if len(blocks) != 2 || obj(blocks[0])["text"] != task || obj(blocks[1])["text"] != evidence {
				t.Fatal("user task and media evidence were merged or changed")
			}
		})
	}
}
