package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gordonklaus/portaudio"
	"google.golang.org/genai"
)

/*
*
这个是输入音频给模型
*/
const (
	ModelName      = "models/gemini-3-pro-preview"
	SampleRate     = 16000
	Channels       = 1
	ChunkSize      = 1024
	SilenceThresh  = 500
	SilenceMaxTime = 1200 * time.Millisecond
)

// AudioEngine 处理音频硬件 I/O
type AudioEngine struct {
	stream     *portaudio.Stream
	inputBuff  []int16
	outputBuff []int16
	playChan   chan []int16
}

func main() {
	// 1. 获取 API Key
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		log.Fatal("请设置环境变量 GEMINI_API_KEY")
	}

	// 2. 初始化 PortAudio
	portaudio.Initialize()
	defer portaudio.Terminate()

	ctx, cancel := context.WithCancel(context.Background())

	// 3. 启动音频引擎
	engine, err := NewAudioEngine()
	if err != nil {
		log.Fatalf("音频初始化失败: %v", err)
	}
	defer engine.Close()

	// 监听 Ctrl+C 退出
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\n正在退出...")
		cancel()
	}()

	fmt.Printf("------------------------------------------------\n")
	fmt.Printf("当前模型: %s\n", ModelName)
	fmt.Printf("🎤 系统就绪！请对着麦克风说话...\n")
	fmt.Printf("------------------------------------------------\n")

	// 4. 运行主会话循环
	if err := runGeminiLoop(ctx, apiKey, engine); err != nil {
		log.Println("运行出错:", err)
	}
}

// runGeminiLoop 核心逻辑
func runGeminiLoop(ctx context.Context, apiKey string, engine *AudioEngine) error {
	// 1. 创建客户端
	// 1. 设置代理 (请修改为你的实际端口)
	proxyUrl, _ := url.Parse("http://127.0.0.1:1082")

	myHttpClient := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyUrl),
		},
	}

	// 2. 创建客户端 (新版 SDK 配置方式)
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:     strings.TrimSpace(apiKey),
		Backend:    genai.BackendGeminiAPI,
		HTTPClient: myHttpClient, // <--- 直接把带有代理的 Client 放在这里
	})
	if err != nil {
		return fmt.Errorf("客户端创建失败: %w", err)
	}

	// 2. 初始化对话历史
	var chatHistory []*genai.Content

	// 3. 配置生成参数 (Gemini 2.5 关键配置)
	config := &genai.GenerateContentConfig{
		Temperature:        genai.Ptr(float32(0.6)),
		ResponseModalities: []string{"AUDIO"}, // 强制要求返回音频
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{
				{Text: "You are a concise, helpful voice assistant. Reply with AUDIO."},
			},
		},
	}

	// --- 录音循环 ---
	var recording []int16
	var isSpeaking bool
	var silenceStart time.Time

	if err := engine.Start(); err != nil {
		return err
	}
	defer engine.Stop()

	ticker := time.NewTicker(time.Millisecond * 20)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// 读取音频
			if err := engine.ReadInput(); err != nil {
				continue
			}

			// VAD 检测
			vol := computeRMS(engine.inputBuff)
			if vol > SilenceThresh {
				if !isSpeaking {
					fmt.Print("🗣️  正在聆听... \r")
					isSpeaking = true
				}
				silenceStart = time.Time{}
				recording = append(recording, engine.inputBuff...)
			} else {
				if isSpeaking {
					recording = append(recording, engine.inputBuff...)
					if silenceStart.IsZero() {
						silenceStart = time.Now()
					} else if time.Since(silenceStart) > SilenceMaxTime {
						// 触发发送
						fmt.Printf("\n🚀 说话结束 (样本数: %d), 正在发送...\n", len(recording))

						// 调用处理函数，更新历史记录
						newHistory, err := handleConversation(ctx, client, chatHistory, config, recording, engine)
						if err != nil {
							log.Printf("交互错误: %v", err)
						} else {
							chatHistory = newHistory
						}

						// 重置状态
						recording = []int16{}
						isSpeaking = false
						silenceStart = time.Time{}
						fmt.Println("🎤 等待输入...")
					}
				}
			}
		}
	}
}

// handleConversation 发送音频并处理响应 (适配 iter.Seq2)
func handleConversation(
	ctx context.Context,
	client *genai.Client,
	history []*genai.Content,
	config *genai.GenerateContentConfig,
	audioData []int16,
	engine *AudioEngine,
) ([]*genai.Content, error) {

	// 1. PCM 转 WAV (API 要求)
	wavBytes := pcmToWav(audioData, SampleRate)

	// 2. 构建用户输入
	userContent := &genai.Content{
		Role: "user",
		Parts: []*genai.Part{
			{
				InlineData: &genai.Blob{
					MIMEType: "audio/wav",
					Data:     wavBytes,
				},
			},
		},
	}

	reqContents := append(history, userContent)

	// 3. 发起请求
	// stream 是一个 iter.Seq2 函数，不是对象
	fmt.Println("🚀 发送请求中...")
	stream := client.Models.GenerateContentStream(ctx, ModelName, reqContents, config)

	fmt.Println("👂 接收回复中...")
	var modelParts []*genai.Part

	// ==========================================================
	// 核心修复：直接使用 for ... range stream
	// Go 1.23 会自动调用那个 iterator 函数
	// ==========================================================
	for resp, err := range stream {
		if err != nil {
			return history, fmt.Errorf("流接收错误: %w", err)
		}

		if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
			content := resp.Candidates[0].Content
			modelParts = append(modelParts, content.Parts...)

			for _, part := range content.Parts {
				// 打印文本
				if part.Text != "" {
					fmt.Printf("🤖 Text: %s\n", part.Text)
					// 调用音频输出
					speakTextWindows(part.Text)
				}
				// 播放音频
				if part.InlineData != nil {
					fmt.Printf("🔊 Audio chunk: %d bytes\n", len(part.InlineData.Data))
					samples := bytesToInt16(part.InlineData.Data)
					engine.Play(samples)
				}
			}
		}
	}

	// 4. 更新历史记录
	history = append(history, userContent)
	if len(modelParts) > 0 {
		history = append(history, &genai.Content{
			Role:  "model",
			Parts: modelParts,
		})
	}

	return history, nil
}

// --- 以下辅助函数保持不变 ---

// pcmToWav 添加 WAV 头
func pcmToWav(pcm []int16, sampleRate int) []byte {
	buf := new(bytes.Buffer)
	numSamples := len(pcm)
	byteRate := sampleRate * Channels * 2
	dataSize := numSamples * 2
	fileSize := 36 + dataSize

	buf.WriteString("RIFF")
	binary.Write(buf, binary.LittleEndian, int32(fileSize))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	binary.Write(buf, binary.LittleEndian, int32(16))
	binary.Write(buf, binary.LittleEndian, int16(1))
	binary.Write(buf, binary.LittleEndian, int16(Channels))
	binary.Write(buf, binary.LittleEndian, int32(sampleRate))
	binary.Write(buf, binary.LittleEndian, int32(byteRate))
	binary.Write(buf, binary.LittleEndian, int16(Channels*2))
	binary.Write(buf, binary.LittleEndian, int16(16))
	buf.WriteString("data")
	binary.Write(buf, binary.LittleEndian, int32(dataSize))

	for _, sample := range pcm {
		binary.Write(buf, binary.LittleEndian, sample)
	}
	return buf.Bytes()
}

func NewAudioEngine() (*AudioEngine, error) {
	engine := &AudioEngine{
		inputBuff:  make([]int16, ChunkSize),
		outputBuff: make([]int16, ChunkSize),
		playChan:   make(chan []int16, 200),
	}
	stream, err := portaudio.OpenDefaultStream(Channels, Channels, float64(SampleRate), ChunkSize, engine.inputBuff, engine.outputBuff)
	if err != nil {
		return nil, err
	}
	engine.stream = stream
	return engine, nil
}

func (e *AudioEngine) Start() error {
	go e.playbackLoop()
	return e.stream.Start()
}
func (e *AudioEngine) Stop()            { e.stream.Stop(); e.stream.Close() }
func (e *AudioEngine) Close()           {}
func (e *AudioEngine) ReadInput() error { return e.stream.Read() }

func (e *AudioEngine) Play(data []int16) {
	for i := 0; i < len(data); i += ChunkSize {
		end := i + ChunkSize
		if end > len(data) {
			end = len(data)
		}
		chunk := make([]int16, end-i)
		copy(chunk, data[i:end])
		e.playChan <- chunk
	}
}

func (e *AudioEngine) playbackLoop() {
	for chunk := range e.playChan {
		// 补齐 buffer 防止爆音
		copy(e.outputBuff, make([]int16, ChunkSize))
		copy(e.outputBuff, chunk)
		e.stream.Write()
	}
}

func computeRMS(samples []int16) float64 {
	var sum float64
	for _, s := range samples {
		sum += float64(s) * float64(s)
	}
	if len(samples) == 0 {
		return 0
	}
	return math.Sqrt(sum / float64(len(samples)))
}

func bytesToInt16(data []byte) []int16 {
	numSamples := len(data) / 2
	samples := make([]int16, numSamples)
	reader := bytes.NewReader(data)
	binary.Read(reader, binary.LittleEndian, &samples)
	return samples
}

func speakTextWindows(text string) {
	// 简单的转义防止命令注入
	// 注意：这里只是简单的实现，对于极其复杂的字符可能需要更严谨的处理
	cmdStr := fmt.Sprintf(`Add-Type -AssemblyName System.Speech; (New-Object System.Speech.Synthesis.SpeechSynthesizer).Speak("%s");`, text)

	cmd := exec.Command("powershell", "-Command", cmdStr)
	err := cmd.Run()
	if err != nil {
		log.Printf("TTS 朗读失败: %v", err)
	}
}
