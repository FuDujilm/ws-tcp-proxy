package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"gopkg.in/yaml.v2"
)

type Config struct {
	LocalPort         int    `yaml:"local_port"`
	WebSocketURL      string `yaml:"websocket_url"`
	ReconnectDelaySec int    `yaml:"reconnect_delay_sec"`
	MaxRetries        int    `yaml:"max_retries"`
	ResolveCDN        bool   `yaml:"resolve_cdn"`
}

var configFile = "client.yaml"

func main() {
	printBanner()

	cfg := loadConfig(configFile)

	listenAddr := fmt.Sprintf(":%d", cfg.LocalPort)
	log.Printf("\033[34m[🎮] TCP 本地监听端口：%s\033[0m", listenAddr)
	log.Printf("\033[36m[🌐] WebSocket 转发地址：%s\033[0m", cfg.WebSocketURL)

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("\033[31m[错误] 无法监听本地端口：%v\033[0m\n", err)
	}
	defer listener.Close()

	log.Println("\033[32m[状态] 等待 Minecraft 客户端连接...\033[0m")

	for {
		clientConn, err := listener.Accept()
		if err != nil {
			log.Println("\033[31m[错误] 接收连接失败：", err, "\033[0m")
			continue
		}
		log.Println("\033[34m[📡] Minecraft 客户端已连接，正在建立 WebSocket 通道\033[0m")
		go handleConnection(clientConn, cfg)
	}
}

func handleConnection(clientConn net.Conn, cfg *Config) {
	defer clientConn.Close()

	if cfg.ResolveCDN {
		resolveCDNAddress(cfg.WebSocketURL)
	}

	var ws *websocket.Conn
	var err error
	for attempt := 1; attempt <= cfg.MaxRetries; attempt++ {
		ws, _, err = websocket.DefaultDialer.Dial(cfg.WebSocketURL, nil)
		if err == nil {
			log.Printf("\033[32m[✅] WebSocket 连接成功（第 %d 次尝试）\033[0m", attempt)
			break
		}
		log.Printf("\033[33m[重试中] WS 第 %d/%d 次连接失败：%v\033[0m", attempt, cfg.MaxRetries, err)
		time.Sleep(time.Duration(cfg.ReconnectDelaySec) * time.Second)
	}

	if ws == nil {
		log.Println("\033[31m[❌] 所有尝试失败，放弃该客户端连接\033[0m")
		return
	}
	defer ws.Close()

	// TCP → WebSocket
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := clientConn.Read(buf)
			if err != nil {
				if err != io.EOF {
					log.Println("\033[31m[TCP → WS] 读取失败：", err, "\033[0m")
				}
				_ = ws.WriteMessage(websocket.CloseMessage, []byte("tcp closed"))
				return
			}
			err = ws.WriteMessage(websocket.BinaryMessage, buf[:n])
			if err != nil {
				log.Println("\033[31m[TCP → WS] 写入失败：", err, "\033[0m")
				return
			}
		}
	}()

	// WebSocket → TCP
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			log.Println("\033[31m[WS → TCP] 读取失败：", err, "\033[0m")
			return
		}
		_, err = clientConn.Write(data)
		if err != nil {
			log.Println("\033[31m[WS → TCP] 写入失败：", err, "\033[0m")
			return
		}
	}
}

func resolveCDNAddress(url string) {
	host := extractHostname(url)
	port := "80"
	if strings.HasPrefix(url, "wss://") {
		port = "443"
	} else if strings.Contains(host, ":") {
		parts := strings.Split(host, ":")
		host = parts[0]
		port = parts[1]
	}

	addr := net.JoinHostPort(host, port)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		log.Printf("\033[33m[CDN检测] 无法连接 %s: %v\033[0m", addr, err)
		return
	}
	remoteAddr := conn.RemoteAddr().String()
	conn.Close()
	log.Printf("\033[35m[CDN检测] 使用 IP：%s\033[0m", remoteAddr)

	// 查询地理位置
	ipOnly := strings.Split(remoteAddr, ":")[0]
	queryCDNGeo(ipOnly)
}

func queryCDNGeo(ip string) {
	timeout := time.Second * 3
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get("https://ipapi.co/" + ip + "/json")
	if err != nil {
		log.Printf("\033[33m[CDN地理] 查询失败：%v\033[0m", err)
		return
	}
	defer resp.Body.Close()

	var data struct {
		City      string  `json:"city"`
		Region    string  `json:"region"`
		Country   string  `json:"country_name"`
		Org       string  `json:"org"`
		ASN       string  `json:"asn"`
		Timezone  string  `json:"timezone"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		log.Printf("\033[33m[CDN地理] 解析失败：%v\033[0m", err)
		return
	}
	log.Printf("\033[36m[CDN地理] %s, %s, %s | ASN: %s | 运营商: %s | 时区: %s\033[0m",
		data.Country, data.Region, data.City, data.ASN, data.Org, data.Timezone)
}

func extractHostname(rawURL string) string {
	host := rawURL
	if strings.HasPrefix(host, "ws://") {
		host = strings.TrimPrefix(host, "ws://")
	} else if strings.HasPrefix(host, "wss://") {
		host = strings.TrimPrefix(host, "wss://")
	}
	if i := strings.Index(host, "/"); i != -1 {
		host = host[:i]
	}
	return host
}

func loadConfig(path string) *Config {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		log.Println("\033[33m[配置] 未找到配置文件，正在生成默认配置...\033[0m")
		defaultCfg := Config{
			LocalPort:         25566,
			WebSocketURL:      "ws://127.0.0.1:12381",
			ReconnectDelaySec: 3,
			MaxRetries:        5,
			ResolveCDN:        true,
		}
		data, _ := yaml.Marshal(defaultCfg)
		_ = ioutil.WriteFile(path, data, 0644)
		log.Println("\033[32m[配置] 已生成 client.yaml，请按需修改\033[0m")
		return &defaultCfg
	}

	data, err := ioutil.ReadFile(path)
	if err != nil {
		log.Fatalln("\033[31m[配置错误] 读取失败：", err, "\033[0m")
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		log.Fatalln("\033[31m[配置错误] 解析失败：", err, "\033[0m")
	}

	return &cfg
}

func printBanner() {
	fmt.Println("\033[36m")
	fmt.Println("                                   ___                          _  _            ")
	fmt.Println("  /\\/\\    ___   ___  __      __   / _ \\  __ _  _ __   __ _   __| |(_) ___   ___ ")
	fmt.Println(" /    \\  / _ \\ / _ \\ \\ \\ /\\ / /  / /_)/ / _` || '__| / _` | / _` || |/ __| / _ \\")
	fmt.Println("/ /\\/\\ \\|  __/| (_) | \\ V  V /  / ___/ | (_| || |   | (_| || (_| || |\\__ \\|  __/")
	fmt.Println("\\/    \\/ \\___| \\___/   \\_/\\_/   \\/      \\__,_||_|    \\__,_| \\__,_||_||___/ \\___|")
	fmt.Println("╔════════════════════════════════════════════════════════════════════════════╗")
	fmt.Println("║ 🐾 Meow Paradise 控制面板 - CLI 版本                                       ║")
	fmt.Println("║                            ║")
	fmt.Println("║ 📸 https://github.com/FuDujilm/ws-tcp-proxy                               ║")
	fmt.Println("╚════════════════════════════════════════════════════════════════════════════╝")
	fmt.Println("\033[0m")
}
