package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gemini_polling/config" // 引入 config 包
	"gemini_polling/model"
	"gemini_polling/storage"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// 移除了这里的 const rateLimitCooldown

type GenAIService struct {
	keyStore         *storage.KeyStore
	httpClient       *http.Client
	rateLimitedKeys  map[uint]time.Time
	rateLimitedMutex sync.RWMutex
	configManager    *config.Manager // 持有 Manager 而不是静态配置
}

// NewGenAIService 构造函数现在接收完整的配置
func NewGenAIService(manager *config.Manager, keyStore *storage.KeyStore) *GenAIService {
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	}
	return &GenAIService{
		keyStore:        keyStore,
		httpClient:      &http.Client{Timeout: 5 * time.Minute, Transport: transport},
		rateLimitedKeys: make(map[uint]time.Time),
		configManager:   manager,
	}
}

// isKeyRateLimited 保持不变

func (s *GenAIService) isKeyRateLimited(keyID uint) bool {
	s.rateLimitedMutex.RLock()
	defer s.rateLimitedMutex.RUnlock()

	disabledUntil, found := s.rateLimitedKeys[keyID]
	if found && time.Now().Before(disabledUntil) {
		return true // 仍在冷却期
	}
	return false
}

// temporaryDisableKey 使用动态配置
func (s *GenAIService) temporaryDisableKey(keyID uint, reason string) {
	s.rateLimitedMutex.Lock()
	defer s.rateLimitedMutex.Unlock()

	// 从管理器获取最新的冷却时间
	cooldown := s.configManager.Get().RateLimitCooldown
	s.rateLimitedKeys[keyID] = time.Now().Add(cooldown)
	log.Printf("Key ID %d 收到 429, 临时禁用 %v. 原因: %s", keyID, cooldown, reason)
}

// StreamChat 现在使用配置的最大重试次数
func (s *GenAIService) StreamChat(ctx context.Context, w io.Writer, req *model.ChatCompletionRequest) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming unsupported")
	}

	req.Stream = true

	reqBodyBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("序列化请求体失败: %w", err)
	}

	// 从管理器获取最新的重试次数
	maxRetries := s.configManager.Get().MaxRetries
	var lastErr error

RetryLoop:
	for i := 0; i < maxRetries; i++ {
		// ... (循环内部逻辑保持不变)
		// 1. 从数据库轮询获取一个可用的Key
		activeKey, err := s.keyStore.GetNextActiveKey()
		if err != nil {
			return fmt.Errorf("无法获取可用的 API Key: %w", err)
		}

		// 2. 检查此 Key 是否在内存中被临时禁用 (因429)
		if s.isKeyRateLimited(activeKey.ID) {
			log.Printf("尝试使用 Key ID %d, 但其正在冷却中，跳过...", activeKey.ID)
			lastErr = fmt.Errorf("key ID %d is rate limited", activeKey.ID)
			continue // 尝试下一个key
		}

		log.Printf("第 %d 次尝试, 使用 Key ID: %d, 模型: %s", i+1, activeKey.ID, req.Model)

		// 3. 准备 REST API 请求
		const url = "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"
		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBodyBytes))
		if err != nil {
			lastErr = fmt.Errorf("创建 HTTP 请求失败: %w", err)
			log.Println(lastErr)
			continue
		}

		httpReq.Header.Set("Authorization", "Bearer "+activeKey.Key)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("Cache-Control", "no-cache")
		httpReq.Header.Set("Connection", "keep-alive")

		// 4. 发送请求
		resp, err := s.httpClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("请求 Google API 失败 (Key ID: %d): %w", activeKey.ID, err)
			log.Println(lastErr)
			// 网络层面错误，可以考虑重试
			continue
		}
		defer resp.Body.Close()

		// 5. 处理响应状态码
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			errorMsg := fmt.Sprintf("上游API错误 (HTTP %d): %s", resp.StatusCode, string(body))
			lastErr = errors.New(errorMsg)
			log.Printf("Key ID %d 请求失败: %s", activeKey.ID, errorMsg)

			if resp.StatusCode == http.StatusTooManyRequests { // 429: 临时禁用
				s.temporaryDisableKey(activeKey.ID, errorMsg)
			} else if resp.StatusCode >= 400 && resp.StatusCode < 500 { // 4xx: 永久禁用 (如 key 无效)
				s.keyStore.Disable(activeKey.ID, errorMsg)
			}
			// 对于服务器端错误 (5xx) 或其他错误，直接重试
			continue RetryLoop
		}

		// 6. 成功，开始流式转发
		// Google 的 OpenAI 兼容 API 直接返回 SSE 格式，我们只需将其转发
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.TrimSpace(line) == "" {
				continue
			}

			// 直接将原始数据行写入客户端响应
			_, err := fmt.Fprintf(w, "%s\n\n", line)
			if err != nil {
				log.Printf("写入响应流失败: %v (客户端可能已断开连接)", err)
				return err // 客户端断开，无法继续
			}
			flusher.Flush()

			// 检查是否是流的结束标志
			if strings.HasSuffix(line, "[DONE]") {
				log.Printf("请求处理成功 (Key ID: %d), 流已结束。", activeKey.ID)
				return nil // 成功完成
			}
		}

		if err := scanner.Err(); err != nil {
			log.Printf("读取上游流时发生错误 (Key ID: %d): %v", activeKey.ID, err)
			lastErr = err
			// 这里可能是一个中间断开的流，重试可能是合适的
			continue RetryLoop
		}

		// 如果scanner正常结束但没有收到[DONE], 依然认为是成功
		log.Printf("请求处理成功 (Key ID: %d), 上游流正常关闭。", activeKey.ID)
		return nil
	}

	log.Printf("所有 %d 次重试均失败。", maxRetries)
	if lastErr != nil {
		return fmt.Errorf("所有 API Key 均尝试失败，最后一次错误: %w", lastErr)
	}
	return errors.New("所有 API Key 均尝试失败，但未捕获到具体错误")
}

// =================================================================
// +++ 新增: 处理非流式请求的函数 +++
// =================================================================
// NonStreamChat 处理非流式请求，并返回一个完整的响应体或错误
func (s *GenAIService) NonStreamChat(ctx context.Context, req *model.ChatCompletionRequest) (interface{}, error) {
	req.Stream = false // 确保 stream 标志位为 false
	reqBodyBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("序列化请求体失败: %w", err)
	}
	maxRetries := s.configManager.Get().MaxRetries
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		activeKey, err := s.keyStore.GetNextActiveKey()
		if err != nil {
			return nil, fmt.Errorf("无法获取可用的 API Key: %w", err)
		}
		if s.isKeyRateLimited(activeKey.ID) {
			log.Printf("尝试使用 Key ID %d, 但其正在冷却中，跳过...", activeKey.ID)
			lastErr = fmt.Errorf("key ID %d is rate limited", activeKey.ID)
			continue
		}
		log.Printf("第 %d 次尝试 (非流式), 使用 Key ID: %d, 模型: %s", i+1, activeKey.ID, req.Model)
		const url = "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"
		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBodyBytes))
		if err != nil {
			lastErr = fmt.Errorf("创建 HTTP 请求失败: %w", err)
			continue
		}
		httpReq.Header.Set("Authorization", "Bearer "+activeKey.Key)
		httpReq.Header.Set("Content-Type", "application/json")
		resp, err := s.httpClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("请求 Google API 失败 (Key ID: %d): %w", activeKey.ID, err)
			log.Println(lastErr)
			continue
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			lastErr = fmt.Errorf("读取响应体失败: %w", err)
			log.Println(lastErr)
			continue
		}
		// 如果请求成功，直接将Google返回的OpenAI兼容格式JSON转发
		if resp.StatusCode == http.StatusOK {
			log.Printf("非流式请求成功 (Key ID: %d)", activeKey.ID)
			var successResp model.OpenAICompletionResponse
			if err := json.Unmarshal(body, &successResp); err != nil {
				// 如果Google返回的成功响应不是我们预期的格式，这是一个严重问题
				return nil, fmt.Errorf("解析上游成功响应失败: %w", err)
			}
			return &successResp, nil
		}

		// 如果上游API返回错误
		errorMsg := fmt.Sprintf("上游API错误 (HTTP %d): %s", resp.StatusCode, string(body))
		lastErr = errors.New(errorMsg)
		log.Printf("Key ID %d 请求失败: %s", activeKey.ID, errorMsg)
		if resp.StatusCode == http.StatusTooManyRequests {
			s.temporaryDisableKey(activeKey.ID, errorMsg)
		} else if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			s.keyStore.Disable(activeKey.ID, errorMsg)
		}
		// 其他错误（如5xx）则重试
		// 尝试解析上游的错误信息，并以标准格式返回
		var errResp model.OpenAIErrorResponse
		if json.Unmarshal(body, &errResp) == nil {
			// 如果能解析成标准错误格式，后续重试失败时就用这个错误
			// （注意：Google的错误格式可能不完全匹配，这里是尽力而为）
		}
	}
	log.Printf("所有 %d 次重试均失败。", maxRetries)
	if lastErr != nil {
		return nil, fmt.Errorf("所有 API Key 均尝试失败，最后一次错误: %w", lastErr)
	}
	return nil, errors.New("所有 API Key 均尝试失败，但未捕获到具体错误")
}

// ListOpenAICompatibleModels 和 ValidateAPIKey 保持不变...
func (s *GenAIService) ListOpenAICompatibleModels(ctx context.Context) ([]byte, int, error) {
	// 1. 获取一个可用的 API Key
	activeKey, err := s.keyStore.GetNextActiveKey()
	if err != nil {
		// 如果没有可用的 key，返回内部服务器错误
		log.Println("获取模型列表失败: 没有可用的API Key")
		return nil, http.StatusInternalServerError, fmt.Errorf("没有可用的 API Key: %w", err)
	}
	log.Printf("正在使用 Key ID: %d 获取模型列表", activeKey.ID)
	// 2. 准备 HTTP 请求
	const url = "https://generativelanguage.googleapis.com/v1beta/openai/models"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		log.Printf("创建模型列表请求失败: %v", err)
		return nil, http.StatusInternalServerError, fmt.Errorf("创建请求失败: %w", err)
	}
	// 3. 设置 Authorization Header
	req.Header.Set("Authorization", "Bearer "+activeKey.Key)
	req.Header.Set("Accept", "application/json")
	// 4. 发送请求
	client := &http.Client{Timeout: 15 * time.Second} // 设置15秒超时
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("请求 Google 模型列表 API 失败 (Key ID: %d): %v", activeKey.ID, err)
		return nil, http.StatusBadGateway, fmt.Errorf("请求 Google API 失败: %w", err)
	}
	defer resp.Body.Close()
	// 5. 读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("读取模型列表响应体失败: %v", err)
		return nil, http.StatusInternalServerError, fmt.Errorf("读取响应体失败: %w", err)
	}
	// 6. 如果是 Key 相关错误 (如 401, 403, 429)，则禁用该 Key
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		errorReason := fmt.Sprintf("获取模型列表失败, 状态码: %d", resp.StatusCode)
		if resp.StatusCode == http.StatusTooManyRequests {
			s.temporaryDisableKey(activeKey.ID, errorReason)
		} else {
			s.keyStore.Disable(activeKey.ID, errorReason)
			log.Printf("因获取模型列表失败而禁用 Key ID %d", activeKey.ID)
		}
	}
	// 7. 返回原始响应体、状态码和 nil 错误
	return body, resp.StatusCode, nil
}

func (s *GenAIService) ListGeminiCompatibleModels(ctx context.Context, queryParams url.Values) ([]byte, int, error) {
	// 1. 获取一个可用的 API Key
	activeKey, err := s.keyStore.GetNextActiveKey()
	if err != nil {
		// 如果没有可用的 key，返回内部服务器错误
		log.Println("获取模型列表失败: 没有可用的API Key")
		return nil, http.StatusInternalServerError, fmt.Errorf("没有可用的 API Key: %w", err)
	}
	log.Printf("正在使用 Key ID: %d 获取模型列表", activeKey.ID)
	// 2. 准备 HTTP 请求
	// 构建带查询参数的URL
	baseURL := "https://generativelanguage.googleapis.com/v1beta/models"
	apiURL, _ := url.Parse(baseURL)
	apiURL.RawQuery = queryParams.Encode() // 保留原始查询参数
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL.String(), nil)
	if err != nil {
		log.Printf("创建模型列表请求失败: %v", err)
		return nil, http.StatusInternalServerError, fmt.Errorf("创建请求失败: %w", err)
	}
	// 3. 设置 Authorization Header
	req.Header.Set("X-Goog-Api-Key", activeKey.Key)
	req.Header.Set("Accept", "application/json")
	// 4. 发送请求
	client := &http.Client{Timeout: 15 * time.Second} // 设置15秒超时
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("请求 Google 模型列表 API 失败 (Key ID: %d): %v", activeKey.ID, err)
		return nil, http.StatusBadGateway, fmt.Errorf("请求 Google API 失败: %w", err)
	}
	defer resp.Body.Close()
	// 5. 读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("读取模型列表响应体失败: %v", err)
		return nil, http.StatusInternalServerError, fmt.Errorf("读取响应体失败: %w", err)
	}
	// 6. 如果是 Key 相关错误 (如 401, 403, 429)，则禁用该 Key
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		errorReason := fmt.Sprintf("获取模型列表失败, 状态码: %d", resp.StatusCode)
		if resp.StatusCode == http.StatusTooManyRequests {
			s.temporaryDisableKey(activeKey.ID, errorReason)
		} else {
			s.keyStore.Disable(activeKey.ID, errorReason)
			log.Printf("因获取模型列表失败而禁用 Key ID %d", activeKey.ID)
		}
	}
	// 7. 返回原始响应体、状态码和 nil 错误
	return body, resp.StatusCode, nil
}

// +++ 新增: 处理 Gemini 原生 generateContent API +++
func (s *GenAIService) GenerateContent(ctx context.Context, modelName string, reqBody []byte) ([]byte, int, error) {
	maxRetries := s.configManager.Get().MaxRetries
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		activeKey, err := s.keyStore.GetNextActiveKey()
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("无法获取可用的 API Key: %w", err)
		}
		if s.isKeyRateLimited(activeKey.ID) {
			lastErr = fmt.Errorf("key ID %d is rate limited", activeKey.ID)
			continue
		}

		log.Printf("第 %d 次尝试 (Gemini GenerateContent), 使用 Key ID: %d, 模型: %s", i+1, activeKey.ID, modelName)
		url_str := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", modelName)

		httpReq, err := http.NewRequestWithContext(ctx, "POST", url_str, bytes.NewReader(reqBody))
		if err != nil {
			lastErr = fmt.Errorf("创建 HTTP 请求失败: %w", err)
			continue
		}

		// 使用 X-Goog-Api-Key Header 进行认证
		httpReq.Header.Set("X-Goog-Api-Key", activeKey.Key)
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := s.httpClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("请求 Google API 失败 (Key ID: %d): %w", activeKey.ID, err)
			continue
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			lastErr = fmt.Errorf("读取上游响应体失败: %w", err)
			continue
		}

		if resp.StatusCode == http.StatusOK {
			log.Printf("Gemini GenerateContent 请求成功 (Key ID: %d)", activeKey.ID)
			return respBody, resp.StatusCode, nil
		}

		errorMsg := fmt.Sprintf("上游API错误 (HTTP %d): %s", resp.StatusCode, string(respBody))
		lastErr = errors.New(errorMsg)
		log.Printf("Key ID %d 请求失败: %s", activeKey.ID, errorMsg)

		if resp.StatusCode == http.StatusTooManyRequests {
			s.temporaryDisableKey(activeKey.ID, errorMsg)
		} else if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			s.keyStore.Disable(activeKey.ID, errorMsg)
		}
		// 其他错误则重试
	}
	return nil, http.StatusServiceUnavailable, fmt.Errorf("所有 API Key 均尝试失败，最后一次错误: %w", lastErr)
}

// +++ 新增: 处理 Gemini 原生 streamGenerateContent API +++
func (s *GenAIService) StreamGenerateContent(ctx context.Context, w io.Writer, modelName string, reqBody []byte) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming unsupported")
	}

	maxRetries := s.configManager.Get().MaxRetries
	var lastErr error

RetryLoop:
	for i := 0; i < maxRetries; i++ {
		activeKey, err := s.keyStore.GetNextActiveKey()
		if err != nil {
			return fmt.Errorf("无法获取可用的 API Key: %w", err)
		}

		if s.isKeyRateLimited(activeKey.ID) {
			lastErr = fmt.Errorf("key ID %d is rate limited", activeKey.ID)
			continue
		}

		log.Printf("第 %d 次尝试 (Gemini Stream), 使用 Key ID: %d, 模型: %s", i+1, activeKey.ID, modelName)

		// 注意 URL 中需要包含 ?alt=sse 参数
		url_str := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:streamGenerateContent?alt=sse", modelName)

		httpReq, err := http.NewRequestWithContext(ctx, "POST", url_str, bytes.NewReader(reqBody))
		if err != nil {
			lastErr = fmt.Errorf("创建 HTTP 请求失败: %w", err)
			continue
		}

		httpReq.Header.Set("X-Goog-Api-Key", activeKey.Key)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")

		resp, err := s.httpClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("请求 Google API 失败 (Key ID: %d): %w", activeKey.ID, err)
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			errorMsg := fmt.Sprintf("上游API错误 (HTTP %d): %s", resp.StatusCode, string(body))
			lastErr = errors.New(errorMsg)
			log.Printf("Key ID %d 请求失败: %s", activeKey.ID, errorMsg)

			if resp.StatusCode == http.StatusTooManyRequests {
				s.temporaryDisableKey(activeKey.ID, errorMsg)
			} else if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				s.keyStore.Disable(activeKey.ID, errorMsg)
			}
			continue RetryLoop
		}

		// 成功，开始流式转发
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.TrimSpace(line) == "" {
				continue
			}
			_, err := fmt.Fprintf(w, "%s\n\n", line)
			if err != nil {
				return err // 客户端可能已断开连接
			}
			flusher.Flush()
		}

		if err := scanner.Err(); err != nil {
			lastErr = err
			continue RetryLoop
		}

		log.Printf("Gemini Stream 请求成功 (Key ID: %d), 流已结束。", activeKey.ID)
		return nil // 成功完成
	}

	return fmt.Errorf("所有 API Key 均尝试失败，最后一次错误: %w", lastErr)
}

// +++ 新增: 处理 Gemini 原生 countTokens API +++
func (s *GenAIService) CountTokens(ctx context.Context, modelName string, reqBody []byte) ([]byte, int, error) {
	maxRetries := s.configManager.Get().MaxRetries
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		activeKey, err := s.keyStore.GetNextActiveKey()
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("无法获取可用的 API Key: %w", err)
		}
		if s.isKeyRateLimited(activeKey.ID) {
			lastErr = fmt.Errorf("key ID %d is rate limited", activeKey.ID)
			continue
		}

		log.Printf("第 %d 次尝试 (Gemini CountTokens), 使用 Key ID: %d, 模型: %s", i+1, activeKey.ID, modelName)
		url_str := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:countTokens", modelName)

		httpReq, err := http.NewRequestWithContext(ctx, "POST", url_str, bytes.NewReader(reqBody))
		if err != nil {
			lastErr = fmt.Errorf("创建 HTTP 请求失败: %w", err)
			continue
		}

		// 使用 X-Goog-Api-Key Header 进行认证
		httpReq.Header.Set("X-Goog-Api-Key", activeKey.Key)
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := s.httpClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("请求 Google API 失败 (Key ID: %d): %w", activeKey.ID, err)
			continue
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			lastErr = fmt.Errorf("读取上游响应体失败: %w", err)
			continue
		}

		if resp.StatusCode == http.StatusOK {
			log.Printf("Gemini CountTokens 请求成功 (Key ID: %d)", activeKey.ID)
			return respBody, resp.StatusCode, nil
		}

		errorMsg := fmt.Sprintf("上游API错误 (HTTP %d): %s", resp.StatusCode, string(respBody))
		lastErr = errors.New(errorMsg)
		log.Printf("Key ID %d 请求失败: %s", activeKey.ID, errorMsg)

		if resp.StatusCode == http.StatusTooManyRequests {
			s.temporaryDisableKey(activeKey.ID, errorMsg)
		} else if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			s.keyStore.Disable(activeKey.ID, errorMsg)
		}
		// 其他错误则重试
	}
	log.Printf("所有 %d 次重试均失败。", maxRetries)
	if lastErr != nil {
		return nil, http.StatusServiceUnavailable, fmt.Errorf("所有 API Key 均尝试失败，最后一次错误: %w", lastErr)
	}
	return nil, http.StatusServiceUnavailable, errors.New("所有 API Key 均尝试失败，但未捕获到具体错误")
}

func (s *GenAIService) ValidateAPIKey(apiKey string) (bool, string) {
	const url = "https://generativelanguage.googleapis.com/v1beta/openai/models"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		// 这种情况一般不会发生
		return false, "Failed to create request: " + err.Error()
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, "Request failed: " + err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return true, "Valid"
	}
	body, _ := io.ReadAll(resp.Body)
	// 返回一个组合了状态码和响应体的错误信息
	return false, fmt.Sprintf("Invalid (HTTP %d): %s", resp.StatusCode, string(body))
}
