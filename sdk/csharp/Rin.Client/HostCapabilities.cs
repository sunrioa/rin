namespace Rin.Client;

public enum HostProfile
{
    Advisory = 0,
    IdempotentAction = 1,
    TransactionalAction = 2,
}

public sealed record HostCapabilities(
    int Version,
    HostProfile Profile,
    bool StableIdentity,
    bool DurableBeforeNetwork,
    bool DurableOutbox,
    bool IdempotentApply,
    bool AtomicApplyAndOutbox)
{
    public static HostCapabilities Advisory(bool stableIdentity = false) =>
        new(1, HostProfile.Advisory, stableIdentity, false, false, false, false);

    public static HostCapabilities IdempotentAction() =>
        new(1, HostProfile.IdempotentAction, true, true, true, true, false);

    public static HostCapabilities TransactionalAction(bool idempotentApply = false) =>
        new(1, HostProfile.TransactionalAction, true, true, true, idempotentApply, true);

    public HostCapabilities Validate()
    {
        if (Version != 1 || !Enum.IsDefined(typeof(HostProfile), Profile))
        {
            throw new RinConfigurationException(
                "invalid_host_capabilities",
                "Host capabilities have an unsupported version or profile");
        }
        if (Profile == HostProfile.IdempotentAction &&
            !(StableIdentity && DurableBeforeNetwork && DurableOutbox && IdempotentApply))
        {
            throw new RinConfigurationException(
                "invalid_host_capabilities",
                "IdempotentAction requires stable durable state, Outbox, and idempotent apply");
        }
        if (Profile == HostProfile.TransactionalAction &&
            !(StableIdentity && DurableBeforeNetwork && DurableOutbox && AtomicApplyAndOutbox))
        {
            throw new RinConfigurationException(
                "invalid_host_capabilities",
                "TransactionalAction requires stable durable state, Outbox, and atomic settlement");
        }
        return this;
    }

    public void Require(HostProfile requiredProfile)
    {
        Validate();
        if (!Enum.IsDefined(typeof(HostProfile), requiredProfile))
        {
            throw new RinConfigurationException(
                "invalid_host_profile",
                "Required host profile is unknown");
        }
        if ((int)Profile < (int)requiredProfile)
        {
            throw new RinConfigurationException(
                "host_capability_insufficient",
                $"Action requires {Label(requiredProfile)}, but host provides {Label(Profile)}");
        }
    }

    public static string Label(HostProfile profile) => profile switch
    {
        HostProfile.Advisory => "advisory",
        HostProfile.IdempotentAction => "idempotent-action",
        HostProfile.TransactionalAction => "transactional-action",
        _ => throw new RinConfigurationException(
            "invalid_host_profile",
            "Host profile is unknown"),
    };
}
