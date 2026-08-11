package io.github.sunrioa.rin;

/** Source-first test entry point used without a build-system dependency. */
public final class RinSdkTest {
    private RinSdkTest() { }

    public static void main(String[] args) throws Exception {
        HostActionContractTest.run();
        HostControlSessionTest.run();
        RinControlClientTest.run();
        RinAgentClientTest.run();
    }
}
