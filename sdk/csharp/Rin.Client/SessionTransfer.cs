using System.Text;
using System.Text.Json;

namespace Rin.Client;

internal sealed class SessionTransferExportReader
{
    private const int ControlFrameMaxBytes = 32 * 1024;
    private const int EventFrameMaxBytes = 64 * 1024 * 1024 + ControlFrameMaxBytes;

    private readonly Stream destination;
    private JsonElement manifest;
    private long eventCount;
    private JsonElement complete;

    internal SessionTransferExportReader(Stream destination)
    {
        this.destination = destination;
    }

    internal async Task<JsonElement> CopyAsync(
        Stream source,
        CancellationToken cancellationToken)
    {
        var input = new byte[64 * 1024];
        using var line = new MemoryStream();
        while (true)
        {
            var count = await source.ReadAsync(
                input.AsMemory(0, input.Length),
                cancellationToken).ConfigureAwait(false);
            if (count == 0) break;
            var start = 0;
            for (var index = 0; index < count; index++)
            {
                if (input[index] != (byte)'\n') continue;
                Append(line, input.AsSpan(start, index - start));
                var frame = ValidateLine(line);
                await WriteLineAsync(line, cancellationToken).ConfigureAwait(false);
                if (frame.ValueKind == JsonValueKind.Object &&
                    frame.GetProperty("type").GetString() == "complete")
                {
                    complete = frame;
                }
                line.SetLength(0);
                start = index + 1;
            }
            Append(line, input.AsSpan(start, count - start));
        }
        if (line.Length != 0)
        {
            throw new RinProtocolException(
                "invalid_transfer",
                "Rin transfer ended without an LF delimiter");
        }
        if (complete.ValueKind != JsonValueKind.Object)
        {
            throw new RinProtocolException(
                "invalid_transfer",
                "Rin transfer ended without complete");
        }
        return complete;
    }

    private void Append(MemoryStream line, ReadOnlySpan<byte> bytes)
    {
        if (line.Length + bytes.Length > CurrentLimit())
        {
            throw new RinProtocolException(
                "transfer_frame_too_large",
                "Rin transfer frame exceeds its limit");
        }
        line.Write(bytes);
    }

    private int CurrentLimit() =>
        manifest.ValueKind != JsonValueKind.Object ||
        complete.ValueKind == JsonValueKind.Object
            ? ControlFrameMaxBytes
            : EventFrameMaxBytes;

    private JsonElement ValidateLine(MemoryStream line)
    {
        if (line.Length == 0)
        {
            throw new RinProtocolException(
                "invalid_transfer",
                "Rin transfer contains an empty frame");
        }
        JsonElement frame;
        try
        {
            if (!line.TryGetBuffer(out var buffer))
            {
                throw new InvalidOperationException("transfer line buffer is unavailable");
            }
            using var document = JsonDocument.Parse(
                new ReadOnlyMemory<byte>(
                    buffer.Array!,
                    buffer.Offset,
                    checked((int)line.Length)));
            frame = document.RootElement.Clone();
        }
        catch (Exception exception) when (
            exception is JsonException or DecoderFallbackException)
        {
            throw new RinProtocolException(
                "invalid_transfer",
                "Rin transfer contains invalid JSON",
                exception);
        }
        if (frame.ValueKind != JsonValueKind.Object ||
            !frame.TryGetProperty("type", out var typeProperty) ||
            typeProperty.ValueKind != JsonValueKind.String)
        {
            throw new RinProtocolException(
                "invalid_transfer",
                "Rin transfer frame is not an object");
        }
        var type = typeProperty.GetString();
        if (type == "error")
        {
            if (line.Length > ControlFrameMaxBytes)
            {
                throw new RinProtocolException(
                    "transfer_frame_too_large",
                    "Rin transfer error frame exceeds its limit");
            }
            var error = frame.TryGetProperty("error", out var detail) &&
                detail.ValueKind == JsonValueKind.Object
                    ? detail
                    : default;
            throw new RinApiException(
                Text(error, "code", 96, "transfer_failed"),
                Text(error, "message", 500, "Rin transfer failed"),
                field: Text(error, "field", 160));
        }
        if (complete.ValueKind == JsonValueKind.Object)
        {
            throw new RinProtocolException(
                "invalid_transfer",
                "Rin transfer contains data after complete");
        }
        if (manifest.ValueKind != JsonValueKind.Object)
        {
            if (type != "manifest" ||
                Text(frame, "transfer_version", 96) != "rin.session-transfer/v1" ||
                !PositiveSafeInteger(frame, "event_count", out var count) ||
                !PositiveSafeInteger(frame, "terminal_revision", out var revision) ||
                count != revision ||
                !Identifier(frame, "session_id"))
            {
                throw new RinProtocolException(
                    "invalid_transfer",
                    "Rin transfer manifest is invalid");
            }
            manifest = frame;
            return frame;
        }
        if (type == "event")
        {
            if (!frame.TryGetProperty("record", out var record) ||
                record.ValueKind != JsonValueKind.Object ||
                !PositiveSafeInteger(record, "sequence", out var sequence) ||
                sequence != eventCount + 1 ||
                Text(frame, "record_sha256", 64).Length != 64)
            {
                throw new RinProtocolException(
                    "invalid_transfer",
                    "Rin transfer event order is invalid");
            }
            eventCount++;
            PositiveSafeInteger(manifest, "event_count", out var declared);
            if (eventCount > declared)
            {
                throw new RinProtocolException(
                    "invalid_transfer",
                    "Rin transfer contains extra events");
            }
            return frame;
        }
        if (type != "complete" ||
            line.Length > ControlFrameMaxBytes ||
            !PositiveSafeInteger(manifest, "event_count", out var manifestCount) ||
            !PositiveSafeInteger(manifest, "terminal_revision", out var manifestRevision) ||
            !PositiveSafeInteger(frame, "event_count", out var frameCount) ||
            !PositiveSafeInteger(frame, "terminal_revision", out var frameRevision) ||
            eventCount != manifestCount ||
            frameCount != manifestCount ||
            frameRevision != manifestRevision ||
            Text(frame, "terminal_head_hash", 64) !=
                Text(manifest, "terminal_head_hash", 64) ||
            Text(frame, "stream_sha256", 64).Length != 64)
        {
            throw new RinProtocolException(
                "invalid_transfer",
                "Rin transfer complete frame is invalid");
        }
        return frame;
    }

    private async Task WriteLineAsync(
        MemoryStream line,
        CancellationToken cancellationToken)
    {
        if (!line.TryGetBuffer(out var buffer))
        {
            throw new InvalidOperationException("transfer line buffer is unavailable");
        }
        await destination.WriteAsync(
            new ReadOnlyMemory<byte>(
                buffer.Array!,
                buffer.Offset,
                checked((int)line.Length)),
            cancellationToken).ConfigureAwait(false);
        await destination.WriteAsync(
            new ReadOnlyMemory<byte>(new byte[] { (byte)'\n' }),
            cancellationToken).ConfigureAwait(false);
    }

    private static bool Identifier(JsonElement element, string name) =>
        element.TryGetProperty(name, out var value) &&
        value.ValueKind == JsonValueKind.String &&
        value.GetString() is string text &&
        text.Length is >= 1 and <= 96 &&
        IsAsciiLetterOrDigit(text[0]) &&
        text.All(character =>
            IsAsciiLetterOrDigit(character) ||
            character is '.' or '_' or '-');

    private static bool PositiveSafeInteger(
        JsonElement element,
        string name,
        out long value)
    {
        value = 0;
        return element.TryGetProperty(name, out var property) &&
            property.ValueKind == JsonValueKind.Number &&
            property.TryGetInt64(out value) &&
            value is >= 1 and <= 9_007_199_254_740_991 &&
            property.GetRawText().All(character => character is >= '0' and <= '9');
    }

    private static string Text(
        JsonElement element,
        string name,
        int maximum,
        string fallback = "")
    {
        if (element.ValueKind != JsonValueKind.Object ||
            !element.TryGetProperty(name, out var property) ||
            property.ValueKind != JsonValueKind.String)
        {
            return fallback;
        }
        return RinException.SafeText(property.GetString(), maximum, fallback);
    }

    private static bool IsAsciiLetterOrDigit(char value) =>
        value is >= 'a' and <= 'z' or >= 'A' and <= 'Z' or >= '0' and <= '9';
}

internal sealed class NonDisposingReadStream : Stream
{
    private readonly Stream inner;

    internal NonDisposingReadStream(Stream inner)
    {
        this.inner = inner;
    }

    public override bool CanRead => inner.CanRead;
    public override bool CanSeek => inner.CanSeek;
    public override bool CanWrite => false;
    public override long Length => inner.Length;
    public override long Position
    {
        get => inner.Position;
        set => inner.Position = value;
    }

    public override void Flush() => inner.Flush();
    public override int Read(byte[] buffer, int offset, int count) =>
        inner.Read(buffer, offset, count);
    public override ValueTask<int> ReadAsync(
        Memory<byte> buffer,
        CancellationToken cancellationToken = default) =>
        inner.ReadAsync(buffer, cancellationToken);
    public override long Seek(long offset, SeekOrigin origin) =>
        inner.Seek(offset, origin);
    public override void SetLength(long value) =>
        throw new NotSupportedException();
    public override void Write(byte[] buffer, int offset, int count) =>
        throw new NotSupportedException();

    protected override void Dispose(bool disposing)
    {
        // The caller owns the wrapped transfer source.
        base.Dispose(disposing);
    }
}
