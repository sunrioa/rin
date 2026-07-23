package io.github.sunrioa.rin;

public final class RinProtocolException extends RinException {
    private static final long serialVersionUID = 1L;

    public RinProtocolException(String code, String message) {
        super(code, message);
    }

    public RinProtocolException(String code, String message, Throwable cause) {
        super(code, message, cause);
    }
}
