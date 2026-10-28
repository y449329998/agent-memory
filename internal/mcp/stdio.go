package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Bridge 把 stdin 上的 JSON-RPC 逐行转发给常驻服务的 /mcp，再把响应逐行写回 stdout。
//
// 为什么要这层：agent 的 MCP 配置里只有 stdio 这一种是四家都稳定支持的，
// 而多个 agent 同时各起一份进程会争抢 SQLite 与重复扫描。桥让「agent 看到的形态」
// 与「我们内部的单实例」解耦。
type Bridge struct {
	endpoint string
	token    string
	client   *http.Client
}

func NewBridge(port int, token string) *Bridge {
	return &Bridge{
		endpoint: fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
		token:    token,
		client:   &http.Client{Timeout: 30 * time.Second},
	}
}

// SetEndpoint 覆盖转发目标，测试与非常规部署用它指向自定义地址。
func (b *Bridge) SetEndpoint(url string) { b.endpoint = url }

// ErrNoDaemon 是明确的、可指导下一步的失败，不是「连接被拒绝」。
var ErrNoDaemon = fmt.Errorf("agent-memory serve 未运行：先执行 agent-memory serve（或看 ~/.agent-memory/port 是否存在）")

func (b *Bridge) Send(ctx context.Context, message []byte) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint, bytes.NewReader(message))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.token)

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("%w (%v)", ErrNoDaemon, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, false, fmt.Errorf("token 不匹配：agent 配置里的 token 与 ~/.agent-memory/token 不一致，重跑 agent-memory link")
	}
	if resp.StatusCode == http.StatusAccepted {
		// 通知不需要回包
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("daemon returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, true, nil
}

// requestID 取入站消息的 id。缺失或 null 都是通知：JSON-RPC 2.0 规定
// 通知没有 id，也绝不允许对通知回包。id 保持原始字节，数字/字符串都原样带回。
func requestID(line []byte) json.RawMessage {
	var env struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(line, &env); err != nil {
		return nil
	}
	id := bytes.TrimSpace(env.ID)
	if len(id) == 0 || bytes.Equal(id, []byte("null")) {
		return nil
	}
	return id
}

// ServeStdio 读 stdin 的换行分隔 JSON-RPC 消息并转发。
// 每行一条消息；空行忽略；读到的行超过 8MB 时报错并退出，避免内存被撑爆。
func (b *Bridge) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 256*1024), 8*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		resp, needReply, err := b.Send(ctx, line)
		if err != nil {
			// 转发失败必须回一条 JSON-RPC error：静默让 agent 以为工具挂了并在原地重试。
			// 但只回给请求；通知没有 id，回包反而违反协议。
			id := requestID(line)
			if len(id) == 0 {
				continue
			}
			errMsg := err.Error()
			if len(errMsg) > 500 {
				errMsg = errMsg[:500]
			}
			body, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": json.RawMessage(id),
				"error": map[string]any{"code": CodeInternalError, "message": errMsg},
			})
			fmt.Fprintln(out, string(body))
			continue
		}
		if needReply {
			if _, err := out.Write(resp); err != nil {
				return err
			}
			if _, err := io.WriteString(out, "\n"); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}
