// [fork-local] 轻量改造版运行时开关。上游 yovoice 里不存在本文件，合并时保留即可；
// 想把某个入口恢复回来，把对应常量改成 false 重新构建，入口与后端 RPC 都还在。
//
// 使用约定：只用于门控渲染，不删后端；不侵入业务逻辑；每个使用处都写成
// `forkHideXxx ? null : <原有 JSX>`（或从数组里过滤掉该条），便于上游 diff 一眼看出差异。

// forkHideCommunity：控制「分享到社区」相关的全部前端入口（播放栏分享按钮、
// 素材库历史菜单里的分享项、设置 › 社区 标签、Ocho 完成时的分享提示）。
// 后端 `community.login/share/logout` 等 RPC 保持不变，随时可以打开。
export const forkHideCommunity = true;

// forkUsesAppDataDir：改造版分发时 start.sh/install.sh 已 export WORKBENCH_DATA 把数据固定到 App 目录，
// 此时「设置 › 存储 › 更改位置」走的是 storage.directory → 写 storage.json、WORKBENCH_DATA 下不会被读取（见 P0-4），
// 故隐藏该 relocate 入口；保留数据目录展示/打开文件夹/清理残留。后端 RPC 不变，改回 false 即恢复。
export const forkUsesAppDataDir = true;
