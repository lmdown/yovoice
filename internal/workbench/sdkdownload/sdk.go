// Package sdkdownload 用官方 Python SDK（modelscope / huggingface_hub）下载模型文件，
// 作为 yovoice 内建 Go 下载器之外的可选路径：安装脚本建好 venv 时走这里，
// 没有 venv 时调用方回退到 internal/download。宿主（Swift/C#/Linux）不参与，逻辑都在服务进程内。
package sdkdownload

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"yovoice/internal/msg"
	"yovoice/internal/platform"
)

//go:embed worker.py
var workerScript []byte

// Spec 描述用官方 SDK 下载单个文件所需的全部参数。
type Spec struct {
	Source   string // "modelscope" 或 "huggingface"
	Repo     string // SDK 仓库 id
	Revision string // SDK 版本（分支或提交）
	Filename string // 仓库内路径
	Dest     string // 落地的绝对文件路径
	Endpoint string // Hugging Face 基础地址（如 hf-mirror）；空为官方默认
	Total    int64  // 预期字节数，用于进度分母
}

// Available 探测给定 python 是否同时可导入 modelscope 与 huggingface_hub。
// 结果应由调用方缓存，避免每次下载都起一个探测进程。
func Available(ctx context.Context, python string) bool {
	if python == "" {
		return false
	}
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second) // 20s，探测不该久等
	defer cancel()
	cmd := exec.CommandContext(runCtx, python, "-E", "-c", "import modelscope, huggingface_hub")
	platform.ConfigureProcess(cmd)
	cmd.Cancel = func() error { platform.KillProcess(cmd); return nil }
	return cmd.Run() == nil
}

// File 用 venv 里的 python 运行内嵌 worker 下载 spec 指定的文件。
// progress 收到 SDK 侧累计的已写/总字节；ctx 取消即杀进程（暂停/取消走这里）。
// logw 非 nil 时，worker 子进程的原生 stderr（modelscope / huggingface 自己的下载进度、tqdm 等）
// 会逐行转写到这里，便于事后排查；为 nil 时仅缓存用于报错。
// 失败返回带 detail 的下载错误，调用方可据此回退或上报。
func File(ctx context.Context, python string, spec Spec, progress func(done, total int64), logw io.Writer) error {
	if python == "" {
		return msg.Err(msg.ErrDownloadHTTP, msg.Params{"detail": "no python"})
	}
	dir, err := os.MkdirTemp("", "yovoice-sdkdl-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, "worker.py")
	if err := platform.WriteFileAtomic(script, workerScript, 0600); err != nil {
		return err
	}
	args := []string{"-E", script,
		"--source", spec.Source, "--repo", spec.Repo, "--revision", spec.Revision,
		"--filename", spec.Filename, "--dest", spec.Dest, "--total", strconv.FormatInt(spec.Total, 10),
	}
	if spec.Endpoint != "" {
		args = append(args, "--endpoint", spec.Endpoint)
	}
	cmd := exec.CommandContext(ctx, python, args...)
	platform.ConfigureProcess(cmd)
	cmd.Cancel = func() error { platform.KillProcess(cmd); return nil }
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	if logw != nil {
		fmt.Fprintf(logw, "\n=== %s %s @ %s -> %s (total=%d) ===\n", spec.Source, spec.Repo, spec.Revision, spec.Filename, spec.Total)
		cmd.Stderr = io.MultiWriter(&stderr, logw)
	} else {
		cmd.Stderr = &stderr
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue // 忽略 SDK 自身的噪声输出，只认 JSON 进度行
		}
		var evt struct {
			Event string `json:"event"`
			Done  int64  `json:"done"`
			Total int64  `json:"total"`
		}
		if json.Unmarshal([]byte(line), &evt) != nil {
			continue
		}
		if evt.Event == "done" {
			continue
		}
		if progress != nil {
			progress(evt.Done, evt.Total)
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		_ = cmd.Wait()
		return err
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err() // 取消/暂停：交给上层编排处理
		}
		return msg.Err(msg.ErrDownloadHTTP, msg.Params{"detail": lastLine(stderr.String())})
	}
	if _, err := os.Stat(spec.Dest); err != nil {
		return msg.Err(msg.ErrDownloadIncomplete, msg.Params{"detail": "worker 未产出目标文件"})
	}
	return nil
}

// lastLine 取多行输出的最后一行非空文本，截断到合理长度，供错误 detail 使用。
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			r := []rune(t)
			if len(r) > 500 {
				r = r[:500]
			}
			return string(r)
		}
	}
	return ""
}
