package io.github.sunrioa.rin;

public final class RinApiException extends RinException {
    private static final long serialVersionUID = 1L;

    private final int status;
    private final String field;

    public RinApiException(String code, String message, int status, String field) {
        super(code, message);
        this.status = status;
        this.field = safeText(field, 160, "");
    }

    public int status() {
        return status;
    }

    public String field() {
        return field;
    }
}
