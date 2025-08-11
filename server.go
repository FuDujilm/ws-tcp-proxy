package main

import (
	"bufio"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
	"gopkg.in/yaml.v2"
)

// 配置结构
type Config struct {
	WSPort  int    `yaml:"ws_port"`
	TCPHost string `yaml:"tcp_host"`
	TCPPort int    `yaml:"tcp_port"`
}

var defaultConfig = Config{
	WSPort:  8080,
	TCPHost: "localhost",
	TCPPort: 25565,
}

// 自动生成默认配置
func writeDefaultConfig(filename string) error {
	data, err := yaml.Marshal(&defaultConfig)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(filename, data, 0644)
}

// 读取配置文件
func loadConfig(filename string) (*Config, error) {
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		log.Println("\033[33m[配置] 未找到 config.yaml，正在生成默认配置...\033[0m")
		if err := writeDefaultConfig(filename); err != nil {
			return nil, fmt.Errorf("无法创建默认配置: %v", err)
		}
		log.Println("\033[32m[配置] 默认配置已生成，请检查 config.yaml\033[0m")
	}
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("读取配置失败: %v", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置失败: %v", err)
	}
	return &cfg, nil
}

// 获取公网 IP
func getPublicIP(url string) string {
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "获取失败：" + err.Error()
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "读取失败：" + err.Error()
	}
	return string(body)
}

// 打印本地 + 公网 IP
func printIPInfo() {
	log.Println("\033[34m[本地IP]\033[0m")
	addrs, _ := net.InterfaceAddrs()
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			ip := ipNet.IP.String()
			if ipNet.IP.To4() != nil {
				log.Println("  IPv4:", ip)
			} else if ipNet.IP.To16() != nil {
				log.Println("  IPv6:", ip)
			}
		}
	}
	log.Println("\033[34m[公网IP]\033[0m")
	log.Println("  IPv4:", getPublicIP("https://api.ipify.org"))
	log.Println("  IPv6:", getPublicIP("https://api64.ipify.org"))
}

// 控制台 Banner
func printBanner() {
	fmt.Println("\033[36m")
	fmt.Println(`  __  __ ______ _____  _____  _____             _     _`)
	fmt.Println(` |  \/  |  ____|  __ \|  __ \|  __ \     /\    | |   (_)`)
	fmt.Println(` | \  / | |__  | |__) | |__) | |__) |   /  \   | |__  _  ___`)
	fmt.Println(` | |\/| |  __| |  ___/|  ___/|  _  /   / /\ \  | '_ \| |/ __|`)
	fmt.Println(` | |  | | |____| |    | |    | | \ \  / ____ \ | |_) | | (__`)
	fmt.Println(` |_|  |_|______|_|    |_|    |_|  \_\/_/    \_\|_.__/|_|\___|`)
	fmt.Println("\033[35m         🐾 MeowParadise - WebSocket ⇄ Minecraft Proxy")
	fmt.Println("         🌐 https://mzyd.work | https://hhnlab.cn")
	fmt.Println("\033[0m===============================================================\n")
}

// 端口占用自动重试
func startServerWithFallback(startPort int, maxTries int, handler http.Handler) (int, error) {
	for i := 0; i < maxTries; i++ {
		port := startPort + i
		addr := fmt.Sprintf(":%d", port)
		server := &http.Server{Addr: addr, Handler: handler}

		ln, err := net.Listen("tcp", addr)
		if err != nil {
			log.Printf("\033[33m[端口占用]\033[0m %d 被占用，尝试下一个...", port)
			continue
		}

		go func() {
			log.Printf("\033[32m[启动成功]\033[0m WebSocket 服务监听 ws://0.0.0.0:%d\n", port)
			log.Fatal(server.Serve(ln))
		}()
		return port, nil
	}
	return 0, fmt.Errorf("端口全部被占用")
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func handleWS(w http.ResponseWriter, r *http.Request, tcpTarget string) {
	wsConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("[WS] 升级失败:", err)
		return
	}
	defer wsConn.Close()
	log.Println("\033[36m[WS] 新的连接\033[0m")

	tcpConn, err := net.Dial("tcp", tcpTarget)
	if err != nil {
		log.Println("[TCP] 连接失败:", err)
		wsConn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, err.Error()))
		return
	}
	defer tcpConn.Close()
	log.Println("\033[36m[TCP] 已连接到", tcpTarget, "\033[0m")

	go func() {
		for {
			mt, message, err := wsConn.ReadMessage()
			if err != nil {
				log.Println("[WS] 读取错误:", err)
				tcpConn.Close()
				return
			}
			if mt == websocket.BinaryMessage {
				tcpConn.Write(message)
			}
		}
	}()

	buf := make([]byte, 4096)
	for {
		n, err := tcpConn.Read(buf)
		if err != nil {
			if err != io.EOF {
				log.Println("[TCP] 读取错误:", err)
			}
			break
		}
		wsConn.WriteMessage(websocket.BinaryMessage, buf[:n])
	}
	log.Println("\033[33m[连接关闭]\033[0m")
}

// 按回车退出
func waitForExit() {
	fmt.Println("\n\033[33m按下 Enter 键退出程序...\033[0m")
	bufio.NewReader(os.Stdin).ReadBytes('\n')
}

func run() {
	printBanner()

	config, err := loadConfig("config.yaml")
	if err != nil {
		log.Fatalln("\033[31m[配置错误]\033[0m", err)
	}
	tcpTarget := fmt.Sprintf("%s:%d", config.TCPHost, config.TCPPort)
	printIPInfo()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		handleWS(w, r, tcpTarget)
	})

	port, err := startServerWithFallback(config.WSPort, 20, http.DefaultServeMux)
	if err != nil {
		log.Fatalln("[错误] 所有端口都无法监听：", err)
	}
	log.Printf("\033[36m[监听端口]\033[0m 实际使用端口：%d\n", port)
	select {} // 阻塞主线程
}

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("\033[31m[崩溃] 程序异常退出：\033[0m", r)
		}
		waitForExit()
	}()

	run()
}
