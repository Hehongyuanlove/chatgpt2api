package main

import (
	"io"
	"net/url"
	"strings"

	http "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"github.com/google/uuid"
)

type Client struct {
	httpClient       tls_client.HttpClient
	session          *Session
	userAgent        string
	deviceID         string
	sessionID        string
	clientVersion    string
	clientBuildNum   string
	powScriptSources []string
	powDataBuild     string
}

func NewClient(sess *Session, proxyURL string) (*Client, error) {
	jar := tls_client.NewCookieJar()

	opts := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(60),
		tls_client.WithClientProfile(profiles.Chrome_124),
		tls_client.WithCookieJar(jar),
		tls_client.WithRandomTLSExtensionOrder(),
	}
	if proxyURL != "" {
		opts = append(opts, tls_client.WithProxyUrl(proxyURL))
	}

	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), opts...)
	if err != nil {
		return nil, err
	}

	deviceID := uuid.New().String()
	sessionID := uuid.New().String()

	c := &Client{
		httpClient:     client,
		session:        sess,
		userAgent:      sess.GeneratedUserAgent,
		deviceID:       deviceID,
		sessionID:      sessionID,
		clientVersion:  "prod-a194cd50d4416d3c0b47c740f206b12ce60f5887",
		clientBuildNum: "6708908",
	}

	return c, nil
}

func (c *Client) baseHeaders() map[string]string {
	h := map[string]string{
		"User-Agent":                      c.userAgent,
		"Origin":                          "https://chatgpt.com",
		"Referer":                         "https://chatgpt.com/",
		"Accept-Language":                 "zh-CN,zh;q=0.9,en;q=0.8,en-US;q=0.7",
		"Cache-Control":                   "no-cache",
		"Pragma":                          "no-cache",
		"Priority":                        "u=1, i",
		"Sec-Ch-Ua":                       c.session.GeneratedSecChUA,
		"Sec-Ch-Ua-Arch":                  `"x86"`,
		"Sec-Ch-Ua-Bitness":               `"64"`,
		"Sec-Ch-Ua-Full-Version":          `"143.0.3650.96"`,
		"Sec-Ch-Ua-Full-Version-List":     `"Microsoft Edge";v="143.0.3650.96", "Chromium";v="143.0.7499.147", "Not A(Brand";v="24.0.0.0"`,
		"Sec-Ch-Ua-Mobile":                c.session.GeneratedSecChUAMobile,
		"Sec-Ch-Ua-Model":                 `""`,
		"Sec-Ch-Ua-Platform":              c.session.GeneratedSecChUAPlatform,
		"Sec-Ch-Ua-Platform-Version":      `"19.0.0"`,
		"Sec-Fetch-Dest":                  "empty",
		"Sec-Fetch-Mode":                  "cors",
		"Sec-Fetch-Site":                  "same-origin",
		"OAI-Device-Id":                   c.deviceID,
		"OAI-Session-Id":                  c.sessionID,
		"OAI-Language":                    "zh-CN",
		"OAI-Client-Version":              c.clientVersion,
		"OAI-Client-Build-Number":         c.clientBuildNum,
	}
	if tok := c.session.Token(); tok != "" {
		h["Authorization"] = "Bearer " + tok
	}
	return h
}

func (c *Client) headers(path string, extra map[string]string) map[string]string {
	h := c.baseHeaders()
	h["X-OpenAI-Target-Path"] = path
	h["X-OpenAI-Target-Route"] = path
	for k, v := range extra {
		h[k] = v
	}
	return h
}

func (c *Client) buildReq(method, urlStr string, body io.Reader, headers map[string]string) (*http.Request, error) {
	req, err := http.NewRequest(method, urlStr, body)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	baseOrder := []string{
		"Authorization", "User-Agent", "Origin", "Referer", "Accept-Language",
		"Cache-Control", "Pragma", "Priority",
		"Sec-Ch-Ua", "Sec-Ch-Ua-Arch", "Sec-Ch-Ua-Bitness",
		"Sec-Ch-Ua-Full-Version", "Sec-Ch-Ua-Full-Version-List",
		"Sec-Ch-Ua-Mobile", "Sec-Ch-Ua-Model", "Sec-Ch-Ua-Platform",
		"Sec-Ch-Ua-Platform-Version", "Sec-Fetch-Dest", "Sec-Fetch-Mode",
		"Sec-Fetch-Site",
		"OAI-Device-Id", "OAI-Session-Id", "OAI-Language",
		"OAI-Client-Version", "OAI-Client-Build-Number",
		"X-OpenAI-Target-Path", "X-OpenAI-Target-Route",
	}
	extra := []string{}
	for k := range headers {
		if k == "Accept" {
			extra = append(extra, k)
		}
	}
	for k := range headers {
		if k == "Content-Type" {
			extra = append(extra, k)
		}
	}
	for _, sentinelKey := range []string{
		"OpenAI-Sentinel-Chat-Requirements-Token",
		"OpenAI-Sentinel-Proof-Token",
		"OpenAI-Sentinel-Turnstile-Token",
		"OpenAI-Sentinel-SO-Token",
	} {
		if _, has := headers[sentinelKey]; has {
			extra = append(extra, sentinelKey)
		}
	}

	order := append(baseOrder, extra...)
	order = append(order, "Content-Length")

	req.Header[http.HeaderOrderKey] = order

	return req, nil
}

func (c *Client) mustUUID() string {
	return uuid.New().String()
}

func (c *Client) SetCookies(cookies []string) {
	baseURL, _ := url.Parse("https://chatgpt.com")
	for _, cookie := range cookies {
		parts := strings.SplitN(cookie, "=", 2)
		if len(parts) == 2 {
			c.httpClient.SetCookies(baseURL, []*http.Cookie{
				{Name: strings.TrimSpace(parts[0]), Value: strings.TrimSpace(parts[1])},
			})
		}
	}
}
