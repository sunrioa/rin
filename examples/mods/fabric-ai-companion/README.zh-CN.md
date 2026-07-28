# Rin AI 伙伴

这是 Minecraft 26.2 的 Fabric 服务端/单机模组。它会生成一个有实体碰撞和寻路的玩家外形伙伴，聊天内容通过本地 Rin 进程交给 OpenAI 兼容模型。默认配置面向 DeepSeek，但管理员可以在游戏里修改 Base URL 和模型名。

第一阶段只做聊天、跟随、停止、召回、皮肤名保存和状态恢复。伙伴不会挖方块、拿取物品、制作、战斗、加载远处区块或建造。这些能力要等后续阶段接入真实背包、配方和生存规则后再开放。

## 版本

- Minecraft 26.2
- Fabric Loader 0.19.3
- Fabric API 0.155.2+26.2
- Java 25
- Rin 0.7.0 / `rin.protocol/v2`

## 构建

在本目录运行：

```powershell
$env:JAVA_HOME='C:\Program Files\Eclipse Adoptium\jdk-25.0.4.7-hotspot'
$env:PATH="$env:JAVA_HOME\bin;$env:PATH"
.\gradlew.bat clean check runGameTest build --no-daemon
```

可安装文件是 `build/libs/rin-ai-companion-0.1.0.jar`。

## PCL2 安装

当前实例目录：

```text
E:\ruanjian\pcl\.minecraft\versions\26.2-Fabric 0.19.3
```

把模组 JAR 放进该实例的 `mods` 目录。保留现有的 `fabric-api-0.155.2+26.2.jar`。

Rin 可执行文件放在：

```text
<实例目录>\rin\rin.exe
```

启动 PCL2 前设置模型密钥。密钥只从环境变量读取，不会写入命令、配置、存档或日志：

```powershell
[Environment]::SetEnvironmentVariable('RIN_MODEL_API_KEY', '你的密钥', 'User')
```

设置后彻底退出并重新打开 PCL2，让启动器继承新的环境变量。

## 使用

```text
/companion spawn
/companion recall
/companion pause
/companion resume
/companion status
/companion skin <正版玩家名>
@伙伴 你好，跟我一起走吧
```

每名玩家只能有一个伙伴。游戏指令按伙伴主人校验，其他玩家不能改它的模式。

管理员模型命令：

```text
/companion model show
/companion model baseurl https://api.deepseek.com/v1
/companion model name deepseek-chat
/companion model apply
```

`baseurl` 只接受 HTTPS；HTTP 仅允许 `localhost`、`127.0.0.1` 和 `::1`。没有 API Key 命令。

## 数据与故障处理

模型配置保存在实例的 `config/rin-ai-companion.properties`。伙伴身份、主人、模式、Session、Pending Turn 和 Outcome Outbox 随世界保存。

Rin 或模型不可用时，Minecraft 不会退出。伙伴会返回固定中文短句，管理员可检查 `rin/rin.exe`、`RIN_MODEL_API_KEY` 和 `/companion model show`，修正后执行 `/companion model apply`。

不要把世界存档、模型密钥、`latest.log` 或本地配置提交到仓库。
