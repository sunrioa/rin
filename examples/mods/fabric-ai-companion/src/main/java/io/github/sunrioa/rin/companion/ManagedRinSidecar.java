package io.github.sunrioa.rin.companion;

import io.github.sunrioa.rin.RinClient;

import java.io.IOException;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.file.Path;
import java.time.Duration;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import java.util.function.Function;
import java.util.concurrent.TimeUnit;

final class ManagedRinSidecar implements AutoCloseable {
    @FunctionalInterface
    interface SidecarProcess {
        void stop();
    }

    @FunctionalInterface
    interface ProcessFactory {
        SidecarProcess start(List<String> command, Map<String, String> environment, Path workingDirectory)
                throws IOException;
    }

    @FunctionalInterface
    interface ReadinessProbe {
        boolean ready(URI uri, String token) throws Exception;
    }

    private final Path executable;
    private final Path dataDirectory;
    private final int port;
    private final Function<String, String> environmentReader;
    private final ProcessFactory processFactory;
    private final ReadinessProbe readinessProbe;
    private CompanionModelConfig config;
    private SidecarProcess process;
    private RinClient client;

    ManagedRinSidecar(Path executable, Path dataDirectory, int port, CompanionModelConfig config,
                      Function<String, String> environmentReader, ProcessFactory processFactory,
                      ReadinessProbe readinessProbe) {
        if (port < 1 || port > 65_535) throw new IllegalArgumentException("invalid Rin sidecar port");
        this.executable = executable;
        this.dataDirectory = dataDirectory;
        this.port = port;
        this.config = config;
        this.environmentReader = environmentReader;
        this.processFactory = processFactory;
        this.readinessProbe = readinessProbe;
    }

    synchronized void start() {
        if (process != null) return;
        String token = UUID.randomUUID().toString().replace("-", "");
        Map<String, String> environment = childEnvironment(token);
        List<String> command = List.of(executable.toString(), "serve", "-addr", localAddress(),
                "-data", dataDirectory.toString());
        try {
            process = processFactory.start(command, environment, executable.toAbsolutePath().getParent());
            URI ready = URI.create("http://" + localAddress() + "/ready");
            if (!readinessProbe.ready(ready, token)) throw new IllegalStateException("Rin sidecar was not ready");
            client = new RinClient("http://" + localAddress(), token, Duration.ofSeconds(10),
                    RinClient.DEFAULT_MAX_RESPONSE_BYTES, new GsonJsonCodec());
        } catch (Exception exception) {
            stopOwnedProcess();
            throw new IllegalStateException("could not start Rin sidecar", exception);
        }
    }

    synchronized void applyConfig(CompanionModelConfig next) {
        stopOwnedProcess();
        config = next;
        start();
    }

    synchronized RinClient client() {
        if (client == null) throw new IllegalStateException("Rin sidecar is not running");
        return client;
    }

    @Override
    public synchronized void close() {
        stopOwnedProcess();
    }

    private Map<String, String> childEnvironment(String token) {
        Map<String, String> values = new LinkedHashMap<>();
        values.put("RIN_POLICY", "model");
        values.put("RIN_MODEL_BASE_URL", config.baseUrl().toString());
        values.put("RIN_MODEL", config.model());
        String apiKey = environmentReader.apply("RIN_MODEL_API_KEY");
        values.put("RIN_MODEL_API_KEY", apiKey == null ? "" : apiKey);
        values.put("RIN_TOKEN", token);
        return values;
    }

    private String localAddress() {
        return "127.0.0.1:" + port;
    }

    private void stopOwnedProcess() {
        if (process == null) return;
        SidecarProcess owned = process;
        process = null;
        client = null;
        owned.stop();
    }

    static ProcessFactory systemProcessFactory() {
        return (command, environment, workingDirectory) -> {
            ProcessBuilder builder = new ProcessBuilder(command);
            if (workingDirectory != null) builder.directory(workingDirectory.toFile());
            builder.environment().putAll(environment);
            builder.redirectErrorStream(true);
            builder.redirectOutput(ProcessBuilder.Redirect.DISCARD);
            Process child = builder.start();
            return () -> {
                child.destroy();
                try {
                    if (!child.waitFor(2, TimeUnit.SECONDS)) child.destroyForcibly();
                } catch (InterruptedException exception) {
                    Thread.currentThread().interrupt();
                    child.destroyForcibly();
                }
            };
        };
    }

    static ReadinessProbe httpReadinessProbe() {
        HttpClient http = HttpClient.newBuilder().connectTimeout(Duration.ofSeconds(1)).build();
        return (uri, token) -> {
            for (int attempt = 0; attempt < 50; attempt++) {
                try {
                    HttpRequest request = HttpRequest.newBuilder(uri)
                            .timeout(Duration.ofSeconds(1))
                            .header("Authorization", "Bearer " + token)
                            .GET().build();
                    if (http.send(request, HttpResponse.BodyHandlers.discarding()).statusCode() == 200) return true;
                } catch (IOException ignored) {
                    // The child may still be starting.
                }
                Thread.sleep(100);
            }
            return false;
        };
    }
}
