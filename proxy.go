package main

import (
	"net/http"
	"net/url"
	"sync"
	"time"
)

// isProxyAvailable 并发检测代理是否可用
// 要求 Google 204 和 GitHub Raw 两个检测目标都成功
func isProxyAvailable(proxy string) bool {
	proxyURL, err := url.Parse(proxy)
	if err != nil {
		return false
	}

	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   3 * time.Second,
	}

	// 检测目标列表
	testURLs := []struct {
		url        string
		expectCode int
	}{
		{"https://www.google.com/generate_204", http.StatusNoContent},                           // 204
		{"https://raw.githubusercontent.com/github/gitignore/main/Go.gitignore", http.StatusOK}, // 200
	}

	var wg sync.WaitGroup
	results := make(chan bool, len(testURLs))

	// 并发检测
	for _, t := range testURLs {
		wg.Add(1)
		go func(target string, expect int) {
			defer wg.Done()
			resp, err := client.Get(target)
			if err != nil {
				results <- false
				return
			}
			defer resp.Body.Close()
			results <- (resp.StatusCode == expect)
		}(t.url, t.expectCode)
	}

	// 等待所有检测完成
	wg.Wait()
	close(results)

	// 必须全部成功
	for ok := range results {
		if !ok {
			return false
		}
	}
	return true
}

// findAvailableProxy 优先检测配置文件中的代理，不可用则并发检测常见端口
func findAvailableProxy(configProxy string, candidates []string) string {
	// Step 1: 优先检测配置文件中的代理
	if configProxy != "" && isProxyAvailable(configProxy) {
		return configProxy
	}

	// Step 2: 并发检测候选代理
	resultCh := make(chan string, 1)
	var wg sync.WaitGroup

	for _, proxy := range candidates {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			if isProxyAvailable(p) {
				select {
				case resultCh <- p: // 只取第一个可用的
				default:
				}
			}
		}(proxy)
	}

	// 等待所有 goroutine 完成后关闭 channel
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// 返回第一个可用代理
	if proxy, ok := <-resultCh; ok {
		return proxy
	}
	return ""
}
