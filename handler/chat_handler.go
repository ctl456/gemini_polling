package handler

import (
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
