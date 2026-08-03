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

bool OfferMatchesInvocation(
    const FRinActionOffer& Offer,
    const FRinActionInvocation& Invocation
)
{
    return Offer.OfferId == Invocation.OfferId &&
        Offer.DecisionWindowId == Invocation.DecisionWindowId &&
        Offer.ActorId == Invocation.ActorId &&
        Offer.CapabilityId == Invocation.CapabilityId &&
        Offer.CapabilityVersion == Invocation.CapabilityVersion &&
        Offer.DescriptorDigest == Invocation.DescriptorDigest &&
        Offer.OfferDigest == Invocation.OfferDigest &&
        Offer.ExpectedEpoch == Invocation.ExpectedEpoch &&
        Offer.ObservationSequence == Invocation.ObservationSequence &&
        Offer.DeadlineClock == Invocation.DeadlineClock &&
        Offer.DeadlineValue == Invocation.DeadlineValue;
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
        !FRinHostEpoch::IsSafePositiveInteger(HostGeneration))
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
    ActionOffers.Reset();
    AuthoritativeClocks.Reset();
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
        !FRinHostEpoch::IsSafePositiveInteger(WorldGeneration) ||
        !FRinHostEpoch::IsSafePositiveInteger(TimelineGeneration))
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
    ActionOffers.Reset();
    AuthoritativeClocks.Reset();
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
    ActionOffers.Reset();
    AuthoritativeClocks.Reset();
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
    const FString CapabilityKey = Id + TEXT("@") + Version;
    TArray<FString> InvalidOffers;
    for (const TPair<FString, FRinActionOffer>& Entry : ActionOffers)
    {
        if (Entry.Value.CapabilityId + TEXT("@") +
            Entry.Value.CapabilityVersion == CapabilityKey)
        {
            InvalidOffers.Add(Entry.Key);
        }
    }
    for (const FString& OfferId : InvalidOffers)
    {
        ActionOffers.Remove(OfferId);
    }
    MarkQueuedRunsStaleForCapability(CapabilityKey);
    return true;
}

bool URinHostSubsystem::ReplaceActionOffers(
    const TArray<FRinActionOffer>& Offers
)
{
    check(IsInGameThread());
    if (!Epoch.IsValid() ||
        Offers.IsEmpty() ||
        Offers.Num() > MaxTrackedActionOffers)
    {
        return false;
    }
    TMap<FString, FRinActionOffer> Replacement;
    for (const FRinActionOffer& Offer : Offers)
    {
        if (!Offer.IsValid() ||
            Offer.ExpectedEpoch != Epoch ||
            Replacement.Contains(Offer.OfferId))
        {
            return false;
        }
        const FRinCapabilityDescriptor* Descriptor = Capabilities.Find(
            Offer.CapabilityId + TEXT("@") + Offer.CapabilityVersion
        );
        const int64* CurrentClock = AuthoritativeClocks.Find(
            Offer.DeadlineClock
        );
        if (Descriptor == nullptr ||
            !Descriptor->bActive ||
            Descriptor->Digest != Offer.DescriptorDigest ||
            CurrentClock == nullptr ||
            *CurrentClock > Offer.DeadlineValue)
        {
            return false;
        }
        Replacement.Add(Offer.OfferId, Offer);
    }
    ActionOffers = MoveTemp(Replacement);
    return true;
}

bool URinHostSubsystem::ObserveAuthoritativeClock(
    const FString& Clock,
    const int64 Value
)
{
    check(IsInGameThread());
    if (!FRinHostEpoch::IsSafeIdentifier(Clock, false) ||
        !FRinHostEpoch::IsSafeNonNegativeInteger(Value))
    {
        return false;
    }
    if (const int64* Existing = AuthoritativeClocks.Find(Clock))
    {
        if (Value < *Existing)
        {
            return false;
        }
    }
    AuthoritativeClocks.Add(Clock, Value);

    TArray<FString> ExpiredOffers;
    for (const TPair<FString, FRinActionOffer>& Entry : ActionOffers)
    {
        if (Entry.Value.DeadlineClock == Clock &&
            Value > Entry.Value.DeadlineValue)
        {
            ExpiredOffers.Add(Entry.Key);
        }
    }
    for (const FString& OfferId : ExpiredOffers)
    {
        ActionOffers.Remove(OfferId);
    }
    MarkExpiredQueuedRuns(Clock, Value);
    return true;
}

void URinHostSubsystem::ForkTimeline()
{
    check(IsInGameThread());
    if (!Epoch.IsValid() ||
        Epoch.TimelineGeneration >= FRinHostEpoch::MaxJsonSafeInteger)
    {
        return;
    }
    ++Epoch.TimelineGeneration;
    ActionOffers.Reset();
    AuthoritativeClocks.Reset();
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
    const FRinActionOffer* Offer = ActionOffers.Find(Invocation.OfferId);
    const int64* CurrentClock = AuthoritativeClocks.Find(
        Invocation.DeadlineClock
    );
    if (Offer == nullptr ||
        !OfferMatchesInvocation(*Offer, Invocation) ||
        CurrentClock == nullptr ||
        *CurrentClock > Invocation.DeadlineValue)
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
    ActionOffers.Remove(Invocation.OfferId);
    AppliedOperationIds.Add(Invocation.OperationId);
    FRinActionRun Queued;
    Queued.Epoch = Invocation.ExpectedEpoch;
    Queued.OperationId = Invocation.OperationId;
    Queued.CapabilityId = Invocation.CapabilityId;
    Queued.CapabilityVersion = Invocation.CapabilityVersion;
    Queued.DescriptorDigest = Invocation.DescriptorDigest;
    Queued.DeadlineClock = Invocation.DeadlineClock;
    Queued.DeadlineValue = Invocation.DeadlineValue;
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
        !FRinHostEpoch::IsSafePositiveInteger(ProgressSequence) ||
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
    if (Current->Status == ERinActionRunStatus::Queued &&
        Status == ERinActionRunStatus::Running &&
        !IsQueuedRunAuthorized(*Current))
    {
        if (Current->ProgressSequence < FRinHostEpoch::MaxJsonSafeInteger)
        {
            FRinActionRun Stale = *Current;
            Stale.Status = ERinActionRunStatus::Stale;
            ++Stale.ProgressSequence;
            Stale.Message = TEXT(
                "Queued invocation authorization changed before execution."
            );
            Runs.Add(OperationId, Stale);
            OnActionRunChanged.Broadcast(Stale);
        }
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

bool URinHostSubsystem::IsQueuedRunAuthorized(
    const FRinActionRun& Run
) const
{
    if (Run.Status != ERinActionRunStatus::Queued ||
        Run.Epoch != Epoch)
    {
        return false;
    }
    const FRinCapabilityDescriptor* Descriptor = Capabilities.Find(
        Run.CapabilityId + TEXT("@") + Run.CapabilityVersion
    );
    const int64* CurrentClock = AuthoritativeClocks.Find(Run.DeadlineClock);
    return Descriptor != nullptr &&
        Descriptor->bActive &&
        Descriptor->Digest == Run.DescriptorDigest &&
        CurrentClock != nullptr &&
        *CurrentClock <= Run.DeadlineValue;
}

void URinHostSubsystem::MarkQueuedRunsStaleForCapability(
    const FString& CapabilityKey
)
{
    TArray<FRinActionRun> Changed;
    for (const TPair<FString, FRinActionRun>& Entry : Runs)
    {
        const FRinActionRun& Current = Entry.Value;
        if (Current.Status != ERinActionRunStatus::Queued ||
            Current.CapabilityId + TEXT("@") +
                Current.CapabilityVersion != CapabilityKey ||
            Current.ProgressSequence >= FRinHostEpoch::MaxJsonSafeInteger)
        {
            continue;
        }
        FRinActionRun Stale = Current;
        Stale.Status = ERinActionRunStatus::Stale;
        ++Stale.ProgressSequence;
        Stale.Message = TEXT(
            "Capability was revoked before queued invocation execution."
        );
        Changed.Add(MoveTemp(Stale));
    }
    for (const FRinActionRun& Stale : Changed)
    {
        Runs.Add(Stale.OperationId, Stale);
        OnActionRunChanged.Broadcast(Stale);
    }
}

void URinHostSubsystem::MarkExpiredQueuedRuns(
    const FString& Clock,
    const int64 Value
)
{
    TArray<FRinActionRun> Changed;
    for (const TPair<FString, FRinActionRun>& Entry : Runs)
    {
        const FRinActionRun& Current = Entry.Value;
        if (Current.Status != ERinActionRunStatus::Queued ||
            Current.DeadlineClock != Clock ||
            Value <= Current.DeadlineValue ||
            Current.ProgressSequence >= FRinHostEpoch::MaxJsonSafeInteger)
        {
            continue;
        }
        FRinActionRun Stale = Current;
        Stale.Status = ERinActionRunStatus::Stale;
        ++Stale.ProgressSequence;
        Stale.Message = TEXT(
            "Decision Window expired before queued invocation execution."
        );
        Changed.Add(MoveTemp(Stale));
    }
    for (const FRinActionRun& Stale : Changed)
    {
        Runs.Add(Stale.OperationId, Stale);
        OnActionRunChanged.Broadcast(Stale);
    }
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
        if (Entry.Value.ProgressSequence >=
            FRinHostEpoch::MaxJsonSafeInteger)
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
    ActionOffers.Reset();
    AuthoritativeClocks.Reset();
    MarkActiveRunsOutcomeUnknown(
        TEXT("World changed before action completion.")
    );
}
