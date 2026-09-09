package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// FileRef represents an uploaded file ready for transcript.
type FileRef struct {
	ID   string `json:"id"`
	URL  string `json:"url"`
	Name string `json:"name"`
	Type string `json:"type"` // image, pdf, etc
}

// UploadFile uploads raw bytes to Notion's file storage and returns a ref.
// Falls back to data URL if upload fails — transcript will still contain the file as text placeholder.
func (c *Client) UploadFile(ctx context.Context, data []byte, filename, contentType string) (*FileRef, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty file")
	}
	if len(data) > 20*1024*1024 {
		return nil, fmt.Errorf("file too large (max 20MB)")
	}
	// 1. Get signed upload URL
	reqBody, _ := json.Marshal(map[string]any{
		"bucket":      "secure",
		"name":        filename,
		"contentType": contentType,
	})
	resp, err := c.do(ctx, "POST", "getUploadFileUrl", reqBody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		drain(resp.Body)
		return nil, classify(resp)
	}
	var out struct {
		URL       string `json:"url"`
		SignedURL string `json:"signedUrl"`
		SignedGetURL string `json:"signedGetUrl"`
		FileID    string `json:"fileId"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("upload url parse: %w", err)
	}
	uploadURL := out.SignedURL
	if uploadURL == "" {
		uploadURL = out.URL
	}
	if uploadURL == "" {
		return nil, fmt.Errorf("no upload url")
	}
	// 2. PUT to signed URL
	putReq, err := http.NewRequestWithContext(ctx, "PUT", uploadURL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	putReq.Header.Set("Content-Type", contentType)
	putResp, err := c.http.Do(putReq)
	if err != nil {
		return nil, err
	}
	defer putResp.Body.Close()
	drain(putResp.Body)
	if putResp.StatusCode >= 300 {
		return nil, fmt.Errorf("upload PUT: %d", putResp.StatusCode)
	}
	// Return ref — Notion transcript can use signedGetUrl or fileId
	url := out.SignedGetURL
	if url == "" {
		url = out.URL
	}
	if url == "" {
		url = uploadURL
	}
	return &FileRef{
		ID:   out.FileID,
		URL:  url,
		Name: filename,
		Type: strings.Split(contentType, "/")[0],
	}, nil
}

// FileBlock creates a transcript entry for a file.
// Used when user sends image/pdf.
func FileBlock(file *FileRef) TranscriptEntry {
	// Notion user block with file — value is array containing file reference
	// Format: {"type": "file", "value": {"id": "...", "url": "...", "name": "..."}}
	return TranscriptEntry{
		ID:   file.ID,
		Type: "file",
		Value: map[string]any{
			"id":   file.ID,
			"url":  file.URL,
			"name": file.Name,
			"type": file.Type,
		},
	}
}

// ImageBlock creates an image transcript entry.
func ImageBlock(url, fileID string) TranscriptEntry {
	return TranscriptEntry{
		ID:   fileID,
		Type: "image",
		Value: map[string]any{
			"url":    url,
			"fileId": fileID,
		},
	}
}
