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
推进和可强制执行的角色边界。其集成测试会通过内部 Agent Runtime 和 MCP 内存会话
分别驱动该场景。

~~~sh
go test ./examples/adapters/story
~~~

## Terminal Story

[terminal-story](terminal-story/) 是可运行的端到端切片，用于证明嵌入式 Host 与
携带外部决策权限的进程内 Controller 会经过共用策略和权威 Operation 执行链。

~~~sh
go run ./examples/terminal-story --line "The light feels familiar." --json
~~~

真实游戏 Adapter 应位于自己的仓库。按照 [Host 脚手架流程](../docs/host-scaffolding.zh-CN.md)
生成可移植契约骨架：

~~~sh
./bin/rin init host -engine custom -runtime java -id my_game_host -output ./my-game-host
~~~

然后在真实游戏中验证权威线程、存档身份、策略、幂等、取消、重启与急停边界。
