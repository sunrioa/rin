# 原生 C Host 参考

[English](README.md) | [简体中文](README.zh-CN.md)

这个无依赖 C99 Library 证明 Rin Host Contract 不依赖托管 Runtime 或单一游戏
引擎。它实现固定容量 Capability Registry、精确 Descriptor Binding、
Epoch/Deadline 检查、最终 TOCTOU 授权、Operation ID 防重复与 ActionRun
状态转换规则。

Registry 与 Workflow Value 借用 String 和 Argument Pointer；Adapter 必须让
这些不可变存储在保留 Operation 的整个生命周期内保持有效。

它有意不解析 JSON、不执行 HTTP、不分配游戏对象，也不伪装实现导航。真实原生
引擎应在该边界外连接自己的 JSON Layer 与所属线程 Dispatcher。

macOS/Linux 直接构建：

```bash
cc -std=c99 -Wall -Wextra -Wpedantic -Werror \
  -Iinclude src/rin_host.c tests/test_rin_host.c -o rin_host_test
./rin_host_test
```

Windows Developer PowerShell：

```powershell
cl /nologo /std:c11 /W4 /WX /Iinclude `
  src\rin_host.c tests\test_rin_host.c /Fe:rin_host_test.exe
.\rin_host_test.exe
```

也支持 CMake 与 CTest。示例本身不拥有 Shell 执行路径；以上命令只用于开发构建。
