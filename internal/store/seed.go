package store

// [fork-local] 首启种子数据：开箱即用的角色/音色。上游 yovoice 里不存在本文件,合并时保留即可。
//
// 设计约束：
//   - 只门控"写入数据",不删/改任何上游 schema 或 RPC;
//   - 种子标记放在 root 下一个隐藏文件 .fork-seeded(不侵入 DB schema);
//   - 用户把全部 Character 删光后重启不会被再次塞回;
//   - install.sh 可预置示例 WAV 到 root/seed/,App 端自动导入为 Voice。

import (
	"os"
	"path/filepath"
	"slices"
	"time"

	"yovoice/internal/audio"
	"yovoice/internal/diag"
	"yovoice/internal/platform"
	"yovoice/internal/schema"
)

const (
	seedMarker  = ".fork-seeded"
	seedModelID = "qwen3-tts-customvoice-q8"
)

// forkSeedCharacter 定义一条预置角色的固定属性。
type forkSeedCharacter struct {
	Name     string
	Speaker  string // 对应 qwen3-tts-customvoice 的内置 voice_id
	Language string // SynthesisLanguage
	DemoText string
}

// forkSeedVoice 定义一条预置参考音色,对应 root/seed/<File> 中的 WAV 文件。
type forkSeedVoice struct {
	File string // root/seed/ 下的文件名(含后缀)
	Name string
	Desc string // ReferenceText
}

// 预置角色列表(首启写入)。名字/示例文本统一用英文,便于国际用户阅读。
var forkSeedCharacters = []forkSeedCharacter{
	{Name: "Vivian", Speaker: "Vivian", Language: "en", DemoText: "Hi, I'm Vivian. It's great to be your voice."},
	{Name: "Dylan", Speaker: "Dylan", Language: "en", DemoText: "Hey, I'm Dylan. What would you like to talk about today?"},
}

// 预置参考音色(install.sh 放 WAV 到 root/seed/ 后首启自动导入;没有对应文件则跳过)。
var forkSeedVoices = []forkSeedVoice{
	{File: "voice_01.wav", Name: "Bright Female", Desc: "Clear, friendly voice"},
	{File: "voice_03.wav", Name: "Gentle Female", Desc: "Soft and calm, good for narration"},
	{File: "voice_04.wav", Name: "Steady Male", Desc: "Deep and composed, good for commentary"},
}

// SeedFork 在 DB 打开后、服务对外暴露前调用：
// 如果 root/seedMarker 不存在,写入种子数据并创建标记。
func (s *Store) SeedFork() {
	marker := filepath.Join(s.Root, seedMarker)
	if _, e := os.Stat(marker); e == nil {
		return // 已种过,不重复
	}
	now := time.Now().UTC()
	e := s.Update(func(state *schema.State) {
		runtime := schema.DefaultDraft().SynthesisSettings // [fork-local] 以默认草稿为基,保证 emotions/mode 等无 omitempty 字段非 null(否则前端渲染崩溃)
		// 只在完全没有角色时写入种子(用户可能通过 import 等方式先手动添加)。
		if len(state.Characters) == 0 {
			for _, seed := range forkSeedCharacters {
				settings := runtime
				settings.ModelID = seedModelID
				settings.Speaker = seed.Speaker
				settings.SynthesisLanguage = seed.Language
				state.Characters = append(state.Characters, schema.Character{
					ID:        schema.NewID(),
					Name:      seed.Name,
					Settings:  settings,
					DemoText:  seed.DemoText,
					CreatedAt: now,
					UpdatedAt: now,
				})
			}
		}
		// 导入 root/seed/ 下的 WAV 为 Voice 记录(install.sh 放好的参考音频)。
		seedDir := filepath.Join(s.Root, "seed")
		for _, sv := range forkSeedVoices {
			src := filepath.Join(seedDir, sv.File)
			if _, e := os.Stat(src); e != nil {
				continue // 文件不存在则跳过
			}
			// 检查是否已经导入(同名)——一般进到这里不会,但保险。
			baseName := "seed-" + sv.File
			if slices.ContainsFunc(state.Voices, func(v schema.Voice) bool { return v.FileName == baseName }) {
				continue
			}
			dst, e := s.MediaPath("voices", baseName)
			if e != nil {
				continue
			}
			// 复制文件到 voices/ 下(种子来源不删原文件)。
			data, e := os.ReadFile(src)
			if e != nil {
				continue
			}
			if e = platform.WriteFileAtomic(dst, data, 0600); e != nil {
				continue
			}
			duration, e := audio.Duration(dst)
			if e != nil {
				duration = 0
			}
			state.Voices = append(state.Voices, schema.Voice{
				ID:            schema.NewID(),
				Name:          sv.Name,
				FileName:      baseName,
				Duration:      duration,
				ReferenceText: sv.Desc,
				Source:        "seed",
			})
		}
	}, true)
	if e != nil {
		diag.Log(s.Root, "fork.seed_error", "error", diag.Error(e))
		return
	}
	// 写标记——即使内容只有一行时间戳也确保原子写,防止半途崩溃后重复种子。
	_ = platform.WriteFileAtomic(marker, []byte(now.Format(time.RFC3339)), 0600)
	diag.Log(s.Root, "fork.seeded", "characters", len(forkSeedCharacters))
}
