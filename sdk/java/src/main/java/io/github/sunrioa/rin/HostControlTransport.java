package io.github.sunrioa.rin;

import java.io.IOException;
import java.util.Map;

/** Engine-provided transport for the loopback Rin Host Control service. */
@FunctionalInterface
public interface HostControlTransport {
    Map<String, Object> post(String path, Map<String, ?> body)
            throws IOException, InterruptedException;
}
