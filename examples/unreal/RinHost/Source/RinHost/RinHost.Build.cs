using UnrealBuildTool;

public sealed class RinHost : ModuleRules
{
    public RinHost(ReadOnlyTargetRules Target) : base(Target)
    {
        PCHUsage = PCHUsageMode.UseExplicitOrSharedPCHs;
        PublicDependencyModuleNames.AddRange(
            new[]
            {
                "Core",
                "CoreUObject",
                "Engine",
                "AIModule"
            });
        PrivateDependencyModuleNames.Add("GameplayTasks");
    }
}
