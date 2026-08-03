#include "RinHostTypes.h"

namespace
{
bool IsLowerHexDigest(const FString& Value)
{
    if (Value.Len() != 64)
    {
        return false;
    }
    for (const TCHAR Character : Value)
    {
        if (!FChar::IsHexDigit(Character) || FChar::IsUpper(Character))
        {
            return false;
        }
    }
    return true;
}

bool IsVersionIdentifier(
    const FString& Value,
    const bool bRejectNumericLeadingZero
)
{
    if (Value.IsEmpty())
    {
        return false;
    }
    bool bNumeric = true;
    for (const TCHAR Character : Value)
    {
        const bool bAlphaNumeric =
            (Character >= TEXT('0') && Character <= TEXT('9')) ||
            (Character >= TEXT('A') && Character <= TEXT('Z')) ||
            (Character >= TEXT('a') && Character <= TEXT('z'));
        if (!bAlphaNumeric && Character != TEXT('-'))
        {
            return false;
        }
        bNumeric = bNumeric &&
            Character >= TEXT('0') &&
            Character <= TEXT('9');
    }
    return !bRejectNumericLeadingZero ||
        !bNumeric ||
        Value.Len() == 1 ||
        Value[0] != TEXT('0');
}

bool IsVersionSuffix(const FString& Value, const bool bPrerelease)
{
    TArray<FString> Identifiers;
    Value.ParseIntoArray(Identifiers, TEXT("."), false);
    if (Identifiers.IsEmpty())
    {
        return false;
    }
    for (const FString& Identifier : Identifiers)
    {
        if (!IsVersionIdentifier(Identifier, bPrerelease))
        {
            return false;
        }
    }
    return true;
}

bool IsExactVersion(const FString& Value)
{
    FString CoreAndPrerelease = Value;
    FString Build;
    if (Value.Split(TEXT("+"), &CoreAndPrerelease, &Build))
    {
        if (Build.Contains(TEXT("+")) || !IsVersionSuffix(Build, false))
        {
            return false;
        }
    }
    FString Core = CoreAndPrerelease;
    FString Prerelease;
    if (CoreAndPrerelease.Split(TEXT("-"), &Core, &Prerelease))
    {
        if (!IsVersionSuffix(Prerelease, true))
        {
            return false;
        }
    }
    TArray<FString> Numbers;
    Core.ParseIntoArray(Numbers, TEXT("."), false);
    if (Numbers.Num() != 3)
    {
        return false;
    }
    for (const FString& Number : Numbers)
    {
        if (Number.IsEmpty() ||
            (Number.Len() > 1 && Number[0] == TEXT('0')))
        {
            return false;
        }
        for (const TCHAR Character : Number)
        {
            if (Character < TEXT('0') || Character > TEXT('9'))
            {
                return false;
            }
        }
    }
    return true;
}
} // namespace

bool FRinHostEpoch::IsValid() const
{
    return IsSafeIdentifier(SessionId, false) &&
        IsSafeIdentifier(WorldId, false) &&
        IsSafePositiveInteger(HostGeneration) &&
        IsSafePositiveInteger(WorldGeneration) &&
        IsSafePositiveInteger(TimelineGeneration);
}

bool FRinHostEpoch::IsSafeIdentifier(
    const FString& Value,
    const bool bNamespaced
)
{
    if (Value.IsEmpty() ||
        Value.Len() > MaxIdentifierLength ||
        Value[0] < TEXT('a') ||
        Value[0] > TEXT('z') ||
        (bNamespaced && !Value.Contains(TEXT("."))))
    {
        return false;
    }
    bool bPreviousWasSeparator = false;
    for (const TCHAR Character : Value)
    {
        const bool bAlphaNumeric =
            (Character >= TEXT('a') && Character <= TEXT('z')) ||
            (Character >= TEXT('0') && Character <= TEXT('9'));
        const bool bSeparator =
            Character == TEXT('.') ||
            Character == TEXT('_') ||
            Character == TEXT('-');
        if ((!bAlphaNumeric && !bSeparator) ||
            (bSeparator && bPreviousWasSeparator))
        {
            return false;
        }
        bPreviousWasSeparator = bSeparator;
    }
    return !bPreviousWasSeparator;
}

bool FRinHostEpoch::IsSafePositiveInteger(const int64 Value)
{
    return Value > 0 && Value <= MaxJsonSafeInteger;
}

bool FRinHostEpoch::IsSafeNonNegativeInteger(const int64 Value)
{
    return Value >= 0 && Value <= MaxJsonSafeInteger;
}

bool FRinHostEpoch::operator==(const FRinHostEpoch& Other) const
{
    return SessionId == Other.SessionId &&
        WorldId == Other.WorldId &&
        HostGeneration == Other.HostGeneration &&
        WorldGeneration == Other.WorldGeneration &&
        TimelineGeneration == Other.TimelineGeneration;
}

bool FRinCapabilityDescriptor::IsValid() const
{
    return FRinHostEpoch::IsSafeIdentifier(Id, true) &&
        IsExactVersion(Version) &&
        IsLowerHexDigest(Digest) &&
        bActive;
}

bool FRinActionOffer::IsValid() const
{
    return FRinHostEpoch::IsSafeIdentifier(OfferId, false) &&
        FRinHostEpoch::IsSafeIdentifier(DecisionWindowId, false) &&
        FRinHostEpoch::IsSafeIdentifier(ActorId, false) &&
        FRinHostEpoch::IsSafeIdentifier(CapabilityId, true) &&
        IsExactVersion(CapabilityVersion) &&
        IsLowerHexDigest(DescriptorDigest) &&
        IsLowerHexDigest(OfferDigest) &&
        ExpectedEpoch.IsValid() &&
        FRinHostEpoch::IsSafePositiveInteger(ObservationSequence) &&
        FRinHostEpoch::IsSafeIdentifier(DeadlineClock, false) &&
        FRinHostEpoch::IsSafePositiveInteger(DeadlineValue);
}

bool FRinActionInvocation::IsValid() const
{
    return FRinHostEpoch::IsSafeIdentifier(OperationId, false) &&
        FRinHostEpoch::IsSafeIdentifier(OfferId, false) &&
        FRinHostEpoch::IsSafeIdentifier(DecisionWindowId, false) &&
        FRinHostEpoch::IsSafeIdentifier(ActorId, false) &&
        FRinHostEpoch::IsSafeIdentifier(CapabilityId, true) &&
        IsExactVersion(CapabilityVersion) &&
        IsLowerHexDigest(DescriptorDigest) &&
        IsLowerHexDigest(OfferDigest) &&
        ExpectedEpoch.IsValid() &&
        FRinHostEpoch::IsSafePositiveInteger(ObservationSequence) &&
        FRinHostEpoch::IsSafeIdentifier(DeadlineClock, false) &&
        FRinHostEpoch::IsSafePositiveInteger(DeadlineValue);
}
