using System.Text.Json;

namespace Rin.Client;

public interface IOpaqueSnapshotStore
{
    ValueTask PutAsync(
        string key,
        byte[] snapshot,
        CancellationToken cancellationToken = default);

    ValueTask<byte[]> GetAsync(
        string key,
        CancellationToken cancellationToken = default);
}

public sealed class OpaqueSnapshotPersistence
{
    public const int MaxBytes = 16 * 1024 * 1024;

    private static readonly JsonSerializerOptions JsonOptions = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.CamelCase,
    };

    private readonly IOpaqueSnapshotStore store;

    public OpaqueSnapshotPersistence(IOpaqueSnapshotStore store)
    {
        this.store = store ?? throw new ArgumentNullException(nameof(store));
    }

    public async ValueTask SaveAsync<T>(
        string key,
        T snapshot,
        CancellationToken cancellationToken = default)
    {
        if (snapshot is null) throw new ArgumentNullException(nameof(snapshot));
        byte[] encoded;
        try
        {
            encoded = JsonSerializer.SerializeToUtf8Bytes(snapshot, JsonOptions);
            using var document = JsonDocument.Parse(encoded, new JsonDocumentOptions
            {
                MaxDepth = 64,
            });
            if (document.RootElement.ValueKind != JsonValueKind.Object)
            {
                throw new JsonException("Snapshot must be an object");
            }
        }
        catch (Exception exception)
            when (exception is JsonException or NotSupportedException)
        {
            throw new RinProtocolException(
                "invalid_snapshot",
                "Snapshot is not a JSON object",
                exception);
        }
        if (encoded.Length > MaxBytes)
        {
            throw new RinProtocolException(
                "snapshot_too_large",
                "Complete Snapshot exceeds the 16 MiB inline limit");
        }
        await store.PutAsync(key, encoded.ToArray(), cancellationToken)
            .ConfigureAwait(false);
    }

    public async ValueTask<JsonElement> LoadAsync(
        string key,
        CancellationToken cancellationToken = default)
    {
        var encoded = await store.GetAsync(key, cancellationToken).ConfigureAwait(false);
        if (encoded is null)
        {
            throw new RinConfigurationException(
                "invalid_snapshot_store",
                "Snapshot store returned null");
        }
        if (encoded.Length > MaxBytes)
        {
            throw new RinProtocolException(
                "snapshot_too_large",
                "Stored Snapshot exceeds the 16 MiB inline limit");
        }
        try
        {
            using var document = JsonDocument.Parse(encoded, new JsonDocumentOptions
            {
                MaxDepth = 64,
            });
            if (document.RootElement.ValueKind != JsonValueKind.Object)
            {
                throw new JsonException("Snapshot must be an object");
            }
            return document.RootElement.Clone();
        }
        catch (JsonException exception)
        {
            throw new RinProtocolException(
                "invalid_snapshot",
                "Stored Snapshot is not a JSON object",
                exception);
        }
    }
}
