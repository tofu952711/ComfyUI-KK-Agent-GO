package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"kkagent/internal/model"
	"log"
)

type payloadStats struct {
	ImageCount   int
	ImageBytes   int64
	RequestBytes int64
	Quality      int
	Converted    int
}

// DSH loopback policy: keep <=1 MiB; larger PNG/JPEG -> JPEG85 4:2:0.
// No aggregate quality ladder, resizing or frame removal.
func (c *Client) requestBytes(ctx context.Context, r model.Request) ([]byte, payloadStats, error) {
	stats := payloadStats{}
	mode := r.Config.ImageTransport
	if mode == "" {
		mode = "dsh"
	}
	if mode != "dsh" && mode != "auto" && mode != "original" {
		return nil, stats, fmt.Errorf("image_transport must be dsh, auto or original")
	}
	wire := r
	wire.Messages = make([]model.Message, len(r.Messages))
	for i, message := range r.Messages {
		copy := message
		copy.Content = make([]model.Block, len(message.Content))
		for j, b := range message.Content {
			if err := ctx.Err(); err != nil {
				return nil, stats, err
			}
			if b.Type == "image" {
				n, err := c.Assets.Size(b)
				if err != nil {
					return nil, stats, err
				}
				stats.ImageCount++
				stats.ImageBytes += n
				if mode != "original" && n > 1<<20 {
					previous := b.Asset
					b, err = c.Assets.JPEGTransmission(b, 85)
					if err != nil {
						return nil, stats, err
					}
					if b.Asset != previous {
						stats.Converted++
						stats.Quality = 85
					}
				}
			}
			copy.Content[j] = b
		}
		wire.Messages[i] = copy
	}
	payload, err := c.payload(wire)
	if err != nil {
		return nil, stats, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, stats, err
	}
	stats.RequestBytes = int64(len(raw))
	limit := c.MaxBytes
	if custom := r.Config.MaxInlineRequestBytes; custom > 0 && custom < limit {
		limit = custom
	}
	if stats.RequestBytes > limit {
		return nil, stats, fmt.Errorf("model request exceeds byte limit: %.2f MiB > %.2f MiB; %d images retained, no resizing or additional quality reduction", float64(len(raw))/(1<<20), float64(limit)/(1<<20), stats.ImageCount)
	}
	log.Printf("image transport: policy=%s images=%d converted=%d source_mib=%.2f request_mib=%.2f jpeg_quality=%d; dimensions/count unchanged", mode, stats.ImageCount, stats.Converted, float64(stats.ImageBytes)/(1<<20), float64(len(raw))/(1<<20), stats.Quality)
	return raw, stats, nil
}
