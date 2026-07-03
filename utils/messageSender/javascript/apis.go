package javascript

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/komari-monitor/komari/config"
)

const (
	javascriptHTTPTimeout       = 30 * time.Second
	javascriptMaxRequestBytes   = 64 * 1024
	javascriptMaxResponseBytes  = 1024 * 1024
	javascriptAllowedDomainsKey = "javascript_allowed_domains"
)

var defaultJavaScriptAllowedDomains = []string{
	"api.chuckfang.com",
	"api.telegram.org",
	"api.day.app",
	"discord.com",
	"discordapp.com",
	"open.feishu.cn",
	"oapi.dingtalk.com",
	"qyapi.weixin.qq.com",
	"sctapi.ftqq.com",
	"push.showdoc.com.cn",
}

func javascriptAllowedDomains() []string {
	allowed := ""
	if config.Ready() {
		allowed, _ = config.GetAs[string](javascriptAllowedDomainsKey, "")
	}
	if strings.TrimSpace(allowed) == "" {
		return defaultJavaScriptAllowedDomains
	}
	parts := strings.Split(allowed, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.ToLower(strings.TrimSpace(part))
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func javascriptHostAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	for _, allowed := range javascriptAllowedDomains() {
		allowed = strings.ToLower(strings.TrimSpace(allowed))
		if allowed == "" {
			continue
		}
		if strings.HasPrefix(allowed, "*.") {
			suffix := allowed[1:]
			if host == allowed[2:] || strings.HasSuffix(host, suffix) {
				return true
			}
			continue
		}
		if host == allowed {
			return true
		}
	}
	return false
}

func javascriptPrivateOrUnsafeIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsUnspecified() ||
		ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast()
}

func validateJavaScriptURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("only http and https URLs are allowed")
	}
	host := parsed.Hostname()
	if host == "" {
		return nil, fmt.Errorf("URL host is required")
	}
	if !javascriptHostAllowed(host) {
		return nil, fmt.Errorf("host %s is not permitted in the JavaScript notification whitelist", host)
	}

	if ip := net.ParseIP(host); ip != nil {
		if javascriptPrivateOrUnsafeIP(ip) {
			return nil, fmt.Errorf("private or internal IP addresses are not allowed")
		}
		return parsed, nil
	}

	ips, err := net.LookupIP(host)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve host %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("host %s did not resolve to any IP", host)
	}
	for _, ip := range ips {
		if javascriptPrivateOrUnsafeIP(ip) {
			return nil, fmt.Errorf("host %s resolves to a private or internal IP address", host)
		}
	}
	return parsed, nil
}

func validateJavaScriptHTTPMethod(method string) (string, error) {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		method = http.MethodGet
	}
	switch method {
	case http.MethodGet, http.MethodPost:
		return method, nil
	default:
		return "", fmt.Errorf("HTTP method %s is not allowed for JavaScript notifications", method)
	}
}

func newJavaScriptHTTPClient() *http.Client {
	return &http.Client{
		Timeout: javascriptHTTPTimeout,
		Transport: &http.Transport{
			DialContext:           javascriptSafeDialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			Proxy:                 nil,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return fmt.Errorf("redirects are disabled for JavaScript notification requests")
		},
	}
}

func javascriptSafeDialContext(ctx context.Context, network string, address string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		if javascriptPrivateOrUnsafeIP(ip) {
			return nil, fmt.Errorf("private or internal IP addresses are not allowed")
		}
	} else {
		ips, err := net.LookupIP(host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if javascriptPrivateOrUnsafeIP(ip) {
				return nil, fmt.Errorf("host %s resolves to a private or internal IP address", host)
			}
		}
	}

	dialer := net.Dialer{Timeout: 10 * time.Second}
	return dialer.DialContext(ctx, network, address)
}

func readJavaScriptResponseBody(r io.Reader) ([]byte, error) {
	limited := io.LimitReader(r, javascriptMaxResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(body) > javascriptMaxResponseBytes {
		return nil, fmt.Errorf("response body exceeds %d bytes", javascriptMaxResponseBytes)
	}
	return body, nil
}

func applyJavaScriptHeaders(req *http.Request, headers map[string]string) {
	for key, value := range headers {
		key = http.CanonicalHeaderKey(strings.TrimSpace(key))
		if key == "" || key == "Host" {
			continue
		}
		req.Header.Set(key, value)
	}
}

// createFetchFunction 创建一个 fetch API 实现
func (j *JavaScriptSender) createFetchFunction() func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 1 {
			panic(j.vm.NewTypeError("fetch requires at least 1 argument"))
		}

		url := call.Argument(0).String()

		// 解析选项
		options := map[string]interface{}{
			"method":  "GET",
			"headers": make(map[string]string),
			"body":    "",
		}

		if len(call.Arguments) > 1 {
			optObj := call.Argument(1).ToObject(j.vm)
			if optObj != nil {
				if method := optObj.Get("method"); method != nil && !goja.IsUndefined(method) {
					options["method"] = method.String()
				}
				if headers := optObj.Get("headers"); headers != nil && !goja.IsUndefined(headers) {
					headersObj := headers.ToObject(j.vm)
					if headersObj != nil {
						headerMap := make(map[string]string)
						for _, key := range headersObj.Keys() {
							headerMap[key] = headersObj.Get(key).String()
						}
						options["headers"] = headerMap
					}
				}
				if body := optObj.Get("body"); body != nil && !goja.IsUndefined(body) {
					options["body"] = body.String()
				}
			}
		}

		parsedURL, err := validateJavaScriptURL(url)
		if err != nil {
			panic(j.vm.NewTypeError(err.Error()))
		}
		method, err := validateJavaScriptHTTPMethod(options["method"].(string))
		if err != nil {
			panic(j.vm.NewTypeError(err.Error()))
		}
		bodyString := options["body"].(string)
		if len(bodyString) > javascriptMaxRequestBytes {
			panic(j.vm.NewTypeError(fmt.Sprintf("request body exceeds %d bytes", javascriptMaxRequestBytes)))
		}

		var body io.Reader
		if bodyString != "" {
			body = strings.NewReader(bodyString)
		}

		req, err := http.NewRequest(method, parsedURL.String(), body)
		if err != nil {
			panic(j.vm.NewTypeError(fmt.Sprintf("failed to create request: %v", err)))
		}
		applyJavaScriptHeaders(req, options["headers"].(map[string]string))

		resp, err := newJavaScriptHTTPClient().Do(req)
		if err != nil {
			panic(j.vm.NewTypeError(fmt.Sprintf("fetch failed: %v", err)))
		}
		defer resp.Body.Close()

		bodyBytes, err := readJavaScriptResponseBody(resp.Body)
		if err != nil {
			panic(j.vm.NewTypeError(fmt.Sprintf("failed to read response: %v", err)))
		}

		responseObj := j.vm.NewObject()
		responseObj.Set("status", resp.StatusCode)
		responseObj.Set("statusText", resp.Status)
		responseObj.Set("ok", resp.StatusCode >= 200 && resp.StatusCode < 300)

		headersObj := j.vm.NewObject()
		for key, values := range resp.Header {
			if len(values) > 0 {
				headersObj.Set(key, values[0])
			}
		}
		responseObj.Set("headers", headersObj)

		responseObj.Set("text", func(goja.FunctionCall) goja.Value {
			return j.vm.ToValue(string(bodyBytes))
		})

		responseObj.Set("json", func(goja.FunctionCall) goja.Value {
			var result interface{}
			if err := json.Unmarshal(bodyBytes, &result); err != nil {
				panic(j.vm.NewTypeError(fmt.Sprintf("failed to parse JSON: %v", err)))
			}
			return j.vm.ToValue(result)
		})

		return responseObj
	}
}

// createXHRConstructor 创建一个 XMLHttpRequest 构造函数
func (j *JavaScriptSender) createXHRConstructor() func(goja.ConstructorCall) *goja.Object {
	return func(call goja.ConstructorCall) *goja.Object {
		xhr := call.This

		// 内部状态
		var method, url string
		var headers = make(map[string]string)
		var requestBody string
		var async = true

		// readyState
		xhr.Set("readyState", 0)
		xhr.Set("status", 0)
		xhr.Set("statusText", "")
		xhr.Set("responseText", "")
		xhr.Set("response", "")

		// 事件处理器
		xhr.Set("onreadystatechange", goja.Null())
		xhr.Set("onload", goja.Null())
		xhr.Set("onerror", goja.Null())

		// open 方法
		xhr.Set("open", func(call goja.FunctionCall) goja.Value {
			if len(call.Arguments) < 2 {
				panic(j.vm.NewTypeError("open requires at least 2 arguments"))
			}
			method = call.Argument(0).String()
			url = call.Argument(1).String()
			if len(call.Arguments) > 2 {
				async = call.Argument(2).ToBoolean()
			}
			xhr.Set("readyState", 1)
			j.callHandler(xhr, "onreadystatechange")
			return goja.Undefined()
		})

		// setRequestHeader 方法
		xhr.Set("setRequestHeader", func(call goja.FunctionCall) goja.Value {
			if len(call.Arguments) < 2 {
				panic(j.vm.NewTypeError("setRequestHeader requires 2 arguments"))
			}
			key := call.Argument(0).String()
			value := call.Argument(1).String()
			headers[key] = value
			return goja.Undefined()
		})

		// send 方法
		xhr.Set("send", func(call goja.FunctionCall) goja.Value {
			if len(call.Arguments) > 0 && !goja.IsUndefined(call.Argument(0)) && !goja.IsNull(call.Argument(0)) {
				requestBody = call.Argument(0).String()
			}

			sendFunc := func() {
				defer func() {
					if r := recover(); r != nil {
						xhr.Set("readyState", 4)
						xhr.Set("status", 0)
						xhr.Set("statusText", fmt.Sprintf("Error: %v", r))
						j.callHandler(xhr, "onerror")
						j.callHandler(xhr, "onreadystatechange")
					}
				}()

				parsedURL, err := validateJavaScriptURL(url)
				if err != nil {
					xhr.Set("readyState", 4)
					xhr.Set("status", 0)
					xhr.Set("statusText", err.Error())
					j.callHandler(xhr, "onerror")
					j.callHandler(xhr, "onreadystatechange")
					return
				}
				validMethod, err := validateJavaScriptHTTPMethod(method)
				if err != nil {
					xhr.Set("readyState", 4)
					xhr.Set("status", 0)
					xhr.Set("statusText", err.Error())
					j.callHandler(xhr, "onerror")
					j.callHandler(xhr, "onreadystatechange")
					return
				}
				if len(requestBody) > javascriptMaxRequestBytes {
					xhr.Set("readyState", 4)
					xhr.Set("status", 0)
					xhr.Set("statusText", fmt.Sprintf("request body exceeds %d bytes", javascriptMaxRequestBytes))
					j.callHandler(xhr, "onerror")
					j.callHandler(xhr, "onreadystatechange")
					return
				}

				// 创建请求
				var body io.Reader
				if requestBody != "" {
					body = bytes.NewReader([]byte(requestBody))
				}

				req, err := http.NewRequest(validMethod, parsedURL.String(), body)
				if err != nil {
					xhr.Set("readyState", 4)
					xhr.Set("status", 0)
					xhr.Set("statusText", err.Error())
					j.callHandler(xhr, "onerror")
					j.callHandler(xhr, "onreadystatechange")
					return
				}

				// 设置请求头
				applyJavaScriptHeaders(req, headers)

				// 发送请求
				xhr.Set("readyState", 2)
				j.callHandler(xhr, "onreadystatechange")

				resp, err := newJavaScriptHTTPClient().Do(req)
				if err != nil {
					xhr.Set("readyState", 4)
					xhr.Set("status", 0)
					xhr.Set("statusText", err.Error())
					j.callHandler(xhr, "onerror")
					j.callHandler(xhr, "onreadystatechange")
					return
				}
				defer resp.Body.Close()

				// 读取响应
				xhr.Set("readyState", 3)
				j.callHandler(xhr, "onreadystatechange")

				bodyBytes, err := readJavaScriptResponseBody(resp.Body)
				if err != nil {
					xhr.Set("readyState", 4)
					xhr.Set("status", resp.StatusCode)
					xhr.Set("statusText", err.Error())
					j.callHandler(xhr, "onerror")
					j.callHandler(xhr, "onreadystatechange")
					return
				}

				// 完成
				xhr.Set("readyState", 4)
				xhr.Set("status", resp.StatusCode)
				xhr.Set("statusText", resp.Status)
				xhr.Set("responseText", string(bodyBytes))
				xhr.Set("response", string(bodyBytes))
				j.callHandler(xhr, "onreadystatechange")
				j.callHandler(xhr, "onload")
			}

			if async {
				go sendFunc()
			} else {
				sendFunc()
			}

			return goja.Undefined()
		})

		// getAllResponseHeaders 方法
		xhr.Set("getAllResponseHeaders", func(call goja.FunctionCall) goja.Value {
			return j.vm.ToValue("")
		})

		// getResponseHeader 方法
		xhr.Set("getResponseHeader", func(call goja.FunctionCall) goja.Value {
			return goja.Null()
		})

		return nil
	}
}

// callHandler 调用事件处理器
func (j *JavaScriptSender) callHandler(obj *goja.Object, handlerName string) {
	handler := obj.Get(handlerName)
	if handler != nil && !goja.IsUndefined(handler) && !goja.IsNull(handler) {
		if fn, ok := goja.AssertFunction(handler); ok {
			fn(obj)
		}
	}
}
