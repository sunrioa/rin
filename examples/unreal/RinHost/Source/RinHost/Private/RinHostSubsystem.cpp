#include "RinHostSubsystem.h"

#include "Async/Async.h"
#include "Engine/GameInstance.h"
#include "Engine/World.h"

namespace
{
bool CanTransition(
    const ERinActionRunStatus From,
    const ERinActionRunStatus To
)
{
    if (From == ERinActionRunStatus::Queued)
    {
        return To == ERinActionRunStatus::Running ||
            To == ERinActionRunStatus::Failed ||
            To == ERinActionRunStatus::Cancelled ||
            To == ERinActionRunStatus::Stale ||
            To == ERinActionRunStatus::OutcomeUnknown;
    }
    if (From == ERinActionRunStatus::Running)
    {
        return To == ERinActionRunStatus::Succeeded ||
            To == ERinActionRunStatus::Failed ||
            To == ERinActionRunStatus::Cancelled ||
            To == ERinActionRunStatus::Interrupted ||
            To == ERinActionRunStatus::Stale ||
            To == ERinActionRunStatus::OutcomeUnknown;
    }
    if (From == ERinActionRunStatus::OutcomeUnknown)
    {
        return To == ERinActionRunStatus::Succeeded ||
            To == ERinActionRunStatus::Failed ||
            To == ERinActionRunStatus::Cancelled ||
            To == ERinActionRunStatus::Interrupted ||
            To == ERinActionRunStatus::Stale;
    }
    return false;
}
} // namespace

void URinHostSubsystem::Initialize(FSubsystemCollectionBase& Collection)
{
    Super::Initialize(Collection);
    Epoch = FRinHostEpoch();
    WorldInitializedHandle =
        FWorldDelegates::OnPostWorldInitialization.AddUObject(
            this,
            &URinHostSubsystem::HandleWorldInitialized
        );
}

bool URinHostSubsystem::ConfigureHostIdentity(
    const FString& StableSessionId,
    const int64 HostGeneration
)
{
    check(IsInGameThread());
    if (!FRinHostEpoch::IsSafeIdentifier(StableSessionId, false) ||
        HostGeneration <= 0)
    {
        return false;
    }
    if (Epoch.SessionId == StableSessionId &&
        HostGeneration < Epoch.HostGeneration)
    {
        return false;
    }
    if (Epoch.SessionId == StableSessionId &&
        HostGeneration == Epoch.HostGeneration)
    {
        return true;
    }
    const bool bChangedSession =
        !Epoch.SessionId.IsEmpty() && Epoch.SessionId != StableSessionId;
    const bool bHadSession = !Epoch.SessionId.IsEmpty();
    Epoch.SessionId = StableSessionId;
    Epoch.HostGeneration = HostGeneration;
    Epoch.WorldId.Reset();
    Epoch.WorldGeneration = 0;
    Epoch.TimelineGeneration = 0;
    if (bHadSession)
    {
        MarkActiveRunsOutcomeUnknown(
            TEXT("Host identity changed before action completion.")
        );
    }
    if (bChangedSession)
    {
        AppliedOperationIds.Reset();
        Runs.Reset();
    }
    return true;
}

bool URinHostSubsystem::BindWorldIdentity(
    const FString& StableWorldId,
    const int64 WorldGeneration,
    const int64 TimelineGeneration
)
{
    check(IsInGameThread());
    if (Epoch.SessionId.IsEmpty() ||
        !FRinHostEpoch::IsSafeIdentifier(StableWorldId, false) ||
        WorldGeneration <= 0 ||
        TimelineGeneration <= 0)
    {
        return false;
    }
    if (Epoch.WorldId == StableWorldId &&
        (WorldGeneration < Epoch.WorldGeneration ||
         (WorldGeneration == Epoch.WorldGeneration &&
          TimelineGeneration < Epoch.TimelineGeneration)))
    {
        return false;
    }
    if (Epoch.WorldId == StableWorldId &&
        WorldGeneration == Epoch.WorldGeneration &&
        TimelineGeneration == Epoch.TimelineGeneration)
    {
        return true;
    }
    const bool bHadEpoch = Epoch.IsValid();
    Epoch.WorldId = StableWorldId;
    Epoch.WorldGeneration = WorldGeneration;
    Epoch.TimelineGeneration = TimelineGeneration;
    if (bHadEpoch)
    {
        MarkActiveRunsOutcomeUnknown(
            TEXT("World identity changed before action completion.")
        );
    }
    return true;
}

void URinHostSubsystem::Deinitialize()
{
    FWorldDelegates::OnPostWorldInitialization.Remove(WorldInitializedHandle);
    Epoch = FRinHostEpoch();
    MarkActiveRunsOutcomeUnknown(
        TEXT("Host stopped before action completion.")
    );
    Capabilities.Reset();
    Runs.Reset();
    AppliedOperationIds.Reset();
    Super::Deinitialize();
}

bool URinHostSubsystem::RegisterCapability(
    const FRinCapabilityDescriptor& Descriptor
)
{
    check(IsInGameThread());
    if (!Descriptor.IsValid())
    {
        return false;
    }
    if (const FRinCapabilityDescriptor* Existing =
        Capabilities.Find(Descriptor.Key()))
    {
        return Existing->bActive &&
            Existing->Digest == Descriptor.Digest;
    }
    Capabilities.Add(Descriptor.Key(), Descriptor);
    return true;
}

bool URinHostSubsystem::RevokeCapability(
    const FString& Id,
    const FString& Version
)
{
    check(IsInGameThread());
    FRinCapabilityDescriptor* Descriptor =
        Capabilities.Find(Id + TEXT("@") + Version);
    if (Descriptor == nullptr)
    {
        return false;
    }
    Descriptor->bActive = false;
    return true;
}

void URinHostSubsystem::ForkTimeline()
{
    check(IsInGameThread());
    if (!Epoch.IsValid())
    {
        return;
    }
    ++Epoch.TimelineGeneration;
    MarkActiveRunsOutcomeUnknown(
        TEXT("Timeline forked before action completion.")
    );
}

bool URinHostSubsystem::AuthorizeAndQueueInvocation(
    const FRinActionInvocation& Invocation
)
{
    check(IsInGameThread());
    if (!Invocation.IsValid() ||
        !Epoch.IsValid() ||
        Invocation.ExpectedEpoch != Epoch ||
        AppliedOperationIds.Contains(Invocation.OperationId) ||
        AppliedOperationIds.Num() >= MaxTrackedOperationIds)
    {
        return false;
    }
    const FRinCapabilityDescriptor* Descriptor = Capabilities.Find(
        Invocation.CapabilityId + TEXT("@") + Invocation.CapabilityVersion
    );
    if (Descriptor == nullptr ||
        !Descriptor->bActive ||
        Descriptor->Digest != Invocation.DescriptorDigest)
    {
        return false;
    }
    AppliedOperationIds.Add(Invocation.OperationId);
    FRinActionRun Queued;
    Queued.Epoch = Invocation.ExpectedEpoch;
    Queued.OperationId = Invocation.OperationId;
    Queued.Status = ERinActionRunStatus::Queued;
    Queued.ProgressSequence = 1;
    Queued.Progress = 0;
    Queued.Message = TEXT("Invocation authorized and queued.");
    Runs.Add(Invocation.OperationId, Queued);
    OnActionRunChanged.Broadcast(Queued);
    return true;
}

bool URinHostSubsystem::ReportRun(
    const FString& OperationId,
    const FRinHostEpoch& ExpectedEpoch,
    const ERinActionRunStatus Status,
    const int64 ProgressSequence,
    const int32 Progress,
    const FString& Message
)
{
    check(IsInGameThread());
    if (OperationId.IsEmpty() ||
        !Epoch.IsValid() ||
        ExpectedEpoch != Epoch ||
        ProgressSequence <= 0 ||
        Progress < 0 ||
        Progress > 100)
    {
        return false;
    }
    FRinActionRun* Current = Runs.Find(OperationId);
    if (Current == nullptr ||
        Current->Epoch != ExpectedEpoch)
    {
        return false;
    }
    if (!CanTransition(Current->Status, Status) ||
        ProgressSequence <= Current->ProgressSequence ||
        Progress < Current->Progress)
    {
        return false;
    }
    if ((Status == ERinActionRunStatus::Queued && Progress != 0) ||
        (Status == ERinActionRunStatus::Succeeded && Progress != 100))
    {
        return false;
    }
    FRinActionRun Next;
    Next.Epoch = ExpectedEpoch;
    Next.OperationId = OperationId;
    Next.Status = Status;
    Next.ProgressSequence = ProgressSequence;
    Next.Progress = Progress;
    Next.Message = Message;
    Runs.Add(OperationId, Next);
    OnActionRunChanged.Broadcast(Next);
    return true;
}

void URinHostSubsystem::MarkActiveRunsOutcomeUnknown(const FString& Message)
{
    TArray<FRinActionRun> Changed;
    for (const TPair<FString, FRinActionRun>& Entry : Runs)
    {
        if (Entry.Value.Status != ERinActionRunStatus::Queued &&
            Entry.Value.Status != ERinActionRunStatus::Running)
        {
            continue;
        }
        FRinActionRun Next = Entry.Value;
        Next.Status = ERinActionRunStatus::OutcomeUnknown;
        ++Next.ProgressSequence;
        Next.Message = Message;
        Changed.Add(MoveTemp(Next));
    }
    for (const FRinActionRun& Next : Changed)
    {
        Runs.Add(Next.OperationId, Next);
        OnActionRunChanged.Broadcast(Next);
    }
}

void URinHostSubsystem::DispatchToGameThread(TUniqueFunction<void()>&& Work)
{
    if (IsInGameThread())
    {
        Work();
        return;
    }
    AsyncTask(ENamedThreads::GameThread, MoveTemp(Work));
}

void URinHostSubsystem::HandleWorldInitialized(
    UWorld* World,
    const UWorld::InitializationValues InitializationValues
)
{
    static_cast<void>(InitializationValues);
    if (World == nullptr ||
        !World->IsGameWorld() ||
        World->GetGameInstance() != GetGameInstance())
    {
        return;
    }
    Epoch.WorldId.Reset();
    Epoch.WorldGeneration = 0;
    Epoch.TimelineGeneration = 0;
    MarkActiveRunsOutcomeUnknown(
        TEXT("World changed before action completion.")
    );
}
