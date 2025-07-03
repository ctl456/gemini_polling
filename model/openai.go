package model

// OpenAI请求和响应结构体
// ChatCompletionRequest 是 /v1/chat/completions 的请求体
type ChatCompletionRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
	// 可以添加其他OpenAI支持的字段，如 Temperature, TopP 等，
	// 只要Google的OpenAI兼容API支持，它们就会被原样转发。
	MaxTokens   int     `json:"max_tokens,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
	TopP        float64 `json:"top_p,omitempty"`
}

// Message 代表对话中的一条消息
// Content 字段被定义为 interface{} 以同时支持
// 纯文本 (string) 和多模态内容 (array)
type Message struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

// OpenAI流式响应的结构
type ChatCompletionStreamResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
}

type Choice struct {
	Index        int         `json:"index"`
	Delta        Delta       `json:"delta"`
	FinishReason interface{} `json:"finish_reason"` // can be string or null
}

type Delta struct {
	Content string `json:"content,omitempty"`
}
