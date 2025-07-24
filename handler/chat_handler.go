package handler

import (
	"encoding/json"
	"gemini_polling/model"
	"gemini_polling/service"
	"github.com/gin-gonic/gin"
	"log"
	"net/http"
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
		log.Printf("Error during streaming chat: %v", err)
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
		log.Printf("Error during streaming chat: %v", err)
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
		log.Printf("Error during non-streaming chat: %v", err)
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
		log.Printf("获取模型列表时发生错误: %v", err)
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
