package main

import (
	"context"
	"encoding/json"
	"fmt"
	"google.golang.org/genai"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hpcloud/tail"
)

// --- 1. 数据结构定义 ---

type CardInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	TechLevel int    `json:"techLevel"` // 酒馆等级：战旗特有属性
	Type      string `json:"type"`      // MINION 或 HERO
}

type Entity struct {
	ID     string
	CardID string
	Name   string
	Zone   string // BOARD, HAND, SHOP
}

type GameSnapshot struct {
	Gold       int      `json:"gold"`
	TavernTier int      `json:"tavern_tier"`
	Board      []string `json:"board"`
	Hand       []string `json:"hand"`
	Shop       []string `json:"shop"`
}

var (
	cardDB       = make(map[string]string)
	entities     = make(map[string]*Entity)
	currentState = GameSnapshot{}
	mu           sync.Mutex
	// 填入你的大模型 API 信息 (以 OpenAI 为例)
	apiKey = "你的API_KEY"
	apiUrl = "https://api.openai.com/v1/chat/completions"
)

// --- 2. 正则表达式 ---
var (
	showEntityRegex = regexp.MustCompile(`SHOW_ENTITY - Entity=\[.*id=(?P<id>\d+).*\] CardID=(?P<cardID>\w+)`)
	tagChangeRegex  = regexp.MustCompile(`TAG_CHANGE Entity=(?P<ent>.*) tag=(?P<tag>\w+) value=(?P<val>\w+)`)
	entityIdRegex   = regexp.MustCompile(`id=(?P<id>\d+)`)
)

func main() {
	// 1. 加载并过滤战旗卡牌
	loadBattlegroundsCards("D:\\gox\\Hearthstone\\log\\cards.collectible.json")

	// 2. 日志路径 (请根据实际修改)
	logPath := "D:\\gox\\Hearthstone\\log\\Power.log"

	t, err := tail.TailFile(logPath, tail.Config{
		Follow:   true,
		ReOpen:   true,
		Location: &tail.SeekInfo{Offset: 0, Whence: 2},
	})
	if err != nil {
		fmt.Printf("❌ 错误：无法读取日志文件 %v\n", err)
		return
	}

	fmt.Println("🚀 酒馆战旗 AI 助手已就绪...")

	// 3. 开启 AI 决策循环
	go aiDecisionLoop()

	// 4. 解析日志流
	for line := range t.Lines {
		parseLine(line.Text)
	}
}

// --- 3. 核心解析函数 ---

func parseLine(text string) {
	mu.Lock()
	defer mu.Unlock()

	// 识别新实体（随从或英雄进入视野）
	if strings.Contains(text, "SHOW_ENTITY") {
		m := showEntityRegex.FindStringSubmatch(text)
		if len(m) > 2 {
			id, cardID := m[1], m[2]
			entities[id] = &Entity{
				ID:     id,
				CardID: cardID,
				Name:   cardDB[cardID],
			}
		}
	}

	// 识别状态变化
	if strings.Contains(text, "TAG_CHANGE") {
		m := tagChangeRegex.FindStringSubmatch(text)
		if len(m) > 3 {
			entPart, tag, val := m[1], m[2], m[3]

			// 金币更新 (RESOURCES)
			if tag == "RESOURCES" && !strings.Contains(entPart, "id=") {
				fmt.Sscanf(val, "%d", &currentState.Gold)
			}
			// 酒馆等级更新
			if tag == "PLAYER_TECH_LEVEL" {
				fmt.Sscanf(val, "%d", &currentState.TavernTier)
			}
			// 区域变化 (判定随从是在场上、手里还是酒馆)
			if tag == "ZONE" {
				idM := entityIdRegex.FindStringSubmatch(entPart)
				if len(idM) > 1 {
					id := idM[1]
					if e, ok := entities[id]; ok {
						e.Zone = convertZone(val)
					}
				}
			}
		}
	}
}

func convertZone(val string) string {
	switch val {
	case "PLAY":
		return "BOARD"
	case "HAND":
		return "HAND"
	case "SETASIDE", "SHOP":
		return "SHOP"
	default:
		return "OTHER"
	}
}

// --- 4. 卡牌数据库加载（带过滤） ---

func loadBattlegroundsCards(filename string) {
	data, _ := ioutil.ReadFile(filename)
	var allCards []CardInfo
	json.Unmarshal(data, &allCards)

	for _, c := range allCards {
		// 关键过滤：只保留有酒馆等级的随从或英雄
		if c.TechLevel > 0 || c.Type == "HERO" {
			cardDB[c.ID] = c.Name
		}
	}
	fmt.Printf("✅ 已加载战旗专用卡牌库，共 %d 个实体\n", len(cardDB))
}

// --- 5. AI 决策逻辑 ---

func aiDecisionLoop() {
	for {
		time.Sleep(12 * time.Second) // 战旗每回合时间充裕，12秒请求一次即可

		mu.Lock()
		// 整理当前快照数据
		currentState.Board, currentState.Hand, currentState.Shop = []string{}, []string{}, []string{}
		for _, e := range entities {
			name := e.Name
			if name == "" {
				continue
			}
			switch e.Zone {
			case "BOARD":
				currentState.Board = append(currentState.Board, name)
			case "HAND":
				currentState.Hand = append(currentState.Hand, name)
			case "SHOP":
				currentState.Shop = append(currentState.Shop, name)
			}
		}

		// 只有在金币大于0（操作阶段）才请求 AI
		if currentState.Gold > 0 && len(currentState.Shop) > 0 {
			go callAI(currentState)
		}
		mu.Unlock()
	}
}

func callAI(snap GameSnapshot) {
	prompt := fmt.Sprintf("你是炉石战棋专家。我当前等级%d, 金币%d。场上:%v, 手牌:%v, 酒馆刷新了:%v。请用一句话告诉我本回合最佳策略。",
		snap.TavernTier, snap.Gold, snap.Board, snap.Hand, snap.Shop)

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
		genai.Text(prompt),
		nil,
	) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Print(result.Candidates[0].Content.Parts[0].Text)
	}
}
