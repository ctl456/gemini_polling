package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gemini_polling/config" // 引入 config 包
	"gemini_polling/logger"
	"gemini_polling/model"
	"gemini_polling/storage"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 移除了这里的 const rateLimitCooldown

type GenAIService struct {
	keyStore      *storage.KeyStore
	httpClient    *http.Client
	configManager *config.Manager // 持有 Manager 而不是静态配置
	keyPool       *KeyPool
}

// BannedKeyInfo 用于向前端展示被临时禁用的Key信息
type BannedKeyInfo struct {
	model.APIKey
	BannedUntil time.Time `json:"banned_until"`
}

// NewGenAIService 构造函数现在接收完整的配置
func NewGenAIService(manager *config.Manager, keyStore *storage.KeyStore, keyPool *KeyPool) *GenAIService {
	// 优化后的 HTTP 连接池配置
	transport := &http.Transport{
		// 连接池大小优化 - 支持更高的并发
		MaxIdleConns:        500,
		MaxIdleConnsPerHost: 50,
		MaxConnsPerHost:     100,

		// 超时配置优化
		IdleConnTimeout:     120 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,

		// 启用 HTTP/2 支持
		ForceAttemptHTTP2: true,

		// 优化拨号器配置
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,

		// 响应头超时和 Expect 100-Continue 超时
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &GenAIService{
		keyStore:      keyStore,
		httpClient:    &http.Client{Timeout: 5 * time.Minute, Transport: transport},
		configManager: manager,
		keyPool:       keyPool,
	}
}

// StreamChat 现在使用配置的最大重试次数
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

	maxRetries := s.configManager.Get().MaxRetries
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		activeKey, err := s.keyPool.GetKey()
		if err != nil {
			lastErr = err
			if errors.Is(err, ErrNoAvailableKeys) {
				logger.Infoln("无可用 Key，等待 Key 池释放...")
				time.Sleep(2 * time.Second) // Wait a bit before retrying
			}
			continue
		}

		logger.Info("第 %d 次尝试, 使用 Key ID: %d, 模型: %s", i+1, activeKey.ID, req.Model)

		const url = "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"
		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBodyBytes))
		if err != nil {
			lastErr = fmt.Errorf("创建 HTTP 请求失败: %w", err)
			s.keyPool.ReturnKey(activeKey, false) // Return key on failure
			logger.Errorln(lastErr)
			continue
		}

		httpReq.Header.Set("Authorization", "Bearer "+activeKey.Key)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("Cache-Control", "no-cache")
		httpReq.Header.Set("Connection", "keep-alive")

		resp, err := s.httpClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("请求 Google API 失败 (Key ID: %d): %w", activeKey.ID, err)
			s.keyPool.ReturnKey(activeKey, false) // Return key on network failure
			logger.Errorln(lastErr)
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			errorMsg := fmt.Sprintf("上游API错误 (HTTP %d): %s", resp.StatusCode, string(body))
			lastErr = errors.New(errorMsg)
			logger.Error("Key ID %d 请求失败: %s", activeKey.ID, errorMsg)

			if resp.StatusCode == http.StatusTooManyRequests {
				s.keyPool.ReturnKey(activeKey, true) // Cooldown
			} else {
				if resp.StatusCode >= 400 && resp.StatusCode < 500 {
					s.keyStore.Disable(activeKey.ID, errorMsg)
				}
				s.keyPool.ReturnKey(activeKey, false) // Return without cooldown
			}
			continue
		}

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.TrimSpace(line) == "" {
				continue
			}
			if _, err := fmt.Fprintf(w, "%s\n\n", line); err != nil {
				logger.Warn("写入响应流失败: %v (客户端可能已断开连接)", err)
				s.keyPool.ReturnKey(activeKey, false)
				return err
			}
			flusher.Flush()
			if strings.HasSuffix(line, "[DONE]") {
				logger.Info("请求处理成功 (Key ID: %d), 流已结束。", activeKey.ID)
				s.keyPool.ReturnKey(activeKey, false)
				return nil
			}
		}

		if err := scanner.Err(); err != nil {
			logger.Error("读取上游流时发生错误 (Key ID: %d): %v", activeKey.ID, err)
			lastErr = err
			s.keyPool.ReturnKey(activeKey, false)
			continue
		}

		logger.Info("请求处理成功 (Key ID: %d), 上游流正常关闭。", activeKey.ID)
		s.keyPool.ReturnKey(activeKey, false)
		return nil
	}

	logger.Error("所有 %d 次重试均失败。", maxRetries)
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
		activeKey, err := s.keyPool.GetKey()
		if err != nil {
			lastErr = err
			if errors.Is(err, ErrNoAvailableKeys) {
				logger.Infoln("无可用 Key，等待 Key 池释放...")
				time.Sleep(2 * time.Second)
			}
			continue
		}

		logger.Info("第 %d 次尝试 (非流式), 使用 Key ID: %d, 模型: %s", i+1, activeKey.ID, req.Model)
		const url = "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"
		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBodyBytes))
		if err != nil {
			lastErr = fmt.Errorf("创建 HTTP 请求失败: %w", err)
			s.keyPool.ReturnKey(activeKey, false)
			continue
		}
		httpReq.Header.Set("Authorization", "Bearer "+activeKey.Key)
		httpReq.Header.Set("Content-Type", "application/json")
		resp, err := s.httpClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("请求 Google API 失败 (Key ID: %d): %w", activeKey.ID, err)
			s.keyPool.ReturnKey(activeKey, false)
			logger.Errorln(lastErr)
			continue
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			lastErr = fmt.Errorf("读取响应体失败: %w", err)
			s.keyPool.ReturnKey(activeKey, false)
			logger.Errorln(lastErr)
			continue
		}

		if resp.StatusCode == http.StatusOK {
			logger.Info("非流式请求成功 (Key ID: %d)", activeKey.ID)
			var successResp model.OpenAICompletionResponse
			if err := json.Unmarshal(body, &successResp); err != nil {
				s.keyPool.ReturnKey(activeKey, false)
				return nil, fmt.Errorf("解析上游成功响应失败: %w", err)
			}
			s.keyPool.ReturnKey(activeKey, false)
			return &successResp, nil
		}

		errorMsg := fmt.Sprintf("上游API错误 (HTTP %d): %s", resp.StatusCode, string(body))
		lastErr = errors.New(errorMsg)
		logger.Error("Key ID %d 请求失败: %s", activeKey.ID, errorMsg)

		isRateLimited := resp.StatusCode == http.StatusTooManyRequests
		if !isRateLimited && resp.StatusCode >= 400 && resp.StatusCode < 500 {
			s.keyStore.Disable(activeKey.ID, errorMsg)
		}
		s.keyPool.ReturnKey(activeKey, isRateLimited)
	}
	logger.Error("所有 %d 次重试均失败。", maxRetries)
	if lastErr != nil {
		return nil, fmt.Errorf("所有 API Key 均尝试失败，最后一次错误: %w", lastErr)
	}
	return nil, errors.New("所有 API Key 均尝试失败，但未捕获到具体错误")
}

// ListOpenAICompatibleModels 和 ValidateAPIKey 保持不变...
func (s *GenAIService) ListOpenAICompatibleModels(ctx context.Context) ([]byte, int, error) {
	activeKey, err := s.keyPool.GetKey()
	if err != nil {
		logger.Errorln("获取模型列表失败: 没有可用的API Key")
		return nil, http.StatusInternalServerError, fmt.Errorf("没有可用的 API Key: %w", err)
	}
	defer s.keyPool.ReturnKey(activeKey, false) // Always return the key

	logger.Info("正在使用 Key ID: %d 获取 OpenAI 模型列表", activeKey.ID)
	const url = "https://generativelanguage.googleapis.com/v1beta/openai/models"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+activeKey.Key)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("请求 Google API 失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("读取响应体失败: %w", err)
	}

	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		errorReason := fmt.Sprintf("获取模型列表失败, 状态码: %d", resp.StatusCode)
		if resp.StatusCode == http.StatusTooManyRequests {
			// This key is already on cooldown via the main request logic,
			// but we can re-affirm it here if needed.
			s.keyPool.ReturnKey(activeKey, true)
		} else {
			s.keyStore.Disable(activeKey.ID, errorReason)
		}
		logger.Warn("因获取模型列表失败而处理 Key ID %d", activeKey.ID)
	}

	return body, resp.StatusCode, nil
}

func (s *GenAIService) ListGeminiCompatibleModels(ctx context.Context, queryParams url.Values) ([]byte, int, error) {
	activeKey, err := s.keyPool.GetKey()
	if err != nil {
		logger.Errorln("获取模型列表失败: 没有可用的API Key")
		return nil, http.StatusInternalServerError, fmt.Errorf("没有可用的 API Key: %w", err)
	}
	// Defer returning the key right away. It will be returned without cooldown.
	// If a 429 happens, a separate ReturnKey(key, true) call can be made,
	// but the deferred one will just be a no-op on an empty channel.
	defer s.keyPool.ReturnKey(activeKey, false)

	logger.Info("正在使用 Key ID: %d 获取 Gemini 模型列表", activeKey.ID)
	baseURL := "https://generativelanguage.googleapis.com/v1beta/models"
	apiURL, _ := url.Parse(baseURL)
	apiURL.RawQuery = queryParams.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL.String(), nil)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("X-Goog-Api-Key", activeKey.Key)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("请求 Google API 失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("读取响应体失败: %w", err)
	}

	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		errorReason := fmt.Sprintf("获取模型列表失败, 状态码: %d", resp.StatusCode)
		if resp.StatusCode == http.StatusTooManyRequests {
			s.keyPool.ReturnKey(activeKey, true) // Explicitly start cooldown
		} else {
			s.keyStore.Disable(activeKey.ID, errorReason)
		}
		logger.Warn("因获取模型列表失败而处理 Key ID %d", activeKey.ID)
	}

	return body, resp.StatusCode, nil
}

// +++ 新增: 处理 Gemini 原生 generateContent API +++
func (s *GenAIService) GenerateContent(ctx context.Context, modelName string, reqBody []byte) ([]byte, int, error) {
	maxRetries := s.configManager.Get().MaxRetries
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		activeKey, err := s.keyPool.GetKey()
		if err != nil {
			lastErr = err
			if errors.Is(err, ErrNoAvailableKeys) {
				time.Sleep(2 * time.Second)
			}
			continue
		}

		logger.Info("第 %d 次尝试 (Gemini GenerateContent), 使用 Key ID: %d, 模型: %s", i+1, activeKey.ID, modelName)
		url_str := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", modelName)

		httpReq, err := http.NewRequestWithContext(ctx, "POST", url_str, bytes.NewReader(reqBody))
		if err != nil {
			lastErr = fmt.Errorf("创建 HTTP 请求失败: %w", err)
			s.keyPool.ReturnKey(activeKey, false)
			continue
		}

		httpReq.Header.Set("X-Goog-Api-Key", activeKey.Key)
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := s.httpClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("请求 Google API 失败 (Key ID: %d): %w", activeKey.ID, err)
			s.keyPool.ReturnKey(activeKey, false)
			continue
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			lastErr = fmt.Errorf("读取上游响应体失败: %w", err)
			s.keyPool.ReturnKey(activeKey, false)
			continue
		}

		if resp.StatusCode == http.StatusOK {
			logger.Info("Gemini GenerateContent 请求成功 (Key ID: %d)", activeKey.ID)
			s.keyPool.ReturnKey(activeKey, false)
			return respBody, resp.StatusCode, nil
		}

		errorMsg := fmt.Sprintf("上游API错误 (HTTP %d): %s", resp.StatusCode, string(respBody))
		lastErr = errors.New(errorMsg)
		logger.Error("Key ID %d 请求失败: %s", activeKey.ID, errorMsg)

		isRateLimited := resp.StatusCode == http.StatusTooManyRequests
		if !isRateLimited && resp.StatusCode >= 400 && resp.StatusCode < 500 {
			s.keyStore.Disable(activeKey.ID, errorMsg)
		}
		s.keyPool.ReturnKey(activeKey, isRateLimited)
	}
	return nil, http.StatusServiceUnavailable, fmt.Errorf("所有 API Key 均尝试失败，最后一次错误: %w", lastErr)
}

func (s *GenAIService) StreamGenerateContent(ctx context.Context, w io.Writer, modelName string, reqBody []byte) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming unsupported")
	}

	maxRetries := s.configManager.Get().MaxRetries
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		activeKey, err := s.keyPool.GetKey()
		if err != nil {
			lastErr = err
			if errors.Is(err, ErrNoAvailableKeys) {
				time.Sleep(2 * time.Second)
			}
			continue
		}

		logger.Info("第 %d 次尝试 (Gemini Stream), 使用 Key ID: %d, 模型: %s", i+1, activeKey.ID, modelName)
		url_str := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:streamGenerateContent?alt=sse", modelName)

		httpReq, err := http.NewRequestWithContext(ctx, "POST", url_str, bytes.NewReader(reqBody))
		if err != nil {
			lastErr = fmt.Errorf("创建 HTTP 请求失败: %w", err)
			s.keyPool.ReturnKey(activeKey, false)
			continue
		}

		httpReq.Header.Set("X-Goog-Api-Key", activeKey.Key)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")

		resp, err := s.httpClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("请求 Google API 失败 (Key ID: %d): %w", activeKey.ID, err)
			s.keyPool.ReturnKey(activeKey, false)
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			errorMsg := fmt.Sprintf("上游API错误 (HTTP %d): %s", resp.StatusCode, string(body))
			lastErr = errors.New(errorMsg)
			logger.Error("Key ID %d 请求失败: %s", activeKey.ID, errorMsg)

			isRateLimited := resp.StatusCode == http.StatusTooManyRequests
			if !isRateLimited && resp.StatusCode >= 400 && resp.StatusCode < 500 {
				s.keyStore.Disable(activeKey.ID, errorMsg)
			}
			s.keyPool.ReturnKey(activeKey, isRateLimited)
			continue
		}

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.TrimSpace(line) == "" {
				continue
			}
			_, err := fmt.Fprintf(w, "%s\n\n", line)
			if err != nil {
				s.keyPool.ReturnKey(activeKey, false)
				return err
			}
			flusher.Flush()
		}

		if err := scanner.Err(); err != nil {
			lastErr = err
			s.keyPool.ReturnKey(activeKey, false)
			continue
		}

		logger.Info("Gemini Stream 请求成功 (Key ID: %d), 流已结束。", activeKey.ID)
		s.keyPool.ReturnKey(activeKey, false)
		return nil
	}

	return fmt.Errorf("所有 API Key 均尝试失败，最后一次错误: %w", lastErr)
}

// +++ 新增: 处理 Gemini 原生 countTokens API +++
func (s *GenAIService) CountTokens(ctx context.Context, modelName string, reqBody []byte) ([]byte, int, error) {
	maxRetries := s.configManager.Get().MaxRetries
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		activeKey, err := s.keyPool.GetKey()
		if err != nil {
			lastErr = err
			if errors.Is(err, ErrNoAvailableKeys) {
				time.Sleep(2 * time.Second)
			}
			continue
		}

		logger.Info("第 %d 次尝试 (Gemini CountTokens), 使用 Key ID: %d, 模型: %s", i+1, activeKey.ID, modelName)
		url_str := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:countTokens", modelName)

		httpReq, err := http.NewRequestWithContext(ctx, "POST", url_str, bytes.NewReader(reqBody))
		if err != nil {
			lastErr = fmt.Errorf("创建 HTTP 请求失败: %w", err)
			s.keyPool.ReturnKey(activeKey, false)
			continue
		}

		httpReq.Header.Set("X-Goog-Api-Key", activeKey.Key)
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := s.httpClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("请求 Google API 失败 (Key ID: %d): %w", activeKey.ID, err)
			s.keyPool.ReturnKey(activeKey, false)
			continue
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			lastErr = fmt.Errorf("读取上游响应体失败: %w", err)
			s.keyPool.ReturnKey(activeKey, false)
			continue
		}

		if resp.StatusCode == http.StatusOK {
			logger.Info("Gemini CountTokens 请求成功 (Key ID: %d)", activeKey.ID)
			s.keyPool.ReturnKey(activeKey, false)
			return respBody, resp.StatusCode, nil
		}

		errorMsg := fmt.Sprintf("上游API错误 (HTTP %d): %s", resp.StatusCode, string(respBody))
		lastErr = errors.New(errorMsg)
		logger.Error("Key ID %d 请求失败: %s", activeKey.ID, errorMsg)

		isRateLimited := resp.StatusCode == http.StatusTooManyRequests
		if !isRateLimited && resp.StatusCode >= 400 && resp.StatusCode < 500 {
			s.keyStore.Disable(activeKey.ID, errorMsg)
		}
		s.keyPool.ReturnKey(activeKey, isRateLimited)
	}
	logger.Error("所有 %d 次重试均失败。", maxRetries)
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

// GetBannedKeysInfo 获取所有当前在内存中被临时禁用的Key的详细信息
func (s *GenAIService) GetBannedKeysInfo() ([]BannedKeyInfo, error) {
	return s.keyPool.GetBannedKeysInfo()
}
