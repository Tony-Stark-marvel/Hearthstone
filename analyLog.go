package main

import (
	"context"
	"errors"
	"fmt"
	"google.golang.org/genai"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hpcloud/tail"
	"github.com/sashabaranov/go-openai"
)

var (
	// 日志缓冲区，只存最近的关键信息
	logBuffer      []string
	mu             sync.Mutex
	ModelNameAnaly = "gemini-3-flash-preview"
	GeminiBaseURL  = "https://generativelanguage.googleapis.com/v1beta/openai/"
)

func main() {
	// 1. 加载战旗卡牌库
	logPath := ("D:\\gox\\Hearthstone\\log\\Power.log")

	t, err := tail.TailFile(logPath, tail.Config{
		Follow: true,
		ReOpen: true,
		// whence 2是从最新的开始读取
		Location: &tail.SeekInfo{Offset: 0, Whence: 0},
	})
	if err != nil {
		fmt.Println("读取失败:", err)
		return
	}

	fmt.Println("🛰️  日志透传模式已启动。正在收集关键对局数据...")

	// 每 20 秒请求一次 AI，进行深度局势分析
	go aiAnalysisLoop()

	for line := range t.Lines {
		collectRelevantLog(line.Text)
	}
}

func aiAnalysisLoop() {
	for {
		time.Sleep(8 * time.Second) // 战棋决策周期较长

		mu.Lock()
		if len(logBuffer) == 0 {
			mu.Unlock()
			continue
		}
		// 复制当前缓冲区数据并清空
		logsToSend := strings.Join(logBuffer, "\n")
		// logBuffer = []string{} // 可选：发送后清空，确保下次是新数据
		mu.Unlock()

		fmt.Println("\n🤖 正在发送原始日志请求 AI 决策...")
		//callAI(logsToSend)
		callGeminiStream(logsToSend)
	}
}

// 过滤掉日志里的废话，只保留有价值的行
func collectRelevantLog(text string) {
	keywords := []string{"SHOW_ENTITY", "FULL_ENTITY", "TAG_CHANGE", "ZONE", "RESOURCES", "PLAYER_TECH_LEVEL", "CardID="}

	isUseful := false
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			isUseful = true
			break
		}
	}

	if isUseful {
		mu.Lock()
		// 只保留最近 60 行，这通常包含了一次刷新或一个回合的所有关键变动
		logBuffer = append(logBuffer, text)
		if len(logBuffer) > 60 {
			logBuffer = logBuffer[1:]
		}
		mu.Unlock()
	}
}

func callGeminiStream(rawLogs string) error {
	// 初始化 OpenAI 兼容客户端
	apiKey := os.Getenv("GEMINI_API_KEY")
	config := openai.DefaultConfig(apiKey)
	config.BaseURL = GeminiBaseURL
	proxyUrl, _ := url.Parse("http://127.0.0.1:1082")

	myHttpClient := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyUrl),
		},
	}
	config.HTTPClient = myHttpClient
	client := openai.NewClientWithConfig(config)

	ctx := context.Background()

	// 构造针对 10 行日志的专项 Prompt
	systemPrompt := `你是一位炉石传说酒馆战棋教练。
我会给你最近发生的10行核心游戏日志。
请注意：ZONE=3是场面，ZONE=4是手牌，ZONE=7是酒馆，RESOURCES是金币。
请根据这10行信息快速推断现状，并直接给出本回合的操作指令（买、卖、升本、刷新或冻结）。`

	req := openai.ChatCompletionRequest{
		Model: ModelNameAnaly,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: "最新日志片段如下：\n" + rawLogs},
		},
		Stream: true,
	}

	// 使用流式传输
	stream, err := client.CreateChatCompletionStream(ctx, req)
	if err != nil {
		return err
	}
	defer stream.Close()

	fmt.Print("✨ AI 导师建议: ")
	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			fmt.Println("\n--------------------------")
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Print(response.Choices[0].Delta.Content)
	}
}

func callAI(rawLogs string) {
	// 精心设计的系统提示词，教会 AI 读日志
	prompt := fmt.Sprintf(`你是一位资深的炉石传说酒馆战棋教练。
下面是一段原始对局日志，请分析并给出建议。

解析指南：
- ZONE=3: 在场上
- ZONE=4: 在手牌
- ZONE=7 或 SETASIDE: 在酒馆(待买)
- RESOURCES: 你的剩余金币
- CardID: 随从标识 (例如 BGS_001)

请输出：
1. 当前我的场面状态和流派建议。
2. 酒馆中有哪些值得购买的随从及其理由。
3. 给出本回合的具体操作顺序建议。

日志数据：
%s`, rawLogs)

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
		ModelNameAnaly,
		genai.Text(prompt),
		nil,
	) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Print(result.Candidates[0].Content.Parts[0].Text)
	}
}
