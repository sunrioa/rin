#pragma once

#include "CoreMinimal.h"
#include "RinHostTypes.generated.h"

UENUM(BlueprintType)
enum class ERinActionRunStatus : uint8
{
    Queued,
    Running,
    Succeeded,
    Failed,
    Cancelled,
    Interrupted,
    Stale,
    OutcomeUnknown
};

USTRUCT(BlueprintType)
struct RINHOST_API FRinHostEpoch
{
    GENERATED_BODY()

    static constexpr int32 MaxIdentifierLength = 96;
    static constexpr int64 MaxJsonSafeInteger = 9007199254740991LL;

    UPROPERTY(BlueprintReadOnly, Category = "Rin")
    FString SessionId;

    UPROPERTY(BlueprintReadOnly, Category = "Rin")
    FString WorldId;

    UPROPERTY(BlueprintReadOnly, Category = "Rin")
    int64 HostGeneration = 0;

    UPROPERTY(BlueprintReadOnly, Category = "Rin")
    int64 WorldGeneration = 0;

    UPROPERTY(BlueprintReadOnly, Category = "Rin")
    int64 TimelineGeneration = 0;

    bool IsValid() const;
    static bool IsSafeIdentifier(const FString& Value, bool bNamespaced);
    static bool IsSafePositiveInteger(int64 Value);
    bool operator==(const FRinHostEpoch& Other) const;
    bool operator!=(const FRinHostEpoch& Other) const
    {
        return !(*this == Other);
    }
};

USTRUCT(BlueprintType)
struct RINHOST_API FRinCapabilityDescriptor
{
    GENERATED_BODY()

    UPROPERTY(EditAnywhere, BlueprintReadWrite, Category = "Rin")
    FString Id;

    UPROPERTY(EditAnywhere, BlueprintReadWrite, Category = "Rin")
    FString Version;

    UPROPERTY(EditAnywhere, BlueprintReadWrite, Category = "Rin")
    FString Digest;

    UPROPERTY(EditAnywhere, BlueprintReadWrite, Category = "Rin")
    bool bSupportsCancellation = false;

    UPROPERTY(VisibleAnywhere, BlueprintReadOnly, Category = "Rin")
    bool bActive = true;

    FString Key() const { return Id + TEXT("@") + Version; }
    bool IsValid() const;
};

USTRUCT(BlueprintType)
struct RINHOST_API FRinActionInvocation
{
    GENERATED_BODY()

    UPROPERTY(BlueprintReadWrite, Category = "Rin")
    FString OperationId;

    UPROPERTY(BlueprintReadWrite, Category = "Rin")
    FString OfferId;

    UPROPERTY(BlueprintReadWrite, Category = "Rin")
    FString CapabilityId;

    UPROPERTY(BlueprintReadWrite, Category = "Rin")
    FString CapabilityVersion;

    UPROPERTY(BlueprintReadWrite, Category = "Rin")
    FString DescriptorDigest;

    UPROPERTY(BlueprintReadWrite, Category = "Rin")
    FRinHostEpoch ExpectedEpoch;

    bool IsValid() const;
};

USTRUCT(BlueprintType)
struct RINHOST_API FRinActionRun
{
    GENERATED_BODY()

    UPROPERTY(BlueprintReadOnly, Category = "Rin")
    FRinHostEpoch Epoch;

    UPROPERTY(BlueprintReadOnly, Category = "Rin")
    FString OperationId;

    UPROPERTY(BlueprintReadOnly, Category = "Rin")
    ERinActionRunStatus Status = ERinActionRunStatus::Queued;

    UPROPERTY(BlueprintReadOnly, Category = "Rin")
    int64 ProgressSequence = 0;

    UPROPERTY(BlueprintReadOnly, Category = "Rin")
    int32 Progress = 0;

    UPROPERTY(BlueprintReadOnly, Category = "Rin")
    FString Message;
};

DECLARE_DYNAMIC_MULTICAST_DELEGATE_OneParam(
    FRinActionRunChanged,
    const FRinActionRun&,
    Run
);
