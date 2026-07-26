namespace Rin.Client;

public enum HostDurabilityProfile
{
    Advisory = 0,
    IdempotentAction = 1,
    TransactionalAction = 2,
}

public sealed record HostDurability(
    int Version,
    HostDurabilityProfile Profile,
    bool StableIdentity,
    bool DurableBeforeNetwork,
    bool DurableOutbox,
    bool IdempotentApply,
    bool AtomicApplyAndOutbox)
{
    public static HostDurability Advisory(bool stableIdentity = false) =>
        new(1, HostDurabilityProfile.Advisory, stableIdentity, false, false, false, false);

    public static HostDurability IdempotentAction() =>
        new(1, HostDurabilityProfile.IdempotentAction, true, true, true, true, false);

    public static HostDurability TransactionalAction(bool idempotentApply = false) =>
        new(1, HostDurabilityProfile.TransactionalAction, true, true, true, idempotentApply, true);

    public HostDurability Validate()
    {
        if (Version != 1 || !Enum.IsDefined(typeof(HostDurabilityProfile), Profile))
        {
            throw new RinConfigurationException(
                "invalid_host_durability",
                "Host durability has an unsupported version or profile");
        }
        if (Profile == HostDurabilityProfile.IdempotentAction &&
            !(StableIdentity && DurableBeforeNetwork && DurableOutbox && IdempotentApply))
        {
            throw new RinConfigurationException(
                "invalid_host_durability",
                "IdempotentAction requires stable durable state, Outbox, and idempotent apply");
        }
        if (Profile == HostDurabilityProfile.TransactionalAction &&
            !(StableIdentity && DurableBeforeNetwork && DurableOutbox && AtomicApplyAndOutbox))
        {
            throw new RinConfigurationException(
                "invalid_host_durability",
                "TransactionalAction requires stable durable state, Outbox, and atomic settlement");
        }
        return this;
    }

    public void Require(HostDurabilityProfile requiredDurability)
    {
        Validate();
        if (!Enum.IsDefined(typeof(HostDurabilityProfile), requiredDurability))
        {
            throw new RinConfigurationException(
                "invalid_host_durability_profile",
                "Required host durability profile is unknown");
        }
        if ((int)Profile < (int)requiredDurability)
        {
            throw new RinConfigurationException(
                "host_durability_insufficient",
                $"Action requires {Label(requiredDurability)}, but host provides {Label(Profile)}");
        }
    }

    public static string Label(HostDurabilityProfile profile) => profile switch
    {
        HostDurabilityProfile.Advisory => "advisory",
        HostDurabilityProfile.IdempotentAction => "idempotent-action",
        HostDurabilityProfile.TransactionalAction => "transactional-action",
        _ => throw new RinConfigurationException(
            "invalid_host_durability_profile",
            "Host durability profile is unknown"),
    };
}
