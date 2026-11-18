package catalog

import (
	"regexp"
	"strings"
	"testing"
)

// [fork-local] 附加清单必须被并入 Models，且自量化档可经 ModelScope 源正确下载。
func TestForkModelsMerged(t *testing.T) {
	digest := regexp.MustCompile(`^[0-9a-f]{64}$`)
	want := []string{
		"index-2-q3", "index-2-q4", "index-2.5-q3", "index-2.5-q4",
		"qwen3-tts-customvoice-q4", "qwen3-tts-voicedesign-q4",
		"qwen3-tts-base-0.6b-q3", "qwen3-tts-base-0.6b-q4", "qwen3-tts-base-q4",
		"yue2-3b-q3",
	}
	for _, id := range want {
		m, err := Lookup(id)
		if err != nil {
			t.Fatalf("附加档 %s 未并入 Models: %v", id, err)
		}
		if !digest.MatchString(m.SHA256) || m.Size <= 0 {
			t.Errorf("%s 摘要或大小无效: sha256=%q size=%d", id, m.SHA256, m.Size)
		}
	}
	// 单文件自量化档走 rookie6667 的 ModelScope 仓库。
	idx, err := Lookup("index-2.5-q3")
	if err != nil {
		t.Fatal(err)
	}
	if url, _ := idx.URL("modelscope"); url != "https://modelscope.cn/models/rookie6667/IndexTTS2.5-GGUF/resolve/master/index-tts2_5-q3_k.gguf" {
		t.Error("index-2.5-q3 ModelScope 下载地址错误:", url)
	}
	// yue2 多文件包：主文件来自 rookie6667，sidecars/vae 跨仓引用 audio-cpp（走 hf-mirror）。
	m, err := Lookup("yue2-3b-q3")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 6 {
		t.Fatalf("yue2-3b-q3 应含 6 个文件，实际 %d", len(m.Files))
	}
	if main, _ := m.URL("modelscope"); !strings.Contains(main, "modelscope.cn/models/rookie6667/Yue2-3B-GGUF") {
		t.Error("yue2 主文件应从 rookie6667 下载:", main)
	}
	side, _ := m.PackageFileURL("modelscope", m.Files[1])
	if !strings.Contains(side, "hf-mirror.com/audio-cpp/Yue2-3B-GGUF") {
		t.Error("yue2 sidecar 应从 audio-cpp 仓库下载:", side)
	}
}
