package io.github.sunrioa.rin;

public class RinException extends RuntimeException {
    private static final long serialVersionUID = 1L;

    private final String code;

    public RinException(String code, String message) {
        this(code, message, null);
    }

    public RinException(String code, String message, Throwable cause) {
        super(safeText(message, 500, "Rin request failed"), cause);
        this.code = safeText(code, 96, "rin_error");
    }

    public String code() {
        return code;
    }

    static String safeText(Object value, int maximum, String fallback) {
        String cleaned = String.valueOf(value == null ? "" : value)
                .replace('\0', ' ')
                .trim()
                .replaceAll("\\s+", " ");
        if (cleaned.isEmpty()) return fallback;
        return cleaned.substring(0, Math.min(cleaned.length(), maximum));
    }
}
