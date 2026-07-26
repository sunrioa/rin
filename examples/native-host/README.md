# Native C Host reference

[English](README.md) | [简体中文](README.zh-CN.md)

This dependency-free C99 library proves that the Rin Host Contract does not
require a managed runtime or one game engine. It implements a fixed-capacity
Capability Registry, exact descriptor binding, Epoch/deadline checks, final
TOCTOU authorization, Operation ID duplicate prevention, and ActionRun
transition rules.

Registry and workflow values borrow their string and argument pointers; the
adapter must keep that immutable storage alive for the retained operation.

It deliberately does not parse JSON, perform HTTP, allocate game objects, or
pretend to implement navigation. A real native engine maps its JSON layer and
authority-thread dispatcher around this boundary.

Build directly on macOS/Linux:

```bash
cc -std=c99 -Wall -Wextra -Wpedantic -Werror \
  -Iinclude src/rin_host.c tests/test_rin_host.c -o rin_host_test
./rin_host_test
```

On Windows Developer PowerShell:

```powershell
cl /nologo /std:c11 /W4 /WX /Iinclude `
  src\rin_host.c tests\test_rin_host.c /Fe:rin_host_test.exe
.\rin_host_test.exe
```

CMake and CTest are also supported. The example owns no shell execution path;
the commands above are developer build instructions only.
