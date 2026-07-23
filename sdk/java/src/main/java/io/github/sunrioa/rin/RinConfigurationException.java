package io.github.sunrioa.rin;

public final class RinConfigurationException extends RinException {
    private static final long serialVersionUID = 1L;

    public RinConfigurationException(String code, String message) {
        super(code, message);
    }

    public RinConfigurationException(String code, String message, Throwable cause) {
        super(code, message, cause);
    }
}
