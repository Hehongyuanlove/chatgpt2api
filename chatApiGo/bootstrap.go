package main

import (
	"io"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

const defaultPowScript = "https://chatgpt.com/backend-api/sentinel/sdk.js"

func (c *Client) Bootstrap() error {
	req, err := c.buildReq("GET", "https://chatgpt.com/", nil, c.headers("/", map[string]string{
		"Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
	}))
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	sources, build := parsePowResources(string(body))
	c.powScriptSources = sources
	c.powDataBuild = build
	return nil
}

func parsePowResources(htmlContent string) (sources []string, dataBuild string) {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return []string{defaultPowScript}, ""
	}

	var extractSrcs func(*html.Node)
	extractSrcs = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "script" {
			for _, attr := range n.Attr {
				if attr.Key == "src" && attr.Val != "" {
					sources = append(sources, attr.Val)
					re := regexp.MustCompile(`c/[^/]*/_`)
					if match := re.FindString(attr.Val); match != "" {
						dataBuild = match
					}
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			extractSrcs(child)
		}
	}
	extractSrcs(doc)

	if len(sources) == 0 {
		sources = []string{defaultPowScript}
	}

	if dataBuild == "" {
		re := regexp.MustCompile(`<html[^>]*data-build="([^"]*)"`)
		if match := re.FindStringSubmatch(htmlContent); len(match) >= 2 {
			dataBuild = match[1]
		}
	}

	return
}
