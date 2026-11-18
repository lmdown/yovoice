package sdkdownload

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"yovoice/internal/msg"
)

// writeFakePython 生成一个可执行的假 python 脚本，内容为给定 shell。仅用于 unix 测试。
func writeFakePython(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("假 python 用 /bin/sh，跳过 Windows")
	}
	path := filepath.Join(t.TempDir(), "fake-python")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFileStreamsProgressAndProducesDest(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "model.gguf")
	python := writeFakePython(t, `
dest=""
while [ $# -gt 0 ]; do
  case "$1" in
    --dest) dest="$2"; shift 2;;
    *) shift;;
  esac
done
printf '{"done":1,"total":10}\n'
printf '{"done":7,"total":10}\n'
printf 'raw progress line\n' >&2
printf 'content!' > "$dest"
printf '{"event":"done"}\n'
`)
	var seen [][2]int64
	var raw bytes.Buffer
	spec := Spec{Source: "huggingface", Repo: "a/b", Revision: "r", Filename: "model.gguf", Dest: dest, Total: 10}
	if err := File(context.Background(), python, spec, func(done, total int64) {
		seen = append(seen, [2]int64{done, total})
	}, &raw); err != nil {
		t.Fatal("假 SDK 下载应成功", err)
	}
	if len(seen) != 2 || seen[0] != [2]int64{1, 10} || seen[1] != [2]int64{7, 10} {
		t.Fatal("应解析到两条进度 JSON 行", seen)
	}
	// logw 应收到头部行与 worker 写到 stderr 的原始进度。
	if !strings.Contains(raw.String(), "===") || !strings.Contains(raw.String(), "raw progress") {
		t.Fatal("logw 应包含头部行与 worker stderr", raw.String())
	}
	info, err := os.Stat(dest)
	if err != nil || info.Size() != 8 {
		t.Fatal("应在 dest 落地产物", err)
	}
}

func TestFileWorkerFailureMapsToDownloadError(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "model.gguf")
	python := writeFakePython(t, `
printf "boom: network unreachable\n" >&2
exit 1
`)
	spec := Spec{Source: "modelscope", Repo: "a/b", Revision: "master", Filename: "model.gguf", Dest: dest, Total: 10}
	var raw bytes.Buffer
	err := File(context.Background(), python, spec, func(int64, int64) {}, &raw)
	if err == nil {
		t.Fatal("worker 非零退出应返回错误")
	}
	if !strings.Contains(raw.String(), "boom") {
		t.Fatal("失败时 worker stderr 也应转写到 logw", raw.String())
	}
	var ce *msg.CallError
	if !errors.As(err, &ce) || ce.Code != msg.ErrDownloadHTTP {
		t.Fatal("应映射为下载错误码", err)
	}
	if _, e := os.Stat(dest); e == nil {
		t.Fatal("失败时不应产出目标文件")
	}
}

func TestFileEmptyPythonErrors(t *testing.T) {
	if err := File(context.Background(), "", Spec{Dest: filepath.Join(t.TempDir(), "x")}, nil, nil); err == nil {
		t.Fatal("空 python 应直接报错，交由调用方回退")
	}
}

func TestAvailableProbesImport(t *testing.T) {
	ok := writeFakePython(t, "exit 0\n")
	if !Available(context.Background(), ok) {
		t.Fatal("假 python 导入成功时应可用")
	}
	bad := writeFakePython(t, "exit 1\n")
	if Available(context.Background(), bad) {
		t.Fatal("导入失败时应判定不可用")
	}
	if Available(context.Background(), "") {
		t.Fatal("空 python 应判定不可用")
	}
}
