package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"strings"

	"github.com/dobyte/due/v2/log"
	"github.com/dobyte/due/v2/packet"
	"github.com/mark3labs/mcp-go/server"

	"due/mcp-client/internal/gameclient"
	"due/mcp-client/internal/mcpserver"
)

func main() {
	// 1. 日志重定向到 stderr，避免污染 MCP stdio 协议独占的 stdout
	initLog()

	// 2. 显式指定消息打包格式，避免依赖 ./etc 目录与当前工作目录的相对关系
	//    （必须与 gate/node 一致：routeBytes=4、seqBytes=0、大端序）
	packet.SetPacker(packet.NewPacker(
		packet.WithByteOrder(binary.BigEndian),
		packet.WithRouteBytes(4),
		packet.WithSeqBytes(0),
		packet.WithBufferBytes(100000),
	))

	// 3. 初始化多客户端管理器（内部创建 due client 组件并启动）
	mgr, err := gameclient.NewManager()
	if err != nil {
		log.Fatalf("初始化游戏客户端管理器失败: %v", err)
	}

	// 4. 启动 MCP server（stdio 传输）
	if err := server.ServeStdio(mcpserver.New(mgr)); err != nil {
		log.Fatalf("MCP server 退出: %v", err)
	}
}

// stderrSyncer 把 due 框架日志输出到 stderr
type stderrSyncer struct{}

func (stderrSyncer) Name() string { return "stderr" }

func (stderrSyncer) Write(e *log.Entity) error {
	_, err := fmt.Fprintf(os.Stderr, "[%s] %-5s %s %s\n", e.Time, strings.ToUpper(string(e.Level)), e.Caller, e.Message)
	return err
}

func (stderrSyncer) Close() error { return nil }

// initLog 将框架日志重定向到 stderr（MCP stdio 协议独占 stdout，不能有任何日志写到 stdout）
func initLog() {
	log.SetLogger(log.NewLogger(
		log.WithLevel(log.LevelDebug),
		log.WithTerminals(log.Terminal("stderr")),
		log.WithSyncers(stderrSyncer{}),
	))
}
