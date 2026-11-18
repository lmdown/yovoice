package workbench

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"yovoice/internal/workbench/sdkdownload"
)

// [fork-local] venv/官方 SDK 下载路径：安装脚本在 <appRoot>/venv 建好带 modelscope+huggingface_hub 的
// conda 环境时，模型下载改走 Python SDK；没有该环境则探测失败，回退到内建 Go 下载器。宿主不参与。

// sdkProbe 缓存 venv python 路径与其 SDK 可用性探测，进程内只探一次，避免每次下载都起探测进程。
type sdkProbe struct {
	once   sync.Once
	python string
	usable bool
}

// resolveSDKPython 按顺序定位 venv 里的 python：① 环境变量 YOVOICE_PYTHON（start.sh 显式导出）；
// ② <appRoot>/venv 的 conda 环境（appRoot 即数据目录去掉尾部 data）。都找不到返回空串，调用方据此回退。
func (w *Workbench) resolveSDKPython() string {
	if p := os.Getenv("YOVOICE_PYTHON"); p != "" && isRegularFile(p) {
		return p
	}
	// WORKBENCH_DATA 指向 <appRoot>/data，其父目录才有 venv；测试里数据目录是临时目录，父级无 venv 自然落空。
	rel := filepath.Join("venv", "bin", "python")
	if runtime.GOOS == "windows" {
		rel = filepath.Join("venv", "Scripts", "python.exe")
	}
	if p := filepath.Join(filepath.Dir(w.Store.Root), rel); isRegularFile(p) {
		return p
	}
	return ""
}

// sdkUsable 返回可用的 venv python 路径与 SDK 可用性；不可用时调用方走 Go 下载器。
func (w *Workbench) sdkUsable() (string, bool) {
	w.sdk.once.Do(func() {
		w.sdk.python = w.resolveSDKPython()
		if w.sdk.python == "" {
			return
		}
		// 探测用独立上下文，不受首个下载任务的取消影响。
		w.sdk.usable = sdkdownload.Available(context.Background(), w.sdk.python)
	})
	return w.sdk.python, w.sdk.usable
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
