// Package sdkassets exposes source-first Rin SDKs as read-only embedded
// assets for offline integration scaffolding.
package sdkassets

import (
	"embed"

	"github.com/sunrioa/rin/release"
)

// Version identifies the SDK source snapshot embedded in FS. The projection
// tests keep the Java, C#, and Lua client constants aligned with this release.
const Version = release.Version

// FS contains only the explicitly reviewed Java, C#, and Lua SDK sources and
// project metadata.
//
//go:embed java/src/main/java/io/github/sunrioa/rin/HostActionContract.java
//go:embed java/src/main/java/io/github/sunrioa/rin/HostControlSession.java
//go:embed java/src/main/java/io/github/sunrioa/rin/HostControlTransport.java
//go:embed java/src/main/java/io/github/sunrioa/rin/JsonValueCodec.java
//go:embed java/src/main/java/io/github/sunrioa/rin/JsonValues.java
//go:embed java/src/main/java/io/github/sunrioa/rin/RinAgentClient.java
//go:embed java/src/main/java/io/github/sunrioa/rin/RinApiException.java
//go:embed java/src/main/java/io/github/sunrioa/rin/RinConfigurationException.java
//go:embed java/src/main/java/io/github/sunrioa/rin/RinControlClient.java
//go:embed java/src/main/java/io/github/sunrioa/rin/RinException.java
//go:embed java/src/main/java/io/github/sunrioa/rin/RinProtocolException.java
//go:embed java/src/main/java/io/github/sunrioa/rin/RinTransportException.java
//go:embed csharp/Rin.Client/AssemblyInfo.cs
//go:embed csharp/Rin.Client/NetStandardPolyfills.cs
//go:embed csharp/Rin.Client/Rin.Client.csproj
//go:embed csharp/Rin.Client/packages.lock.json
//go:embed csharp/Rin.Client/RinControlClient.cs
//go:embed csharp/Rin.Client/RinControlClientOptions.cs
//go:embed csharp/Rin.Client/RinException.cs
//go:embed lua/rin.lua
var FS embed.FS
