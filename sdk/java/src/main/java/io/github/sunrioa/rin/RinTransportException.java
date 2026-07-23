package io.github.sunrioa.rin;

public final class RinTransportException extends RinException {
    private static final long serialVersionUID = 1L;

    public RinTransportException(String code, String message) {
        super(code, message);
    }

    public RinTransportException(String code, String message, Throwable cause) {
        super(code, message, cause);
    }
}
