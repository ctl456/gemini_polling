package model

import "encoding/json"

// =================================================================
// +++ 新增: 函数调用相关的基础结构体 +++
// =================================================================

// FunctionDefinition 定义了一个可供模型调用的函数结构。
type FunctionDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Parameters 是一个JSON Schema对象，用于描述函数参数。
	// 使用 json.RawMessage 可以灵活处理各种复杂的参数结构。
	Parameters json.RawMessage `json:"parameters"`
}

// Tool 定义了一个工具，目前只支持 "function" 类型。
type Tool struct {
	Type     string             `json:"type"` // 必须是 "function"
	Function FunctionDefinition `json:"function"`
}

// FunctionCall 代表模型希望调用的一个具体函数。
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // 参数是一个JSON字符串
}

// ToolCall 代表模型在响应中返回的一个完整的工具调用请求。
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // "function"
	Function FunctionCall `json:"function"`
}

// =================================================================
// *** 修改: 更新现有的请求和响应结构体 ***
// =================================================================

// ChatCompletionRequest 更新了对 'tools' 和 'tool_choice' 的支持
type ChatCompletionRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
	TopP        float64   `json:"top_p,omitempty"`
	// +++ 新增 +++
	Tools      []Tool      `json:"tools,omitempty"`
	ToolChoice interface{} `json:"tool_choice,omitempty"` // 可以是 "none", "auto", 或 {"type": "function", "function": {"name": "my_function"}}
}

// Message 代表对话中的一条消息，增加了对 tool_calls 的支持
type Message struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // 当有 tool_calls 时, content 可以为 null
	// +++ 新增 +++
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // 模型响应中返回
	ToolCallID string     `json:"tool_call_id,omitempty"` // 在 "tool" role 的消息中，指定这是哪个 tool_call 的结果
}

// OpenAICompletionResponse 是非流式调用的标准响应体
type OpenAICompletionResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []CompletionChoice `json:"choices"` // *** 修改 *** (结构体内部修改)
	Usage   Usage              `json:"usage"`
}

// CompletionChoice 代表非流式响应中的一个选项，Message 结构体已更新
type CompletionChoice struct {
	Index   int     `json:"index"`
	Message Message `json:"message"` // *** 修改 *** (Message 结构体已更新，自动支持 tool_calls)
	// *** 修改 *** finish_reason 现在可以是 "tool_calls"
	FinishReason string `json:"finish_reason"`
}

// Usage 包含了本次请求的token使用量统计
// model/openai.go L:82 - 修改后
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"` // 修正
}

// ChatCompletionStreamResponse 是流式响应的结构
type ChatCompletionStreamResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"` // *** 修改 *** (结构体内部修改)
}

// Choice 代表流式响应中的一个片段
type Choice struct {
	Index int   `json:"index"`
	Delta Delta `json:"delta"` // *** 修改 *** (Delta 结构体已更新)
	// *** 修改 *** finish_reason 现在可以是 "tool_calls"
	FinishReason interface{} `json:"finish_reason"` // can be string or null
}

// Delta 代表流中的增量变化，增加了对 tool_calls 的支持
type Delta struct {
	Content string `json:"content,omitempty"`
	// +++ 新增 +++
	ToolCalls []ToolCall `json:"tool_calls,omitempty"` // 流式响应中也可能有 tool_calls
	Role      string     `json:"role,omitempty"`       // role 字段也可能出现在 delta 中
}

// OpenAIErrorResponse 用于向客户端返回符合OpenAI规范的错误信息
type OpenAIErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail 错误详情
type ErrorDetail struct {
	Message string      `json:"message"`
	Type    string      `json:"type"`
	Param   interface{} `json:"param,omitempty"`
	Code    interface{} `json:"code,omitempty"`
}
