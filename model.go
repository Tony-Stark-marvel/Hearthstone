package main

import (
	"context"
	"fmt"
	"google.golang.org/genai"
	"log"
	"net/http"
	"net/url"
	"os"
)

const ModelNameIdea = "models/gemini-3-flash-preview"

func main() {
	ctx := context.Background()
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		log.Fatal("请设置环境变量 GEMINI_API_KEY")
	}
	proxyUrl, _ := url.Parse("http://127.0.0.1:1082")

	myHttpClient := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyUrl),
		},
	}
	// 1. 初始化客户端
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:     apiKey,
		Backend:    genai.BackendGeminiAPI,
		HTTPClient: myHttpClient, // <--- 直接把带有代理的 Client 放在这里

	})
	if err != nil {
		log.Fatalf("客户端初始化失败: %v", err)
	}

	// 2. 调用流式接口
	// 注意：新版 SDK 返回的是一个 Iterator 对象
	for result, err := range client.Models.GenerateContentStream(
		ctx,
		ModelNameIdea,
		genai.Text("Give me top 3 indoor kids friendly ideas."),
		nil,
	) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Print(result.Candidates[0].Content.Parts[0].Text)
	}
}
