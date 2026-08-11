# Rin 示例

[English](README.md)

仓库内示例只验证与游戏引擎无关的 Harness V2 契约，不再复制具体引擎工程，也不宣称
已经支持某个特定游戏的生产环境。

## Grid Adapter

[adapters/grid](adapters/grid/) 是紧凑的参考实现，覆盖 Observation、能力绑定、
Effect Policy、资源采集、容器转移、取消、重启拒绝和权威 Outcome。

~~~sh
go test ./examples/adapters/grid
~~~

## Story Adapter

[adapters/story](adapters/story/) 把同一套 HostKit 契约用于对白、关系变化、剧情
推进和可强制执行的角色边界。

~~~sh
go test ./examples/adapters/story
~~~

## Terminal Story

[terminal-story](terminal-story/) 是可运行的端到端切片，用于证明内部 Agent
Runtime 与外部 MCP Client 共用相同的策略和权威 Operation 执行链。

~~~sh
go run ./examples/terminal-story --line "The light feels familiar." --json
~~~

真实游戏 Adapter 应位于自己的仓库。先用 "rin init host" 生成可移植契约骨架，再
在真实游戏中验证权威线程、存档身份、策略、幂等、取消、重启与急停边界。
