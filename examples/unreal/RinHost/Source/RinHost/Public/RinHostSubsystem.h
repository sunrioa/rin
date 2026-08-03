#pragma once

#include "CoreMinimal.h"
#include "Engine/World.h"
#include "Subsystems/GameInstanceSubsystem.h"
#include "RinHostTypes.h"
#include "RinHostSubsystem.generated.h"

UCLASS()
class RINHOST_API URinHostSubsystem final : public UGameInstanceSubsystem
{
    GENERATED_BODY()

public:
    virtual void Initialize(FSubsystemCollectionBase& Collection) override;
    virtual void Deinitialize() override;

    UFUNCTION(BlueprintCallable, Category = "Rin|Host")
    bool ConfigureHostIdentity(
        const FString& StableSessionId,
        int64 HostGeneration
    );

    UFUNCTION(BlueprintCallable, Category = "Rin|Host")
    bool BindWorldIdentity(
        const FString& StableWorldId,
        int64 WorldGeneration,
        int64 TimelineGeneration
    );

    UFUNCTION(BlueprintCallable, Category = "Rin|Host")
    bool RegisterCapability(const FRinCapabilityDescriptor& Descriptor);

    UFUNCTION(BlueprintCallable, Category = "Rin|Host")
    bool RevokeCapability(const FString& Id, const FString& Version);

    // Atomically replaces the current Decision Window's Host-authored offers.
    UFUNCTION(BlueprintCallable, Category = "Rin|Host")
    bool ReplaceActionOffers(const TArray<FRinActionOffer>& Offers);

    UFUNCTION(BlueprintCallable, Category = "Rin|Host")
    bool ObserveAuthoritativeClock(const FString& Clock, int64 Value);

    UFUNCTION(BlueprintPure, Category = "Rin|Host")
    FRinHostEpoch CurrentEpoch() const { return Epoch; }

    UFUNCTION(BlueprintCallable, Category = "Rin|Host")
    void ForkTimeline();

    UFUNCTION(BlueprintCallable, Category = "Rin|Host")
    bool AuthorizeAndQueueInvocation(
        const FRinActionInvocation& Invocation
    );

    UFUNCTION(BlueprintCallable, Category = "Rin|Host")
    bool ReportRun(
        const FString& OperationId,
        const FRinHostEpoch& ExpectedEpoch,
        ERinActionRunStatus Status,
        int64 ProgressSequence,
        int32 Progress,
        const FString& Message
    );

    void DispatchToGameThread(TUniqueFunction<void()>&& Work);

    UPROPERTY(BlueprintAssignable, Category = "Rin|Host")
    FRinActionRunChanged OnActionRunChanged;

private:
    void MarkActiveRunsOutcomeUnknown(const FString& Message);
    void MarkQueuedRunsStaleForCapability(const FString& CapabilityKey);
    void MarkExpiredQueuedRuns(const FString& Clock, int64 Value);
    bool IsQueuedRunAuthorized(const FRinActionRun& Run) const;

    void HandleWorldInitialized(
        UWorld* World,
        const UWorld::InitializationValues InitializationValues
    );

    FRinHostEpoch Epoch;
    TMap<FString, FRinCapabilityDescriptor> Capabilities;
    TMap<FString, FRinActionOffer> ActionOffers;
    TMap<FString, int64> AuthoritativeClocks;
    TMap<FString, FRinActionRun> Runs;
    TSet<FString> AppliedOperationIds;
    FDelegateHandle WorldInitializedHandle;

    static constexpr int32 MaxTrackedOperationIds = 4096;
    static constexpr int32 MaxTrackedActionOffers = 256;
};
