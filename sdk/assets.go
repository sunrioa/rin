// Package sdkassets exposes source-first Rin SDKs as read-only embedded
// assets for offline integration scaffolding.
package sdkassets

import (
	"embed"

	"github.com/sunrioa/rin/protocol"
)

// Version identifies the SDK source snapshot embedded in FS. The projection
// tests keep the Java, C#, and Lua client constants aligned with this release.
const Version = protocol.ContractReleaseVersion

// FS contains only the explicitly reviewed Java, C#, and Lua SDK sources and
// project metadata.
//
//go:embed java/src/main/java/io/github/sunrioa/rin/HostCapabilities.java
//go:embed java/src/main/java/io/github/sunrioa/rin/HostProfile.java
//go:embed java/src/main/java/io/github/sunrioa/rin/JsonCodec.java
//go:embed java/src/main/java/io/github/sunrioa/rin/OutcomeOutboxEntry.java
//go:embed java/src/main/java/io/github/sunrioa/rin/PendingTurn.java
//go:embed java/src/main/java/io/github/sunrioa/rin/ProposalFreshness.java
//go:embed java/src/main/java/io/github/sunrioa/rin/ResolvedPendingTurn.java
//go:embed java/src/main/java/io/github/sunrioa/rin/RinApiException.java
//go:embed java/src/main/java/io/github/sunrioa/rin/RinClient.java
//go:embed java/src/main/java/io/github/sunrioa/rin/RinConfigurationException.java
//go:embed java/src/main/java/io/github/sunrioa/rin/RinException.java
//go:embed java/src/main/java/io/github/sunrioa/rin/RinProtocolException.java
//go:embed java/src/main/java/io/github/sunrioa/rin/RinTransportException.java
//go:embed java/src/main/java/io/github/sunrioa/rin/WorkflowCoordinator.java
//go:embed java/src/main/java/io/github/sunrioa/rin/WorkflowStore.java
//go:embed csharp/Rin.Client/AssemblyInfo.cs
//go:embed csharp/Rin.Client/AuthoritativeWorkflow.cs
//go:embed csharp/Rin.Client/HostCapabilities.cs
//go:embed csharp/Rin.Client/NetStandardPolyfills.cs
//go:embed csharp/Rin.Client/OpaqueSnapshots.cs
//go:embed csharp/Rin.Client/ProposalFreshness.cs
//go:embed csharp/Rin.Client/ProtocolModels.cs
//go:embed csharp/Rin.Client/Rin.Client.csproj
//go:embed csharp/Rin.Client/packages.lock.json
//go:embed csharp/Rin.Client/RinBinding.cs
//go:embed csharp/Rin.Client/RinCapabilities.cs
//go:embed csharp/Rin.Client/RinClient.cs
//go:embed csharp/Rin.Client/RinClientOptions.cs
//go:embed csharp/Rin.Client/RinException.cs
//go:embed csharp/Rin.Client/RinIds.cs
//go:embed csharp/Rin.Client/SessionTransfer.cs
//go:embed csharp/Rin.Client/WorkflowCoordinator.cs
//go:embed lua/rin.lua
var FS embed.FS
