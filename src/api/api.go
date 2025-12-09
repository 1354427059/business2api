package api

import (
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
)

// ChatRequest 聊天请求
type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Tools       []ToolDef `json:"tools,omitempty"`
}

// Message 消息
type Message struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

// ToolDef 工具定义
type ToolDef struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

// FunctionDef 函数定义
type FunctionDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

var (
	StreamChat  func(c *gin.Context, req ChatRequest)
	FixedModels []string
)
type GeminiRequest struct {
	Contents          []GeminiContent          `json:"contents"`
	SystemInstruction *GeminiContent           `json:"systemInstruction,omitempty"`
	GenerationConfig  map[string]interface{}   `json:"generationConfig,omitempty"`
	GeminiTools       []map[string]interface{} `json:"tools,omitempty"`
}

type GeminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []GeminiPart `json:"parts"`
}

type GeminiPart struct {
	Text       string            `json:"text,omitempty"`
	InlineData *GeminiInlineData `json:"inlineData,omitempty"`
}

type GeminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

func HandleGeminiGenerate(c *gin.Context) {
	action := c.Param("action")
	if action == "" {
		c.JSON(400, gin.H{"error": gin.H{"code": 400, "message": "Missing model action", "status": "INVALID_ARGUMENT"}})
		return
	}

	// 去掉开头的 /
	action = strings.TrimPrefix(action, "/")

	// 解析模型名和动作
	var model string
	var isStream bool
	if idx := strings.LastIndex(action, ":"); idx > 0 {
		model = action[:idx]
		actionType := action[idx+1:]
		isStream = actionType == "streamGenerateContent"
	} else {
		model = action
	}

	if model == "" {
		model = FixedModels[0]
	}

	var geminiReq GeminiRequest
	if err := c.ShouldBindJSON(&geminiReq); err != nil {
		c.JSON(400, gin.H{"error": gin.H{"code": 400, "message": err.Error(), "status": "INVALID_ARGUMENT"}})
		return
	}

	var messages []Message

	// 处理systemInstruction
	if geminiReq.SystemInstruction != nil && len(geminiReq.SystemInstruction.Parts) > 0 {
		var sysText string
		for _, part := range geminiReq.SystemInstruction.Parts {
			if part.Text != "" {
				sysText += part.Text
			}
		}
		if sysText != "" {
			messages = append(messages, Message{Role: "system", Content: sysText})
		}
	}
	for _, content := range geminiReq.Contents {
		role := content.Role
		if role == "model" {
			role = "assistant"
		}

		var textParts []string
		var contentParts []interface{}

		for _, part := range content.Parts {
			if part.Text != "" {
				textParts = append(textParts, part.Text)
			}
			if part.InlineData != nil {
				contentParts = append(contentParts, map[string]interface{}{
					"type": "image_url",
					"image_url": map[string]string{
						"url": fmt.Sprintf("data:%s;base64,%s", part.InlineData.MimeType, part.InlineData.Data),
					},
				})
			}
		}

		if len(contentParts) > 0 {
			if len(textParts) > 0 {
				contentParts = append([]interface{}{map[string]interface{}{"type": "text", "text": strings.Join(textParts, "\n")}}, contentParts...)
			}
			messages = append(messages, Message{Role: role, Content: contentParts})
		} else if len(textParts) > 0 {
			messages = append(messages, Message{Role: role, Content: strings.Join(textParts, "\n")})
		}
	}

	// 流式判断：路径中包含streamGenerateContent 或 query参数 alt=sse
	stream := isStream || c.Query("alt") == "sse"
	var tools []ToolDef
	for _, gt := range geminiReq.GeminiTools {
		if funcDecls, ok := gt["functionDeclarations"].([]interface{}); ok {
			for _, fd := range funcDecls {
				if funcMap, ok := fd.(map[string]interface{}); ok {
					name, _ := funcMap["name"].(string)
					desc, _ := funcMap["description"].(string)
					params, _ := funcMap["parameters"].(map[string]interface{})
					tools = append(tools, ToolDef{
						Type: "function",
						Function: FunctionDef{
							Name:        name,
							Description: desc,
							Parameters:  params,
						},
					})
				}
			}
		}
	}

	req := ChatRequest{
		Model:    model,
		Messages: messages,
		Stream:   stream,
		Tools:    tools,
	}

	StreamChat(c, req)
}

type ClaudeRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	System      string    `json:"system,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream"`
	Temperature float64   `json:"temperature,omitempty"`
	Tools       []ToolDef `json:"tools,omitempty"`
}
func HandleClaudeMessages(c *gin.Context) {
	var claudeReq ClaudeRequest
	if err := c.ShouldBindJSON(&claudeReq); err != nil {
		c.JSON(400, gin.H{"type": "error", "error": gin.H{"type": "invalid_request_error", "message": err.Error()}})
		return
	}

	req := ChatRequest{
		Model:       claudeReq.Model,
		Messages:    claudeReq.Messages,
		Stream:      claudeReq.Stream,
		Temperature: claudeReq.Temperature,
		Tools:       claudeReq.Tools,
	}
	if claudeReq.System != "" {
		systemMsg := Message{Role: "system", Content: claudeReq.System}
		req.Messages = append([]Message{systemMsg}, req.Messages...)
	}

	// 保持模型名原样，不做映射
	if req.Model == "" {
		req.Model = FixedModels[0]
	}

	StreamChat(c, req)
}
