package handler

import (
	"encoding/json"
	"gemini_polling/logger"
	"gemini_polling/model"
	"gemini_polling/service"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
)

type ChatHandler struct {
	genaiService *service.GenAIService
}

func NewChatHandler(s *service.GenAIService) *ChatHandler {
	return &ChatHandler{genaiService: s}
}

// HandleChatCompletions 是一个新的、统一的handler，取代了旧的 ChatStream
func (h *ChatHandler) HandleChatCompletions(c *gin.Context) {
	var req model.ChatCompletionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.OpenAIErrorResponse{
			Error: model.ErrorDetail{
				Message: err.Error(),
				Type:    "invalid_request_error",
			},
		})
		return
	}
	// 根据请求中的 stream 参数决定处理逻辑
	if req.Stream {
		h.handleStream(c, &req)
	} else {
		h.handleNonStream(c, &req)
	}
}

func (h *ChatHandler) ChatStream(c *gin.Context) {
	var req model.ChatCompletionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 强制流式输出
	if !req.Stream {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Please use stream=true for this endpoint."})
		return
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("Access-Control-Allow-Origin", "*") // 可选，用于CORS

	// 将请求上下文、响应写入器和请求体传递给服务层
	err := h.genaiService.StreamChat(c.Request.Context(), c.Writer, &req)
	if err != nil {
		// 如果流已经开始，就不能再写JSON错误了
		// 只能通过日志记录错误，因为HTTP头已经发送
		logger.Error("Error during streaming chat: %v", err)
		// 可以在流中发送一个错误消息
		c.SSEvent("error", gin.H{"message": err.Error()})
	}
}

// Model 定义了 OpenAI 兼容的模型结构体
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ModelListResponse 定义了模型列表的响应体
type ModelListResponse struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

// handleStream 处理流式请求 (这是你原来的 ChatStream 函数逻辑)
func (h *ChatHandler) handleStream(c *gin.Context, req *model.ChatCompletionRequest) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
	err := h.genaiService.StreamChat(c.Request.Context(), c.Writer, req)
	if err != nil {
		logger.Error("Error during streaming chat: %v", err)
		// 如果流已经开始，无法发送JSON错误。
		// 可以在流中发送一个错误事件，但注意这并非标准OpenAI行为。
		// OpenAI标准做法是在流的某个chunk中包含error字段。
		// 由于我们是直接转发，这里发送一个SSE错误事件是一个合理的妥协。
		errorMsg, _ := json.Marshal(model.OpenAIErrorResponse{
			Error: model.ErrorDetail{
				Message: err.Error(),
				Type:    "api_error",
			},
		})
		c.SSEvent("error", string(errorMsg))
	}
}

// handleNonStream 处理非流式请求
func (h *ChatHandler) handleNonStream(c *gin.Context, req *model.ChatCompletionRequest) {
	response, err := h.genaiService.NonStreamChat(c.Request.Context(), req)
	if err != nil {
		logger.Error("Error during non-streaming chat: %v", err)
		c.JSON(http.StatusInternalServerError, model.OpenAIErrorResponse{
			Error: model.ErrorDetail{
				Message: err.Error(),
				Type:    "api_error",
				Code:    "service_unavailable",
			},
		})
		return
	}

	// 根据响应类型返回不同的结果
	switch res := response.(type) {
	case *model.OpenAICompletionResponse:
		c.JSON(http.StatusOK, res)
	case *model.OpenAIErrorResponse:
		// 这里可以根据上游返回的错误类型来决定HTTP状态码，为简化暂用500
		c.JSON(http.StatusInternalServerError, res)
	default:
		// 未知响应类型
		c.JSON(http.StatusInternalServerError, model.OpenAIErrorResponse{
			Error: model.ErrorDetail{
				Message: "An unexpected error occurred and the response format is unknown.",
				Type:    "api_error",
			},
		})
	}
}

// ListModels 从 Google API 获取并返回支持的模型列表
func (h *ChatHandler) ListModels(c *gin.Context) {
	// 调用服务层来获取模型列表
	body, statusCode, err := h.genaiService.ListOpenAICompatibleModels(c.Request.Context())
	if err != nil {
		// 如果服务层返回错误，记录日志并向客户端返回错误信息
		logger.Error("获取模型列表时发生错误: %v", err)
		// 使用服务层返回的状态码，或者如果它不可用，则使用500
		if statusCode == 0 {
			statusCode = http.StatusInternalServerError
		}
		c.JSON(statusCode, gin.H{"error": "Failed to fetch models from upstream: " + err.Error()})
		return
	}
	// 直接将从 Google API 收到的响应体和状态码转发给客户端
	// 设置正确的 Content-Type
	c.Data(statusCode, "application/json; charset=utf-8", body)
}

// ListModels 从 Google API 获取并返回支持的模型列表(Gemini Api 格式)
func (h *ChatHandler) ListModels2(c *gin.Context) {

	queryParams := c.Request.URL.Query()
	// 调用服务层来获取模型列表
	body, statusCode, err := h.genaiService.ListGeminiCompatibleModels(c.Request.Context(), queryParams)
	if err != nil {
		// 如果服务层返回错误，记录日志并向客户端返回错误信息
		logger.Error("获取模型列表时发生错误: %v", err)
		// 使用服务层返回的状态码，或者如果它不可用，则使用500
		if statusCode == 0 {
			statusCode = http.StatusInternalServerError
		}
		c.JSON(statusCode, gin.H{"error": "Failed to fetch models from upstream: " + err.Error()})
		return
	}
	// 直接将从 Google API 收到的响应体和状态码转发给客户端
	// 设置正确的 Content-Type
	c.Data(statusCode, "application/json; charset=utf-8", body)
}

// +++ 新增: 统一处理 Gemini 原生 API 请求的 Handler +++
func (h *ChatHandler) HandleGeminiAction(c *gin.Context) {
	// 从 catch-all 参数中获取路径，并去除开头的'/'
	fullPath := strings.TrimPrefix(c.Param("model_and_action"), "/")

	// 按 ':' 分割来获取模型和动作
	parts := strings.SplitN(fullPath, ":", 2)
	if len(parts) != 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid URL format. Expected 'model:action'."})
		return
	}
	modelName := parts[0]
	action := parts[1]

	// 读取请求体
	requestBody, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read request body: " + err.Error()})
		return
	}

	// 根据动作分发
	switch action {
	case "generateContent":
		h.proxyGeminiGenerateContent(c, modelName, requestBody)
	case "streamGenerateContent":
		h.proxyGeminiStreamGenerateContent(c, modelName, requestBody)
		// +++ 新增 case +++
	case "countTokens":
		h.proxyGeminiCountTokens(c, modelName, requestBody)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unsupported action: " + action})
	}
}

// proxyGeminiGenerateContent 是 HandleGeminiAction 的一个辅助函数，处理非流式代理
func (h *ChatHandler) proxyGeminiGenerateContent(c *gin.Context, modelName string, requestBody []byte) {
	respBody, statusCode, err := h.genaiService.GenerateContent(c.Request.Context(), modelName, requestBody)
	if err != nil {
		logger.Error("Error proxying GenerateContent for model %s: %v", modelName, err)
		if statusCode == 0 {
			statusCode = http.StatusServiceUnavailable
		}
		// 尝试解析上游错误，如果不行就返回通用错误
		var upstreamError map[string]interface{}
		if json.Unmarshal(respBody, &upstreamError) == nil {
			c.JSON(statusCode, upstreamError)
		} else {
			c.JSON(statusCode, gin.H{"error": "Upstream API error: " + err.Error()})
		}
		return
	}
	c.Data(statusCode, "application/json; charset=utf-8", respBody)
}

// proxyGeminiStreamGenerateContent 是 HandleGeminiAction 的一个辅助函数，处理流式代理
func (h *ChatHandler) proxyGeminiStreamGenerateContent(c *gin.Context, modelName string, requestBody []byte) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("Access-Control-Allow-Origin", "*")

	err := h.genaiService.StreamGenerateContent(c.Request.Context(), c.Writer, modelName, requestBody)
	if err != nil {
		logger.Error("Error proxying StreamGenerateContent for model %s: %v", modelName, err)
	}
}

// +++ 新增: 代理 Gemini countTokens 请求的辅助函数 +++
func (h *ChatHandler) proxyGeminiCountTokens(c *gin.Context, modelName string, requestBody []byte) {
	respBody, statusCode, err := h.genaiService.CountTokens(c.Request.Context(), modelName, requestBody)
	if err != nil {
		logger.Error("Error proxying CountTokens for model %s: %v", modelName, err)
		if statusCode == 0 {
			statusCode = http.StatusServiceUnavailable
		}
		// 尝试解析上游错误，如果不行就返回通用错误
		var upstreamError map[string]interface{}
		if json.Unmarshal(respBody, &upstreamError) == nil {
			c.JSON(statusCode, upstreamError)
		} else {
			c.JSON(statusCode, gin.H{"error": "Upstream API error: " + err.Error()})
		}
		return
	}
	c.Data(statusCode, "application/json; charset=utf-8", respBody)
}
