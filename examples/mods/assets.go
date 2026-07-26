// Package modtemplates exposes the checked-in game Mod templates as
// read-only embedded assets.
package modtemplates

import "embed"

// FS contains only the explicitly reviewed template inputs. SDK sources are
// embedded separately by package sdkassets.
//
//go:embed fabric-rin-npc/build.gradle
//go:embed fabric-rin-npc/LICENSE-GRADLE.txt
//go:embed fabric-rin-npc/NOTICE-GRADLE.txt
//go:embed fabric-rin-npc/gradle.properties
//go:embed fabric-rin-npc/settings.gradle
//go:embed fabric-rin-npc/gradlew
//go:embed fabric-rin-npc/gradlew.bat
//go:embed fabric-rin-npc/gradle/wrapper/gradle-wrapper.jar
//go:embed fabric-rin-npc/gradle/wrapper/gradle-wrapper.properties
//go:embed fabric-rin-npc/src/main/java/io/github/sunrioa/rin/example/FabricServerTasks.java
//go:embed fabric-rin-npc/src/main/java/io/github/sunrioa/rin/example/FabricWorkflowStore.java
//go:embed fabric-rin-npc/src/main/java/io/github/sunrioa/rin/example/GsonJsonCodec.java
//go:embed fabric-rin-npc/src/main/java/io/github/sunrioa/rin/example/RinFabricState.java
//go:embed fabric-rin-npc/src/main/java/io/github/sunrioa/rin/example/RinNpcMod.java
//go:embed fabric-rin-npc/src/main/java/io/github/sunrioa/rin/example/RinNpcRequests.java
//go:embed fabric-rin-npc/src/main/resources/fabric.mod.json
//go:embed fabric-rin-npc/src/test/java/io/github/sunrioa/rin/example/RinFabricStateTest.java
//go:embed bepinex-rin-npc/Directory.Build.props
//go:embed bepinex-rin-npc/NuGet.config
//go:embed bepinex-rin-npc/package_bepinex.py
//go:embed bepinex-rin-npc/third-party/LICENSE-DOTNET.txt
//go:embed bepinex-rin-npc/third-party/THIRD-PARTY-NOTICES-DOTNET-STANDARD-2.0.txt
//go:embed bepinex-rin-npc/third-party/THIRD-PARTY-NOTICES-MICROSOFT-BCL-8.0.txt
//go:embed bepinex-rin-npc/third-party/THIRD-PARTY-NOTICES-MONO.txt
//go:embed bepinex-rin-npc/third-party/THIRD-PARTY-NOTICES-NUMERICS-VECTORS-4.4.txt
//go:embed bepinex-rin-npc/third-party/THIRD-PARTY-NOTICES-RUNTIME-UNSAFE-6.0.txt
//go:embed bepinex-rin-npc/third-party/THIRD-PARTY-NOTICES-TEXT-JSON-8.0.6.txt
//go:embed bepinex-rin-npc/RinNpc.BepInEx.sln
//go:embed bepinex-rin-npc/RinNpc.Core/BepInExWorkflowState.cs
//go:embed bepinex-rin-npc/RinNpc.Core/RinNpc.Core.csproj
//go:embed bepinex-rin-npc/RinNpc.Core/RinNpcRuntime.cs
//go:embed bepinex-rin-npc/RinNpc.Core/packages.lock.json
//go:embed bepinex-rin-npc/RinNpc.Core.Tests/Program.cs
//go:embed bepinex-rin-npc/RinNpc.Core.Tests/RinNpc.Core.Tests.csproj
//go:embed bepinex-rin-npc/RinNpc.Core.Tests/packages.lock.json
//go:embed bepinex-rin-npc/RinNpc.Mono/Plugin.cs
//go:embed bepinex-rin-npc/RinNpc.Mono/RinNpc.Mono.csproj
//go:embed bepinex-rin-npc/RinNpc.Mono/packages.lock.json
//go:embed bepinex-rin-npc/RinNpc.IL2CPP/Plugin.cs
//go:embed bepinex-rin-npc/RinNpc.IL2CPP/RinNpc.IL2CPP.csproj
//go:embed bepinex-rin-npc/RinNpc.IL2CPP/packages.lock.json
//go:embed luanti-rin-npc/init.lua
//go:embed luanti-rin-npc/mod.conf
//go:embed luanti-rin-npc/settingtypes.txt
//go:embed luanti-rin-npc/state.lua
//go:embed luanti-rin-npc/test_state.lua
var FS embed.FS
