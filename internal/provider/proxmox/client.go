package proxmox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	DefaultTimeout   = 10 * time.Second
	maxResponseBytes = 4 << 20
)

type Client struct {
	baseURL     *url.URL
	httpClient  *http.Client
	credentials Credentials
}

type Version struct {
	Version string `json:"version"`
	Release string `json:"release"`
	RepoID  string `json:"repoid"`
}

type NodeInfo struct {
	Name      string  `json:"node"`
	Status    string  `json:"status"`
	CPU       float64 `json:"cpu"`
	MaxCPU    int     `json:"maxcpu"`
	Memory    int64   `json:"mem"`
	MaxMemory int64   `json:"maxmem"`
	Uptime    int64   `json:"uptime"`
}

type VMResource struct {
	VMID      int     `json:"vmid"`
	Type      string  `json:"type"`
	Name      string  `json:"name"`
	Node      string  `json:"node"`
	Status    string  `json:"status"`
	CPU       float64 `json:"cpu"`
	MaxCPU    int     `json:"maxcpu"`
	Memory    int64   `json:"mem"`
	MaxMemory int64   `json:"maxmem"`
	Disk      int64   `json:"disk"`
	MaxDisk   int64   `json:"maxdisk"`
	Template  int     `json:"template"`
	Tags      string  `json:"tags"`
}

type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Status     string
	Message    string
}

func (e *APIError) Error() string {
	base := fmt.Sprintf("proxmox API %s %s returned %s", e.Method, e.Path, e.Status)
	if e.Message == "" {
		return base
	}
	return base + ": " + e.Message
}

func NewClient(endpoint string, credentials Credentials, httpClient *http.Client) (*Client, error) {
	baseURL, err := parseEndpoint(endpoint)
	if err != nil {
		return nil, fmt.Errorf("validate endpoint: %w", err)
	}
	if err := credentials.validate(); err != nil {
		return nil, fmt.Errorf("validate credentials: %w", err)
	}

	client := http.Client{Timeout: DefaultTimeout}
	if httpClient != nil {
		client = *httpClient
		if client.Timeout <= 0 {
			client.Timeout = DefaultTimeout
		}
	}

	return &Client{
		baseURL:     baseURL,
		httpClient:  &client,
		credentials: credentials,
	}, nil
}

func (c *Client) Version(ctx context.Context) (Version, error) {
	return get[Version](ctx, c, "/version", nil)
}

func (c *Client) Nodes(ctx context.Context) ([]NodeInfo, error) {
	return get[[]NodeInfo](ctx, c, "/nodes", nil)
}

func (c *Client) VMResources(ctx context.Context) ([]VMResource, error) {
	query := url.Values{}
	query.Set("type", "vm")
	return get[[]VMResource](ctx, c, "/cluster/resources", query)
}

func get[T any](ctx context.Context, c *Client, path string, query url.Values) (T, error) {
	var zero T
	if c == nil || c.baseURL == nil || c.httpClient == nil {
		return zero, errors.New("proxmox client is not initialized")
	}

	requestURL := *c.baseURL
	requestURL.Path = strings.TrimRight(requestURL.Path, "/") + "/api2/json" + path
	requestURL.RawPath = ""
	requestURL.RawQuery = query.Encode()
	requestURL.Fragment = ""

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return zero, fmt.Errorf("create proxmox request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "PVEAPIToken="+c.credentials.TokenID+"="+c.credentials.TokenSecret)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return zero, fmt.Errorf("execute proxmox request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return zero, fmt.Errorf("read proxmox response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return zero, fmt.Errorf("proxmox response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return zero, &APIError{
			Method:     http.MethodGet,
			Path:       path,
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Message:    redactSecret(apiErrorMessage(body), c.credentials.TokenSecret),
		}
	}

	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return zero, fmt.Errorf("decode proxmox response envelope: %w", err)
	}
	data := bytes.TrimSpace(envelope.Data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return zero, errors.New("decode proxmox response envelope: data is missing")
	}

	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return zero, fmt.Errorf("decode proxmox response data: %w", err)
	}
	return value, nil
}

func apiErrorMessage(body []byte) string {
	var payload struct {
		Message string            `json:"message"`
		Errors  map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	if message := strings.TrimSpace(payload.Message); message != "" {
		return message
	}
	if len(payload.Errors) == 0 {
		return ""
	}

	keys := make([]string, 0, len(payload.Errors))
	for key := range payload.Errors {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+": "+payload.Errors[key])
	}
	return strings.Join(parts, "; ")
}

func redactSecret(message, secret string) string {
	if message == "" || secret == "" {
		return message
	}
	return strings.ReplaceAll(message, secret, "[REDACTED]")
}
