package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"kkagent/internal/asset"
	"kkagent/internal/model"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func photoBlock(t *testing.T, a *asset.Store) model.Block {
	t.Helper()
	imageData := image.NewRGBA(image.Rect(0, 0, 900, 700))
	var seed uint32 = 12345
	for y := 0; y < 700; y++ {
		for x := 0; x < 900; x++ {
			seed = 1664525*seed + 1013904223
			noise := uint8(seed >> 24)
			imageData.SetRGBA(x, y, color.RGBA{R: uint8(x/4) + noise/3, G: uint8(y/4) + noise/3, B: noise / 3, A: 255})
		}
	}
	var data bytes.Buffer
	if err := png.Encode(&data, imageData); err != nil {
		t.Fatal(err)
	}
	blocks, err := a.Spool([]model.Block{{Type: "image", MIME: "image/png", Data: base64.StdEncoding.EncodeToString(data.Bytes())}})
	if err != nil {
		t.Fatal(err)
	}
	return blocks[0]
}
func TestLargePayloadCompactsWithoutDroppingImagesOrResizing(t *testing.T) {
	a, _ := asset.New(t.TempDir(), 128<<20)
	b := photoBlock(t, a)
	original, _ := a.Read(b.Asset)
	client := New(a, 128<<20)
	request := model.Request{Config: model.ProviderConfig{Protocol: "openai-completions", MaxInlineRequestBytes: 2 << 20}, Rules: "原文规则", Messages: []model.Message{{Role: "user", Content: []model.Block{{Type: "text", Text: "原文用户要求"}, b, b, b}}}}
	raw, stats, err := client.requestBytes(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(raw)) > 2<<20 || stats.ImageCount != 3 || stats.Quality == 0 {
		t.Fatalf("not compacted: %+v", stats)
	}
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	messages := arr(payload["messages"])
	if obj(messages[0])["content"] != "原文规则" {
		t.Fatal("rules changed")
	}
	blocks := arr(obj(messages[1])["content"])
	if obj(blocks[0])["text"] != "原文用户要求" || len(blocks) != 4 {
		t.Fatal("text or image count changed")
	}
	for _, item := range blocks[1:] {
		uri := str(obj(obj(item)["image_url"])["url"])
		_, data, _ := strings.Cut(uri, ",")
		encoded, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			t.Fatal(err)
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(encoded))
		if err != nil || cfg.Width != 900 || cfg.Height != 700 || format != "jpeg" {
			t.Fatal("lost dimensions or MIME")
		}
	}
	source, _ := a.Read(b.Asset)
	if !bytes.Equal(source, original) {
		t.Fatal("source asset modified")
	}
}
func TestSmallPayloadRetainsOriginalPNGAnd413ExplainsRequestSize(t *testing.T) {
	a, _ := asset.New(t.TempDir(), 128<<20)
	var pngData bytes.Buffer
	_ = png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 16, 16)))
	blocks, _ := a.Spool([]model.Block{{Type: "image", MIME: "image/png", Data: base64.StdEncoding.EncodeToString(pngData.Bytes())}})
	b := blocks[0]
	client := New(a, 128<<20)
	raw, stats, err := client.requestBytes(context.Background(), model.Request{Config: model.ProviderConfig{Protocol: "openai-completions"}, Messages: []model.Message{{Role: "user", Content: []model.Block{b}}}})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Quality != 0 || !bytes.Contains(raw, []byte("image/png")) {
		t.Fatal("unnecessary lossy conversion")
	}
	message := (&HTTPError{Status: 413, RequestBytes: 73 << 20, ImageCount: 29}).Error()
	if !strings.Contains(message, "73.00") || !strings.Contains(message, "29") {
		t.Fatal("missing 413 size diagnosis")
	}
}

func TestRealFailedPayloadOffline(t *testing.T) {
	path := os.Getenv("KK_TEST_FAILED_ASSETS")
	if path == "" {
		t.Skip("set KK_TEST_FAILED_ASSETS for private local-only replay")
	}
	a, _ := asset.New(t.TempDir(), 128<<20)
	files, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	blocks := []model.Block{{Type: "text", Text: strings.Repeat("素材证据", 15000)}}
	for _, file := range files {
		if file.IsDir() || len(file.Name()) != 64 {
			continue
		}
		p := filepath.Join(path, file.Name())
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		_, format, err := image.DecodeConfig(bytes.NewReader(raw))
		if err != nil || format != "png" {
			continue
		}
		id, err := a.Import(p)
		if err != nil {
			t.Fatal(err)
		}
		blocks = append(blocks, model.Block{Type: "image", Asset: id, MIME: "image/png"})
	}
	raw, stats, err := New(a, 128<<20).requestBytes(context.Background(), model.Request{Config: model.ProviderConfig{Protocol: "openai-completions"}, Rules: "规则未改写", Messages: []model.Message{{Role: "user", Content: blocks}}})
	if err != nil {
		t.Fatal(err)
	}
	if stats.ImageCount != 29 || len(raw) > 20<<20 {
		t.Fatalf("unexpected replay size: %+v", stats)
	}
	t.Logf("LOCAL ONLY: images=%d raw=%.2f MiB request=%.2f MiB quality=%d", stats.ImageCount, float64(stats.ImageBytes)/(1<<20), float64(len(raw))/(1<<20), stats.Quality)
	if stats.Converted != 28 || stats.Quality != 85 {
		t.Fatalf("does not match DSH observed conversion policy: %+v", stats)
	}
}

func TestDSHThresholdExactlyOneMiB(t *testing.T) {
	a, _ := asset.New(t.TempDir(), 128<<20)
	client := New(a, 128<<20)
	var encoded bytes.Buffer
	_ = png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 16, 16)))
	for _, size := range []int{1<<20 - 1, 1 << 20, 1<<20 + 1} {
		raw := append([]byte(nil), encoded.Bytes()...)
		raw = append(raw, make([]byte, size-len(raw))...)
		blocks, err := a.Spool([]model.Block{{Type: "image", MIME: "image/png", Data: base64.StdEncoding.EncodeToString(raw)}})
		if err != nil {
			t.Fatal(err)
		}
		_, stats, err := client.requestBytes(context.Background(), model.Request{Config: model.ProviderConfig{Protocol: "openai-completions"}, Messages: []model.Message{{Role: "user", Content: blocks}}})
		if err != nil {
			t.Fatal(err)
		}
		expected := 0
		if size > 1<<20 {
			expected = 1
		}
		if stats.Converted != expected {
			t.Fatalf("size=%d converted=%d", size, stats.Converted)
		}
	}
}
