package adminlogs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"business2api/src/logger"
)

type StreamHandlerConfig struct {
	GetRegistrarBaseURL func() string
	HTTPClient          *http.Client
}

type StreamHandler struct {
	getRegistrarBaseURL func() string
	httpClient          *http.Client
}

type streamPayload struct {
	Items []logger.LogEntry `json:"items"`
}

type registrarLogResponse struct {
	Items       []logger.LogEntry `json:"items"`
	NextAfterID int64             `json:"next_after_id"`
	HasMore     bool              `json:"has_more"`
}

func NewStreamHandler(cfg StreamHandlerConfig) *StreamHandler {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &StreamHandler{
		getRegistrarBaseURL: cfg.GetRegistrarBaseURL,
		httpClient:          client,
	}
}

func (h *StreamHandler) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		source := normalizeSource(c.DefaultQuery("source", "all"))
		level := normalizeLevel(c.DefaultQuery("level", "all"))
		bootstrapLimit := clampInt(c.DefaultQuery("bootstrap_limit", "200"), 1, 1000, 200)
		pollMS := clampInt(c.DefaultQuery("poll_ms", "1000"), 500, 10000, 1000)

		writer := c.Writer
		flusher, ok := writer.(http.Flusher)
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "stream unsupported"})
			return
		}

		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("Cache-Control", "no-cache")
		writer.Header().Set("Connection", "keep-alive")
		writer.Header().Set("X-Accel-Buffering", "no")
		writer.WriteHeader(http.StatusOK)
		flusher.Flush()

		ctx := c.Request.Context()
		localAfterID := int64(0)
		registrarAfterID := int64(0)

		bootstrap := make([]logger.LogEntry, 0, bootstrapLimit*2)
		if source == "all" || source == "business2api" {
			local := logger.Recent(bootstrapLimit, "business2api", level)
			if len(local) > 0 {
				localAfterID = local[len(local)-1].ID
				bootstrap = append(bootstrap, local...)
			}
		}
		if source == "all" || source == "registrar" {
			items, nextID, err := h.fetchRegistrarLogs(ctx, 0, bootstrapLimit, level)
			if err != nil {
				h.writeSystemEvent(writer, flusher, "registrar bootstrap error: "+err.Error())
			} else {
				registrarAfterID = nextID
				bootstrap = append(bootstrap, items...)
			}
		}
		if len(bootstrap) > 0 {
			sort.Slice(bootstrap, func(i, j int) bool {
				if bootstrap[i].TS == bootstrap[j].TS {
					return bootstrap[i].ID < bootstrap[j].ID
				}
				return bootstrap[i].TS < bootstrap[j].TS
			})
			h.writeLogsEvent(writer, flusher, bootstrap)
		}

		ticker := time.NewTicker(time.Duration(pollMS) * time.Millisecond)
		pingTicker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		defer pingTicker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-pingTicker.C:
				h.writePingEvent(writer, flusher)
			case <-ticker.C:
				batch := make([]logger.LogEntry, 0, 200)
				if source == "all" || source == "business2api" {
					local, nextID := logger.After(localAfterID, 200, "business2api", level)
					if nextID > localAfterID {
						localAfterID = nextID
					}
					batch = append(batch, local...)
				}
				if source == "all" || source == "registrar" {
					items, nextID, err := h.fetchRegistrarLogs(ctx, registrarAfterID, 200, level)
					if err != nil {
						h.writeSystemEvent(writer, flusher, "registrar pull error: "+err.Error())
					} else {
						if nextID > registrarAfterID {
							registrarAfterID = nextID
						}
						batch = append(batch, items...)
					}
				}
				if len(batch) == 0 {
					continue
				}
				sort.Slice(batch, func(i, j int) bool {
					if batch[i].TS == batch[j].TS {
						return batch[i].ID < batch[j].ID
					}
					return batch[i].TS < batch[j].TS
				})
				h.writeLogsEvent(writer, flusher, batch)
			}
		}
	}
}

func (h *StreamHandler) fetchRegistrarLogs(ctx context.Context, afterID int64, limit int, level string) ([]logger.LogEntry, int64, error) {
	baseURL := ""
	if h.getRegistrarBaseURL != nil {
		baseURL = strings.TrimSpace(h.getRegistrarBaseURL())
	}
	if baseURL == "" {
		return nil, afterID, fmt.Errorf("registrar base url is empty")
	}

	endpoint := strings.TrimRight(baseURL, "/") + "/logs"
	q := url.Values{}
	q.Set("after_id", strconv.FormatInt(afterID, 10))
	q.Set("limit", strconv.Itoa(limit))
	q.Set("level", level)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, afterID, err
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, afterID, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, afterID, fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out registrarLogResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, afterID, err
	}
	if out.NextAfterID < afterID {
		out.NextAfterID = afterID
	}
	for i := range out.Items {
		out.Items[i].Source = "registrar"
	}
	return out.Items, out.NextAfterID, nil
}

func (h *StreamHandler) writeLogsEvent(w io.Writer, flusher http.Flusher, items []logger.LogEntry) {
	if len(items) == 0 {
		return
	}
	payload, _ := json.Marshal(streamPayload{Items: items})
	_, _ = fmt.Fprintf(w, "event: logs\ndata: %s\n\n", payload)
	flusher.Flush()
}

func (h *StreamHandler) writeSystemEvent(w io.Writer, flusher http.Flusher, message string) {
	payload, _ := json.Marshal(map[string]string{"message": message})
	_, _ = fmt.Fprintf(w, "event: system\ndata: %s\n\n", payload)
	flusher.Flush()
}

func (h *StreamHandler) writePingEvent(w io.Writer, flusher http.Flusher) {
	payload, _ := json.Marshal(map[string]string{"ts": time.Now().UTC().Format(time.RFC3339Nano)})
	_, _ = fmt.Fprintf(w, "event: ping\ndata: %s\n\n", payload)
	flusher.Flush()
}

func normalizeSource(source string) string {
	source = strings.ToLower(strings.TrimSpace(source))
	switch source {
	case "business2api", "registrar", "all":
		return source
	default:
		return "all"
	}
}

func normalizeLevel(level string) string {
	level = strings.ToLower(strings.TrimSpace(level))
	switch level {
	case "error", "warn", "info", "debug", "all":
		return level
	default:
		return "all"
	}
}

func clampInt(raw string, min, max, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
